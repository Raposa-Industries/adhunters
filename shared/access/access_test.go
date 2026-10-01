package access

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAccess(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	certs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cdn-cgi/access/certs" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA", "n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer certs.Close()

	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	a := &Checker{Team: certs.URL, Audience: "aud-1", Client: certs.Client(), Now: func() time.Time { return at }}
	sign := func(k *rsa.PrivateKey, kid string, claims map[string]any) string {
		head, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid})
		body, _ := json.Marshal(claims)
		msg := b64(head) + "." + b64(body)
		sum := sha256.Sum256([]byte(msg))
		sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		return msg + "." + b64(sig)
	}
	good := map[string]any{"aud": []string{"aud-1"}, "iss": certs.URL, "exp": at.Add(time.Hour).Unix(), "email": "mari@example.com"}
	with := func(k string, v any) map[string]any {
		c := map[string]any{}
		for kk, vv := range good {
			c[kk] = vv
		}
		c[k] = v
		return c
	}

	ctx := context.Background()
	if email, err := a.Check(ctx, sign(key, "k1", good)); err != nil || email != "mari@example.com" {
		t.Fatalf("good token: %q %v", email, err)
	}
	if _, err := a.Check(ctx, sign(key, "k1", with("aud", "aud-1"))); err != nil {
		t.Errorf("aud as one string: %v", err)
	}
	for name, tok := range map[string]string{
		"none":         "",
		"other key":    sign(other, "k1", good),
		"unknown kid":  sign(key, "k2", good),
		"wrong aud":    sign(key, "k1", with("aud", []string{"aud-2"})),
		"wrong issuer": sign(key, "k1", with("iss", "https://evil.cloudflareaccess.com")),
		"expired":      sign(key, "k1", with("exp", at.Add(-time.Hour).Unix())),
		"not yet":      sign(key, "k1", with("nbf", at.Add(time.Hour).Unix())),
	} {
		if _, err := a.Check(ctx, tok); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// Wrap refuses without a token, even with Access's email header (which
	// anything on the box could send), and passes the email on with one.
	h := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(Email(r))) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/app/", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("no token: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "/app/", nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", "mari@example.com")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || w.Body.String() == "mari@example.com" {
		t.Errorf("the email header alone: %d %q", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("GET", "/app/", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", sign(key, "k1", with("email", "Mari@Example.com")))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "mari@example.com" {
		t.Errorf("with token: %d %q", w.Code, w.Body.String())
	}
}

func TestFromEnv(t *testing.T) {
	for _, c := range []struct {
		team, aud string
		check     bool
		fails     bool
	}{
		{"", "", false, false},
		{"https://acme.cloudflareaccess.com", "aud-1", true, false},
		{"https://acme.cloudflareaccess.com", "", false, true},
		{"", "aud-1", false, true},
		{"FILL_ME", "FILL_ME", false, true},
	} {
		t.Setenv("ACCESS_TEAM", c.team)
		t.Setenv("ACCESS_AUD", c.aud)
		a, err := FromEnv()
		if (err != nil) != c.fails || (a != nil) != c.check {
			t.Errorf("%q %q: %v, %v", c.team, c.aud, a, err)
		}
		if a != nil && (a.Team != c.team || a.Audience != c.aud) {
			t.Errorf("%q %q: %+v", c.team, c.aud, a)
		}
	}
}

func TestWithEmail(t *testing.T) {
	r := httptest.NewRequest("GET", "/app/", nil)
	if Email(r) != "" {
		t.Errorf("no one: %q", Email(r))
	}
	r = r.WithContext(WithEmail(r.Context(), " Ana@Example.com "))
	if Email(r) != "ana@example.com" {
		t.Errorf("%q", Email(r))
	}
}
