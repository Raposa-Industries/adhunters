package logins

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Raposa-Industries/adhunters/launch/internal/store"
)

// fakeProxy is a plain HTTP forward proxy (httptest) that wants a user and
// password, marks what it carries with "Via: fake-proxy" and counts it.
// Closing it makes every request through it fail.
type fakeProxy struct {
	srv  *httptest.Server
	addr string // http://user:password@host:port
	mu   sync.Mutex
	n    int
	bad  int // requests refused for a wrong user or password
}

func newFakeProxy(t *testing.T, user, pass string) *fakeProxy {
	p := &fakeProxy{}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	out := &http.Transport{} // straight to the target: never another proxy
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != want {
			p.mu.Lock()
			p.bad++
			p.mu.Unlock()
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		if !r.URL.IsAbs() {
			http.Error(w, "not a proxy request", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.n++
		p.mu.Unlock()
		req, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		req.Header = r.Header.Clone()
		req.Header.Del("Proxy-Authorization")
		req.Header.Set("Via", "fake-proxy")
		res, err := out.RoundTrip(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	t.Cleanup(p.srv.Close)
	u, _ := url.Parse(p.srv.URL)
	u.User = url.UserPassword(user, pass)
	p.addr = u.String()
	return p
}

func (p *fakeProxy) carried() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func (p *fakeProxy) hostPort() string { return strings.TrimPrefix(p.srv.URL, "http://") }

// askedBy is what Taboola got from one client id.
func (b *backstage) askedBy(id string) []string {
	var out []string
	for _, g := range b.asked() {
		if strings.HasPrefix(g, id+" ") {
			out = append(out, g)
		}
	}
	return out
}

func allVia(t *testing.T, got []string, how string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatal("Taboola was asked nothing")
	}
	for _, g := range got {
		if !strings.HasSuffix(g, " "+how) {
			t.Errorf("%q did not come %s", g, how)
		}
	}
}

const newID = "new-id-0123456789"

func TestAddedLoginGoesOnlyThroughItsProxy(t *testing.T) {
	r := setup(t)
	if _, err := r.s.Add(ctx, "ana", "Nova", newID, "4242", "new-secret", r.px.addr, []string{"new-1-sc", "new-2-sc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.net.Campaigns(ctx, "new-2-sc"); err != nil {
		t.Fatal(err)
	}
	// The check, the token and every read went through the proxy.
	got := r.bs.askedBy(newID)
	allVia(t, got, "proxy")
	if !strings.Contains(strings.Join(got, "\n"), newID+" POST token proxy") {
		t.Errorf("the token did not come through the proxy: %v", got)
	}
	if r.px.carried() < len(got) {
		t.Errorf("the proxy carried %d of %d", r.px.carried(), len(got))
	}

	list, err := r.s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	added := list[len(list)-1]
	if added.Proxy != r.px.hostPort() || added.UserID != "4242" || len(added.Accounts) != 2 || added.Accounts[0].Proxy != r.px.hostPort() {
		t.Fatalf("listed %+v", added)
	}

	// The proxy goes down: the request fails, says so, and nothing goes
	// direct, the token included.
	before := len(r.bs.askedBy(newID))
	r.px.srv.Close()
	_, err = r.net.Campaigns(ctx, "new-1-sc")
	if err == nil || !strings.Contains(err.Error(), "o proxy "+r.px.hostPort()) || !strings.Contains(err.Error(), "nada foi enviado direto") {
		t.Fatalf("got %v; want the proxy's failure", err)
	}
	if strings.Contains(err.Error(), "p4ss") {
		t.Fatalf("the error holds the password: %v", err)
	}
	r.net.Set(nil)
	if err := r.s.Load(ctx); err != nil { // a restart: a new client asks for a token
		t.Fatal(err)
	}
	if _, err := r.net.Campaigns(ctx, "new-1-sc"); err == nil {
		t.Fatal("a request went out without its proxy")
	}
	if now := r.bs.askedBy(newID); len(now) != before {
		t.Fatalf("Taboola was asked without the proxy: %v", now[before:])
	}

	// A new proxy is checked through itself before it is kept.
	p2 := newFakeProxy(t, "bia", "s3nha")
	if err := r.s.SetLoginProxy(ctx, added.ID, p2.addr); err != nil {
		t.Fatal(err)
	}
	if _, err := r.net.Campaigns(ctx, "new-1-sc"); err != nil {
		t.Fatal(err)
	}
	allVia(t, r.bs.askedBy(newID), "proxy")
	if p2.carried() == 0 {
		t.Fatal("the new proxy carried nothing")
	}
	refused(t, r.s.SetLoginProxy(ctx, added.ID, ""), "falta o proxy")
	wrong := strings.Replace(p2.addr, "s3nha", "nope", 1)
	if err := r.s.SetLoginProxy(ctx, added.ID, wrong); err == nil || !strings.Contains(err.Error(), "recusou o usuário e a senha") {
		t.Fatalf("a proxy that refuses the password: %v", err)
	}

	if err := r.s.SetUserID(ctx, added.ID, " 777 "); err != nil {
		t.Fatal(err)
	}
	refused(t, r.s.SetUserID(ctx, added.ID, ""), "user ID")
	if row, _ := r.st.Login(ctx, added.ID); row.UserID != "777" {
		t.Fatalf("user id %q", row.UserID)
	}
}

func TestLoginWithoutProxyIsRefused(t *testing.T) {
	r := setup(t)
	sealed, err := r.box.Seal([]byte("new-secret"), bound("taboola", newID))
	if err != nil {
		t.Fatal(err)
	}
	// A login added before proxies.
	if _, err := r.st.AddLogin(ctx, store.Login{Network: "taboola", Name: "Old", ClientID: newID, Secret: sealed, Accounts: []string{"new-1-sc"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.net.Campaigns(ctx, "new-1-sc"); err == nil || !strings.Contains(err.Error(), "não tem proxy") {
		t.Fatalf("got %v", err)
	}
	if got := r.bs.askedBy(newID); len(got) != 0 {
		t.Fatalf("Taboola was asked: %v", got)
	}
	list, _ := r.s.List(ctx)
	if a := list[len(list)-1].Accounts; len(a) != 1 || !strings.Contains(a[0].Problem, "não tem proxy") {
		t.Fatalf("listed %+v", a)
	}
}

func TestServerAccountsMayHaveAProxy(t *testing.T) {
	r := setup(t)
	if err := r.s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.net.Campaigns(ctx, "zolta-1-sc"); err != nil {
		t.Fatal(err)
	}
	allVia(t, r.bs.askedBy("srv-id"), "direct") // without one, direct as before

	refused(t, r.s.SetAccountProxy(ctx, "ana", "new-1-sc", r.px.addr), "muda pelo acesso")
	refused(t, r.s.SetAccountProxy(ctx, "ana", "zolta-1-sc", "ftp://x:1"), "proxy inválido")
	if err := r.s.SetAccountProxy(ctx, "ana", "zolta-1-sc", r.px.addr); err != nil {
		t.Fatal(err)
	}
	before := len(r.bs.askedBy("srv-id"))
	if _, err := r.net.Campaigns(ctx, "zolta-1-sc"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.net.Accounts(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := r.s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a := list[0].Accounts; len(a) != 1 || a[0].ID != "zolta-1-sc" || a[0].Name != "Zolta 1" || a[0].Proxy != r.px.hostPort() {
		t.Fatalf("listed %+v", a)
	}
	allVia(t, r.bs.askedBy("srv-id")[before:], "proxy")

	// Down: refused, never direct.
	before = len(r.bs.askedBy("srv-id"))
	r.px.srv.Close()
	r.net.Set(nil)
	if err := r.s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.net.Campaigns(ctx, "zolta-1-sc"); err == nil || !strings.Contains(err.Error(), "nada foi enviado direto") {
		t.Fatalf("got %v", err)
	}
	_, _ = r.net.Accounts(ctx)
	if l, _ := r.s.List(ctx); len(l[0].Accounts) != 1 || !strings.Contains(l[0].Accounts[0].Problem, "o proxy") {
		t.Fatalf("listed %+v", l[0].Accounts)
	}
	if now := r.bs.askedBy("srv-id"); len(now) != before {
		t.Fatalf("Taboola was asked without the proxy: %v", now[before:])
	}

	// Taken away: direct again.
	if err := r.s.SetAccountProxy(ctx, "ana", "zolta-1-sc", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.net.Campaigns(ctx, "zolta-1-sc"); err != nil {
		t.Fatal(err)
	}
	if last := r.bs.askedBy("srv-id"); !strings.HasSuffix(last[len(last)-1], " direct") {
		t.Fatalf("not direct: %v", last)
	}
}

func TestProxyIsSealedAndNeverKeptOrLogged(t *testing.T) {
	r := setup(t)
	if _, err := r.s.Add(ctx, "", "Nova", newID, "uid-zz91", "new-secret", r.px.addr, []string{"new-1-sc"}); err != nil {
		t.Fatal(err)
	}
	if err := r.s.SetAccountProxy(ctx, "", "zolta-1-sc", r.px.addr); err != nil {
		t.Fatal(err)
	}
	if _, err := r.net.Campaigns(ctx, "new-1-sc"); err != nil {
		t.Fatal(err)
	}
	rows, _ := r.st.Logins(ctx)
	proxies, _ := r.st.AccountProxies(ctx, "taboola")
	for _, sealed := range [][]byte{rows[0].Proxy, proxies["zolta-1-sc"]} {
		if len(sealed) == 0 || bytes.Contains(sealed, []byte("p4ss")) || bytes.Contains(sealed, []byte("127.0.0.1")) {
			t.Fatal("a proxy is in the database in the clear")
		}
	}
	// A login's sealed proxy does not open as its secret or another's.
	if _, err := r.box.Open(rows[0].Proxy, bound("taboola", newID)); err == nil {
		t.Fatal("the proxy opened as the secret")
	}
	var all bytes.Buffer
	all.Write(r.logs.Bytes())
	_ = filepath.WalkDir(r.kept, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			all.Write(b)
		}
		return nil
	})
	for _, bad := range []string{"p4ss", "ana:p4ss", "uid-zz91"} {
		if strings.Contains(all.String(), bad) {
			t.Errorf("%q is in the logs or the keep folder", bad)
		}
	}
}

func TestParseProxy(t *testing.T) {
	for raw, want := range map[string]string{
		"http://u:p@host.example:8080":    "host.example:8080",
		" HTTPS://10.0.0.1:443/ ":         "10.0.0.1:443",
		"socks5://u:p@[2001:db8::1]:1080": "[2001:db8::1]:1080",
		"http://host:3128":                "host:3128",
	} {
		u, err := ParseProxy(raw)
		if err != nil || HostPort(u) != want {
			t.Errorf("%q: %v %v", raw, HostPort(u), err)
		}
	}
	for _, raw := range []string{"", "host:3128", "ftp://h:1", "http://h", "http://h:0", "http://h:99999", "http://:80", "http://h:80/path", "http://h:80?x=1", "socks4://h:1"} {
		_, err := ParseProxy(raw)
		if err == nil {
			t.Errorf("%q was accepted", raw)
			continue
		}
		if raw != "" && strings.Contains(err.Error(), raw) {
			t.Errorf("%q: the error repeats the address", raw)
		}
	}
}
