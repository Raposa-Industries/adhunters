package logins

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/keep"
	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	tbnet "github.com/Raposa-Industries/adhunters/launch/internal/network/taboola"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
	"github.com/Raposa-Industries/adhunters/launch/internal/testdb"
	"github.com/Raposa-Industries/adhunters/shared/taboola/write"
)

// backstage is a fake Taboola with two logins: the server's (srv-id,
// accounts zolta-1-sc) and a second one (new-id, accounts new-1-sc and
// new-2-sc under new-network). It records which login asked what, and
// whether it came through the fake proxy (fakeProxy).
type backstage struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []string // "<client id> <method> <path> <via>", via "proxy" or "direct"
}

func via(r *http.Request) string {
	if r.Header.Get("Via") == "fake-proxy" {
		return "proxy"
	}
	return "direct"
}

var secrets = map[string]string{"srv-id": "srv-secret", "new-id-0123456789": "new-secret"}

func newBackstage(t *testing.T) *backstage {
	b := &backstage{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/backstage/oauth/token" {
			_ = r.ParseForm()
			id := r.Form.Get("client_id")
			b.mu.Lock()
			b.got = append(b.got, id+" POST token "+via(r))
			b.mu.Unlock()
			if s, ok := secrets[id]; !ok || r.Form.Get("client_secret") != s {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"access_token":"tok-%s","token_type":"bearer","expires_in":3600}`, id)
			return
		}
		id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer tok-")
		path := strings.TrimPrefix(r.URL.Path, "/backstage/api/1.0/")
		b.mu.Lock()
		b.got = append(b.got, id+" "+r.Method+" "+path+" "+via(r))
		b.mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("%s asked %s %s: adding a login must only read", id, r.Method, path)
		}
		switch {
		case path == "users/current/allowed-accounts/" && id == "srv-id":
			io.WriteString(w, `{"results":[{"account_id":"zolta-1-sc","name":"Zolta 1","type":"PARTNER"}]}`)
		case path == "users/current/allowed-accounts/":
			io.WriteString(w, `{"results":[{"account_id":"new-network","name":"New","type":"NETWORK"},
				{"account_id":"new-1-sc","name":"New 1","type":"PARTNER"},{"account_id":"new-2-sc","name":"New 2","type":"PARTNER"}]}`)
		case strings.HasSuffix(path, "/campaigns/"):
			io.WriteString(w, `{"results":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *backstage) asked() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.got...)
}

type rig struct {
	px   *fakeProxy
	s    *Service
	net  *tbnet.Logins
	st   *store.Store
	box  *Box
	bs   *backstage
	kept string
	logs *bytes.Buffer
}

func setup(t *testing.T) *rig {
	t.Helper()
	db := testdb.New(t)
	bs := newBackstage(t)
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	dir := t.TempDir()
	kept := keep.New(dir)
	set := write.Settings{Base: bs.srv.URL, ClientID: "srv-id", ClientSecret: "srv-secret", Accounts: []string{"zolta-1-sc"}, MaxCPC: 1, MaxDailyCap: 20, MaxSpendLimit: 20}
	server, err := write.New(set, kept, log)
	if err != nil {
		t.Fatal(err)
	}
	box, err := OpenKey(filepath.Join(t.TempDir(), "login.key"))
	if err != nil {
		t.Fatal(err)
	}
	net := tbnet.NewLogins(tbnet.Login{T: tbnet.New(server), Accounts: set.Accounts})
	st := store.New(db)
	s := New(st, box, net, server, set, kept, log)
	return &rig{px: newFakeProxy(t, "ana", "p4ss"), s: s, net: net, st: st, box: box, bs: bs, kept: dir, logs: logs}
}

var ctx = context.Background()

func refused(t *testing.T, err error, want string) {
	t.Helper()
	var r *network.Refused
	if !errors.As(err, &r) || !strings.Contains(r.Message, want) {
		t.Fatalf("got %v; want a refusal saying %q", err, want)
	}
}

