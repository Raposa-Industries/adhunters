// Package signin is the team's sign-in at hunt-teste.fyi: one username and
// password for the whole site, checked by create-web in front of every app
// (it replaced Cloudflare Access on 2026-10-01, owner's word). A good
// password gives a signed cookie that lasts 30 days; every request without
// one is sent to the sign-in page (pages) or refused (everything else).
//
// Only a hash of the password is kept (PBKDF2-SHA256, written by
// `create-web hash-password`), in /etc/adhunters/create-web.env. The cookie
// is signed with a key derived from that hash, so a new password signs
// everyone out.
package signin

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Path is the sign-in page; Path+"/out" signs out.
	Path = "/_signin"
	// Cookie is the name of the session cookie.
	Cookie = "ah_session"
	// Lasts is how long a sign-in lasts; past half of it, it is renewed.
	Lasts = 30 * 24 * time.Hour

	scheme = "pbkdf2-sha256"

	// After maxFails wrong passwords from one address within failWindow,
	// that address is refused until the window passes.
	maxFails   = 10
	failWindow = 15 * time.Minute
)

// iterations is PBKDF2's work for a new hash (a var so tests run fast).
var iterations = 600_000

// Hash returns the stored form of a password:
// pbkdf2-sha256:<iterations>:<salt>:<key>, salt and key in base64url. No $
// in it, so it sits in an env file as is.
func Hash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return "", err
	}
	b := base64.RawURLEncoding
	return fmt.Sprintf("%s:%d:%s:%s", scheme, iterations, b.EncodeToString(salt), b.EncodeToString(key)), nil
}

type hashed struct {
	iter      int
	salt, key []byte
}

func parseHash(s string) (hashed, error) {
	p := strings.Split(strings.TrimSpace(s), ":")
	if len(p) != 4 || p[0] != scheme {
		return hashed{}, errors.New("not a hash from create-web hash-password")
	}
	iter, err := strconv.Atoi(p[1])
	if err != nil || iter < 100_000 {
		return hashed{}, errors.New("hash: bad iteration count")
	}
	salt, err1 := base64.RawURLEncoding.DecodeString(p[2])
	key, err2 := base64.RawURLEncoding.DecodeString(p[3])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(key) < 16 {
		return hashed{}, errors.New("hash: bad salt or key")
	}
	return hashed{iter, salt, key}, nil
}

func (h hashed) matches(password string) bool {
	got, err := pbkdf2.Key(sha256.New, password, h.salt, h.iter, len(h.key))
	return err == nil && subtle.ConstantTimeCompare(got, h.key) == 1
}

// Gate checks the sign-in in front of a handler.
type Gate struct {
	user string
	hash hashed
	key  []byte // signs cookies
	Now  func() time.Time

	checks chan struct{} // at most a few password checks at once: each costs CPU

	mu    sync.Mutex
	fails map[string][]time.Time
}

// New makes a Gate for one username and the stored hash of its password.
func New(user, hash string) (*Gate, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return nil, errors.New("no username")
	}
	h, err := parseHash(hash)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, []byte("adhunters sign-in cookie"))
	m.Write([]byte(strings.TrimSpace(hash)))
	return &Gate{user: user, hash: h, key: m.Sum(nil), checks: make(chan struct{}, 2), fails: map[string][]time.Time{}}, nil
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Wrap serves the sign-in page and lets through only signed-in requests.
// The session cookie is taken off each request before next sees it.
func (g *Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case Path:
			g.page(w, r)
			return
		case Path + "/out":
			http.SetCookie(w, g.cookie("", -1))
			http.Redirect(w, r, Path, http.StatusSeeOther)
			return
		}
		exp, ok := g.valid(r)
		if !ok {
			if (r.Method == http.MethodGet || r.Method == http.MethodHead) && wantsPage(r) {
				http.Redirect(w, r, Path+"?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			http.Error(w, "entre em "+Path, http.StatusUnauthorized)
			return
		}
		if exp.Sub(g.now()) < Lasts/2 {
			http.SetCookie(w, g.cookie(g.sign(g.now().Add(Lasts)), int(Lasts/time.Second)))
		}
		dropCookie(r)
		next.ServeHTTP(w, r)
	})
}

func wantsPage(r *http.Request) bool {
	a := r.Header.Get("Accept")
	return a == "" || strings.Contains(a, "text/html") || strings.Contains(a, "*/*") && r.Header.Get("Sec-Fetch-Mode") == "navigate"
}

// dropCookie removes the session cookie from the request, keeping the others.
func dropCookie(r *http.Request) {
	cs := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cs {
		if c.Name != Cookie {
			r.AddCookie(c)
		}
	}
}

