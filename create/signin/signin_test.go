package signin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func init() { iterations = 100_000 }

func newGate(t *testing.T) *Gate {
	t.Helper()
	h, err := Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	g, err := New("team", h)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// app answers with the cookies it was given, so tests see what passed through.
var app = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("app " + r.Header.Get("Cookie")))
})

func post(h http.Handler, ip, user, pw, next string) *httptest.ResponseRecorder {
	form := url.Values{"user": {user}, "password": {pw}, "next": {next}}
	req := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Cf-Connecting-Ip", ip)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHashRoundTrip(t *testing.T) {
	h, err := Hash("s3cret!@#")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h, "$") || strings.Contains(h, "s3cret") {
		t.Fatalf("hash %q", h)
	}
	p, err := parseHash(h)
	if err != nil {
		t.Fatal(err)
	}
	if !p.matches("s3cret!@#") || p.matches("s3cret!@") {
		t.Fatal("matches is wrong")
	}
	if _, err := New("team", "plain"); err == nil {
		t.Fatal("a plain password was taken as a hash")
	}
	if _, err := New("", h); err == nil {
		t.Fatal("no username was taken")
	}
}

func TestWithoutSignIn(t *testing.T) {
	h := newGate(t).Wrap(app)

	req := httptest.NewRequest(http.MethodGet, "/launch/campaigns?x=1", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != Path+"?next=%2Flaunch%2Fcampaigns%3Fx%3D1" {
		t.Fatalf("page: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	req = httptest.NewRequest(http.MethodGet, "/launch/api/tree", nil)
	req.Header.Set("Accept", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "app") {
		t.Fatalf("api: %d %q", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/launch/api/x", nil)
	req.AddCookie(&http.Cookie{Name: Cookie, Value: "forged.value"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged cookie: %d", rec.Code)
	}
}

func TestSignInThenThrough(t *testing.T) {
	g := newGate(t)
	h := g.Wrap(app)

	rec := post(h, "1.2.3.4", "team", "wrong", "/spy/")
	if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	rec = post(h, "1.2.3.4", "other", "correct horse", "/spy/")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong user: %d", rec.Code)
	}
	rec = post(h, "1.2.3.4", "team", "correct horse", "/spy/?a=1")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/spy/?a=1" {
		t.Fatalf("sign-in: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 || !cs[0].HttpOnly || !cs[0].Secure || cs[0].MaxAge != int(Lasts/time.Second) {
		t.Fatalf("cookie %+v", cs)
	}

	req := httptest.NewRequest(http.MethodPost, "/launch/api/x", nil)
	req.AddCookie(cs[0])
	req.AddCookie(&http.Cookie{Name: "app_pref", Value: "1"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "app app_pref=1" {
		t.Fatalf("through: %d %q (the session cookie must not reach the app)", rec.Code, rec.Body.String())
	}

	// Past 30 days the cookie is refused; past half of it, renewed.
	g.Now = func() time.Time { return time.Now().Add(16 * 24 * time.Hour) }
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(cs[0])
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("renewal: %d %d", rec.Code, len(rec.Result().Cookies()))
	}
	g.Now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(cs[0])
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("an expired cookie got through")
	}

	// A new password signs everyone out.
	g.Now = nil
	g2 := newGate(t)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(cs[0])
	g2.Wrap(app).ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("a cookie from the old password got through")
	}
}

func TestTooManyTries(t *testing.T) {
	g := newGate(t)
	h := g.Wrap(app)
	for range maxFails {
		post(h, "9.9.9.9", "team", "nope", "/")
	}
	if rec := post(h, "9.9.9.9", "team", "correct horse", "/"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d misses: %d", maxFails, rec.Code)
	}
	if rec := post(h, "8.8.8.8", "team", "correct horse", "/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("another address: %d", rec.Code)
	}
	g.Now = func() time.Time { return time.Now().Add(failWindow + time.Minute) }
	if rec := post(h, "9.9.9.9", "team", "correct horse", "/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("after the window: %d", rec.Code)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/launch/":         "/launch/",
		"":                 "/",
		"https://evil.com": "/",
		"//evil.com":       "/",
		"/\\evil.com":      "/",
		Path + "/out":      "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSignOut(t *testing.T) {
	rec := httptest.NewRecorder()
	newGate(t).Wrap(app).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, Path+"/out", nil))
	cs := rec.Result().Cookies()
	if rec.Code != http.StatusSeeOther || len(cs) != 1 || cs[0].MaxAge >= 0 {
		t.Fatalf("sign-out: %d %+v", rec.Code, cs)
	}
}
