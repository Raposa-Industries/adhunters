package web

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Access checks the Cloudflare Access login on every request: Access puts a
// signed token in the Cf-Access-Jwt-Assertion header, and a request without
// a valid one (one that got around Access) is refused. The keys come from
// the team's certs address and are read again every hour, or when a token
// names a key not seen yet.
type Access struct {
	Team     string // the team's address, https://<team>.cloudflareaccess.com
	Audience string // the application's AUD tag
	Client   *http.Client
	Now      func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

type ctxUser struct{}

// who is the email of the person asking, or "" without Access.
func who(r *http.Request) string {
	u, _ := r.Context().Value(ctxUser{}).(string)
	return u
}

// Wrap refuses requests without a valid token and records the email.
func (a *Access) Wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		email, err := a.Check(r.Context(), r.Header.Get("Cf-Access-Jwt-Assertion"))
		if err != nil {
			http.Error(w, "sign in through Cloudflare Access", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser{}, email)))
	})
}

// Check verifies a token (RS256, our audience, the team as issuer, not
// expired) and returns its email.
func (a *Access) Check(ctx context.Context, token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("no token")
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &head); err != nil {
		return "", err
	}
	if head.Alg != "RS256" {
		return "", fmt.Errorf("alg %s", head.Alg)
	}
	key, err := a.key(ctx, head.Kid)
	if err != nil {
		return "", err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return "", err
	}
	var claims struct {
		Aud   audience `json:"aud"`
		Iss   string   `json:"iss"`
		Exp   int64    `json:"exp"`
		Nbf   int64    `json:"nbf"`
		Email string   `json:"email"`
	}
	if err := decodePart(parts[1], &claims); err != nil {
		return "", err
	}
	now := a.now().Unix()
	switch {
	case !claims.Aud.has(a.Audience):
		return "", errors.New("wrong audience")
	case strings.TrimSuffix(claims.Iss, "/") != strings.TrimSuffix(a.Team, "/"):
		return "", errors.New("wrong issuer")
	case claims.Exp == 0 || now >= claims.Exp+30:
		return "", errors.New("expired")
	case claims.Nbf != 0 && now < claims.Nbf-30:
		return "", errors.New("not yet valid")
	}
	return claims.Email, nil
}

func (a *Access) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// key returns the public key with this id, reading the certs when it is
// unknown or they are an hour old (at most once a minute).
func (a *Access) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	k, ok := a.keys[kid]
	stale := a.now().Sub(a.fetched) > time.Hour
	if ok && !stale {
		return k, nil
	}
	if a.now().Sub(a.fetched) < time.Minute && !stale && a.keys != nil {
		return nil, errors.New("unknown key")
	}
	keys, err := a.fetch(ctx)
	if err != nil {
		if ok {
			return k, nil // keep using the known key while the certs cannot be read
		}
		return nil, err
	}
	a.keys, a.fetched = keys, a.now()
	if k, ok = keys[kid]; !ok {
		return nil, errors.New("unknown key")
	}
	return k, nil
}

func (a *Access) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	c := a.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(a.Team, "/")+"/cdn-cgi/access/certs", nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("certs: %s", res.Status)
	}
	var body struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range body.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(keys) == 0 {
		return nil, errors.New("certs: no RSA keys")
	}
	return keys, nil
}

func decodePart(p string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// audience is a token's aud: one string or a list.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

func (a audience) has(want string) bool {
	for _, x := range a {
		if x == want {
			return true
		}
	}
	return false
}