func TestAddedLoginsAccountsGoToTheirOwnLogin(t *testing.T) {
	r := setup(t)
	allowed, err := r.s.Check(ctx, "new-id-0123456789", "new-secret", r.px.addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 3 || !allowed[0].Network {
		t.Fatalf("allowed %+v", allowed)
	}
	if _, err := r.s.Add(ctx, "ana", " Nova  conta ", "new-id-0123456789", "4242", "new-secret", r.px.addr, []string{"new-network"}); err == nil {
		t.Fatal("a network account was accepted")
	}
	id, err := r.s.Add(ctx, "ana", " Nova  conta ", "new-id-0123456789", "4242", "new-secret", r.px.addr, []string{"new-2-sc"})
	if err != nil {
		t.Fatal(err)
	}

	accts, err := r.net.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(accts) != "[{zolta-1-sc Zolta 1} {new-2-sc New 2}]" {
		t.Fatalf("accounts %v", accts)
	}
	if _, err := r.net.Campaigns(ctx, "new-2-sc"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.net.Campaigns(ctx, "zolta-1-sc"); err != nil {
		t.Fatal(err)
	}
	// An account of the login that was not chosen stays out.
	if _, err := r.net.Campaigns(ctx, "new-1-sc"); err == nil {
		t.Fatal("an account not chosen was used")
	}
	got := strings.Join(r.bs.asked(), "\n")
	for _, want := range []string{"new-id-0123456789 GET new-2-sc/campaigns/", "srv-id GET zolta-1-sc/campaigns/"} {
		if !strings.Contains(got, want) {
			t.Errorf("Taboola was not asked %q:\n%s", want, got)
		}
	}

	list, err := r.s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || !list[0].Server || list[1].ID != id || list[1].Name != "Nova conta" || list[1].ClientID != "new-…6789" || list[1].AddedBy != "ana" || len(list[1].Accounts) != 1 {
		t.Fatalf("list %+v", list)
	}
	// The server's own accounts carry when Launch first used them, and keep it.
	first := list[0].Accounts[0].AddedAt
	if first == nil || time.Since(*first) > time.Hour {
		t.Fatalf("server account added_at %v", first)
	}
	if again, _ := r.s.List(ctx); again[0].Accounts[0].AddedAt == nil || !again[0].Accounts[0].AddedAt.Equal(*first) {
		t.Fatalf("added_at changed: %v then %v", first, again[0].Accounts[0].AddedAt)
	}

	// Choosing again, and a restart that loads the logins from the database.
	if err := r.s.Change(ctx, id, "Nova", []string{"new-1-sc", "new-2-sc"}); err != nil {
		t.Fatal(err)
	}
	r.net.Set(nil)
	if err := r.s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if accts, _ := r.net.Accounts(ctx); len(accts) != 3 {
		t.Fatalf("after load: %v", accts)
	}

	if err := r.s.Remove(ctx, id); err != nil {
		t.Fatal(err)
	}
	if accts, _ := r.net.Accounts(ctx); len(accts) != 1 {
		t.Fatalf("after remove: %v", accts)
	}
	if err := r.s.Remove(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second remove: %v", err)
	}
}

func TestSecretIsSealedAndNeverKeptOrLogged(t *testing.T) {
	r := setup(t)
	if _, err := r.s.Add(ctx, "", "Nova", "new-id-0123456789", "4242", "new-secret", r.px.addr, []string{"new-1-sc"}); err != nil {
		t.Fatal(err)
	}
	rows, err := r.st.Logins(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if bytes.Contains(rows[0].Secret, []byte("new-secret")) {
		t.Fatal("the database holds the secret in the clear")
	}
	// Moved to another login, the sealed secret does not open.
	if _, err := r.box.Open(rows[0].Secret, bound("taboola", "other-id")); !errors.Is(err, ErrSealed) {
		t.Fatalf("opened under another client id: %v", err)
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
	for _, bad := range []string{"new-secret", "srv-secret", "tok-", "p4ss"} {
		if strings.Contains(all.String(), bad) {
			t.Errorf("%q is in the logs or the keep folder", bad)
		}
	}
}

func TestRefusals(t *testing.T) {
	r := setup(t)
	_, err := r.s.Check(ctx, "new-id-0123456789", "wrong", r.px.addr)
	refused(t, err, "recusou esse client ID")
	_, err = r.s.Check(ctx, "", "x", r.px.addr)
	refused(t, err, "preencha")
	_, err = r.s.Add(ctx, "", "", "new-id-0123456789", "4242", "new-secret", r.px.addr, []string{"new-1-sc"})
	refused(t, err, "nome")
	_, err = r.s.Add(ctx, "", "x", "srv-id", "4242", "srv-secret", r.px.addr, []string{"zolta-1-sc"})
	refused(t, err, "login do servidor")
	_, err = r.s.Add(ctx, "", "x", "new-id-0123456789", "4242", "new-secret", r.px.addr, []string{"zolta-1-sc"})
	refused(t, err, "não está neste login")
	_, err = r.s.Add(ctx, "", "x", "new-id-0123456789", "4242", "new-secret", r.px.addr, nil)
	refused(t, err, "pelo menos uma")
	if _, err := r.s.Add(ctx, "", "x", "new-id-0123456789", "4242", "new-secret", r.px.addr, []string{"new-1-sc"}); err != nil {
		t.Fatal(err)
	}
	_, err = r.s.Add(ctx, "", "y", "new-id-0123456789", "4242", "new-secret", r.px.addr, []string{"new-1-sc"})
	refused(t, err, "já foi adicionado")
}

func TestKeyMadeOnceAndKept(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "login.key")
	a, err := OpenKey(p)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 || st.Size() != keySize {
		t.Fatalf("key file %v %v", st, err)
	}
	sealed, err := a.Seal([]byte("x"), []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b.Open(sealed, []byte("b")); err != nil || string(got) != "x" {
		t.Fatalf("reopened key: %q %v", got, err)
	}
	if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKey(p); err == nil {
		t.Fatal("a short key was accepted")
	}
}

func TestKeyFromSetting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "login.key")
	file, err := OpenKey(p)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := file.Seal([]byte("x"), []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	same := base64.StdEncoding.EncodeToString(raw)

	// The setting holding the file's key opens what the file's key sealed.
	b, differs, err := KeyFrom(" "+same+"\n", p)
	if err != nil || differs {
		t.Fatalf("same key: differs %v, %v", differs, err)
	}
	if got, err := b.Open(sealed, []byte("b")); err != nil || string(got) != "x" {
		t.Fatalf("opened with the setting: %q %v", got, err)
	}

	// Another key in the setting wins, says so, and leaves the file alone.
	other := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, keySize))
	b, differs, err = KeyFrom(other, p)
	if err != nil || !differs {
		t.Fatalf("other key: differs %v, %v", differs, err)
	}
	if _, err := b.Open(sealed, []byte("b")); err == nil {
		t.Fatal("another key opened the file key's secret")
	}
	if now, _ := os.ReadFile(p); !bytes.Equal(now, raw) {
		t.Fatal("the key file changed")
	}

	// With the setting, no key file is made.
	none := filepath.Join(dir, "none", "login.key")
	if _, differs, err := KeyFrom(same, none); err != nil || differs {
		t.Fatalf("no file: differs %v, %v", differs, err)
	}
	if _, err := os.Stat(none); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a key file was made: %v", err)
	}

	// Bad settings are refused without the setting in the error.
	for _, bad := range []string{"not base64 !!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		_, _, err := KeyFrom(bad, p)
		if err == nil {
			t.Fatalf("%q was accepted", bad)
		}
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("the error holds the setting: %v", err)
		}
	}
}
