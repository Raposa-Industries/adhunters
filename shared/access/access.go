// Package access checks the Cloudflare Access login of an app's pages:
// Access puts a signed token in the Cf-Access-Jwt-Assertion header of every
// request it lets through, and the email in a valid token is who is asking.
// A request without a valid one got around Access and is refused. Moved
// from Spy when Desk needed it.
package access

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
	"os"
	"strings"
	"sync"
	"time"
)

// Checker checks the token on every request. The keys come from the team's
// certs address and are read again every hour, or when a token names a key
// not seen yet.
type Checker struct {
	Team     string // the team's address, https://<team>.cloudflareaccess.com
	Audience string // the application's AUD tag
	Client   *http.Client
	Now      func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// FromEnv reads ACCESS_TEAM and ACCESS_AUD. Both set give a Checker;
// neither gives nil, for an app that runs without Access (on a laptop); one
// alone is a mistake.
func FromEnv() (*Checker, error) {
	team, aud := strings.TrimSpace(os.Getenv("ACCESS_TEAM")), strings.TrimSpace(os.Getenv("ACCESS_AUD"))
	switch {
	case team == "" && aud == "":
		return nil, nil
	case team == "" || aud == "":
		return nil, errors.New("set both ACCESS_TEAM and ACCESS_AUD, or neither")
	case !strings.HasPrefix(team, "https://"):
		return nil, fmt.Errorf("ACCESS_TEAM is the team's address, https://<team>.cloudflareaccess.com, not %q", team)
	}
	return &Checker{Team: team, Audience: aud}, nil
}

type ctxEmail struct{}

// Email is the email of the person asking, as Wrap (or WithEmail) recorded
// it, or "" for no one.
func Email(r *http.Request) string {
	e, _ := r.Context().Value(ctxEmail{}).(string)
	return e
}

// WithEmail records who is asking without a token: the one person of an app
// run on a laptop without Access, or a test's.
func WithEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, ctxEmail{}, strings.ToLower(strings.TrimSpace(email)))
}

// Wrap refuses requests without a valid token and records the email.
func (a *Checker) Wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		email, err := a.Check(r.Context(), r.Header.Get("Cf-Access-Jwt-Assertion"))
		if err != nil {
			http.Error(w, "sign in through Cloudflare Access", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r.WithContext(WithEmail(r.Context(), email)))
	})
}

// Check verifies a token (RS256, our audience, the team as issuer, not
// expired) and returns its email.
func (a *Checker) Check(ctx context.Context, token string) (string, error) {
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

func (a *Checker) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// key returns the public key with this id, reading the certs when it is
// unknown or they are an hour old (at most once a minute).
func (a *Checker) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
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

func (a *Checker) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
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