// sign makes a cookie value: user|unix expiry, then its HMAC.
func (g *Gate) sign(exp time.Time) string {
	body := base64.RawURLEncoding.EncodeToString([]byte(g.user + "|" + strconv.FormatInt(exp.Unix(), 10)))
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (g *Gate) valid(r *http.Request) (time.Time, bool) {
	c, err := r.Cookie(Cookie)
	if err != nil {
		return time.Time{}, false
	}
	body, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return time.Time{}, false
	}
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte(body))
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, m.Sum(nil)) {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return time.Time{}, false
	}
	user, unix, ok := strings.Cut(string(raw), "|")
	n, err := strconv.ParseInt(unix, 10, 64)
	if !ok || err != nil || user != g.user {
		return time.Time{}, false
	}
	exp := time.Unix(n, 0)
	return exp, g.now().Before(exp)
}

func (g *Gate) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: Cookie, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

// client is the address asking: Cloudflare's header when the request came
// through the local tunnel, else the peer.
func client(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if cf := strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip")); cf != "" {
			return cf
		}
	}
	return host
}

func (g *Gate) blocked(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	cut := g.now().Add(-failWindow)
	kept := g.fails[ip][:0]
	for _, t := range g.fails[ip] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(g.fails, ip)
	} else {
		g.fails[ip] = kept
	}
	return len(kept) >= maxFails
}

func (g *Gate) failed(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.fails) > 10_000 { // a flood of addresses: forget the oldest state rather than grow
		g.fails = map[string][]time.Time{}
	}
	g.fails[ip] = append(g.fails[ip], g.now())
}

func (g *Gate) page(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if _, ok := g.valid(r); ok {
			http.Redirect(w, r, next, http.StatusFound)
			return
		}
		render(w, http.StatusOK, next, "")
	case http.MethodPost:
		ip := client(r)
		if g.blocked(ip) {
			render(w, http.StatusTooManyRequests, next, "Muitas tentativas. Espere 15 minutos.")
			return
		}
		g.checks <- struct{}{}
		ok := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(r.PostFormValue("user"))), []byte(g.user)) == 1
		ok = g.hash.matches(r.PostFormValue("password")) && ok
		<-g.checks
		if !ok {
			g.failed(ip)
			render(w, http.StatusUnauthorized, next, "Usuário ou senha errados.")
			return
		}
		http.SetCookie(w, g.cookie(g.sign(g.now().Add(Lasts)), int(Lasts/time.Second)))
		http.Redirect(w, r, next, http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// safeNext keeps a redirect on this site: a path, never //host or /\host.
func safeNext(s string) string {
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/\\") || strings.HasPrefix(s, Path) {
		return "/"
	}
	return s
}

func render(w http.ResponseWriter, status int, next, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, struct{ Next, Msg, Path string }{next, msg, Path})
}

var pageTmpl = template.Must(template.New("signin").Parse(`<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Entrar · AdHunters</title>
<style>
:root{color-scheme:light dark;--bg:#f6f5f2;--card:#fff;--fg:#1d1b18;--mute:#6b665e;--line:#ddd8cf;--acc:#d9622b;--err:#b3261e}
@media (prefers-color-scheme:dark){:root{--bg:#161412;--card:#201d1a;--fg:#eeeae4;--mute:#a39d94;--line:#38332d;--err:#f2b8b5}}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--fg);font:15px/1.4 system-ui,sans-serif;padding:16px}
form{width:100%;max-width:320px;background:var(--card);border:1px solid var(--line);border-radius:12px;padding:24px;display:grid;gap:12px}
h1{font-size:18px;margin:0 0 4px}label{display:grid;gap:4px;color:var(--mute);font-size:13px}
input{font:inherit;padding:9px 10px;border:1px solid var(--line);border-radius:8px;background:transparent;color:var(--fg)}
button{font:inherit;font-weight:600;padding:10px;border:0;border-radius:8px;background:var(--acc);color:#fff;cursor:pointer}
p{margin:0;color:var(--err);font-size:13px}
</style></head><body>
<form method="post" action="{{.Path}}">
<h1>AdHunters</h1>
{{if .Msg}}<p role="alert">{{.Msg}}</p>{{end}}
<input type="hidden" name="next" value="{{.Next}}">
<label>Usuário<input name="user" autocomplete="username" autocapitalize="none" required autofocus></label>
<label>Senha<input name="password" type="password" autocomplete="current-password" required></label>
<button type="submit">Entrar</button>
</form></body></html>`))
