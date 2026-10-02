package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/intel/collect"
	"github.com/Raposa-Industries/adhunters/intel/internal/testdb"
	"github.com/Raposa-Industries/adhunters/intel/taboola"
	"github.com/Raposa-Industries/adhunters/shared/taboola/logins"
)

// A login added on Launch's Contas page is read with Launch's key, through
// its own proxy, and only for its chosen accounts; one without a proxy, or
// sealed with another key, is not read at all.
func TestContasLoginsAreReadThroughTheirProxy(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	// Stands in for Launch's view (contract/sql/launch/taboola_login_v1.sql).
	if _, err := db.Exec(ctx, `CREATE SCHEMA launch_api;
		CREATE TABLE launch_api.taboola_login_v1 (id bigint, name text, client_id text, secret bytea,
			proxy bytea, accounts text[], changed_at timestamptz)`); err != nil {
		t.Fatal(err)
	}

	// The proxy answers as Taboola would, and notes what it carried.
	var mu sync.Mutex
	var carried []string
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		carried = append(carried, r.Method+" "+r.URL.String())
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth/token"):
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
		case strings.HasSuffix(r.URL.Path, "/allowed-accounts"):
			_, _ = w.Write([]byte(`{"results":[{"account_id":"net","type":"NETWORK","time_zone_name":"UTC"},
				{"account_id":"kept-sc","type":"PARTNER","time_zone_name":"UTC"},
				{"account_id":"other-sc","type":"PARTNER","time_zone_name":"UTC"}]}`))
		default:
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer px.Close()

	key := make([]byte, 32)
	box, err := logins.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	seal := func(b *logins.Box, v string, bound []byte) []byte {
		out, err := b.Seal([]byte(v), bound)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	otherKey := make([]byte, 32)
	otherKey[0] = 1
	other, _ := logins.NewBox(otherKey)
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	rows := []struct {
		id            int64
		client        string
		secret, proxy []byte
		accounts      string
	}{
		{1, "c1", seal(box, "s1", logins.LoginBound("taboola", "c1")), seal(box, px.URL, logins.ProxyBound("taboola", "c1")), "{kept-sc}"},
		{2, "c2", seal(box, "s2", logins.LoginBound("taboola", "c2")), nil, "{x-sc}"},
		{3, "c3", seal(other, "s3", logins.LoginBound("taboola", "c3")), seal(other, px.URL, logins.ProxyBound("taboola", "c3")), "{y-sc}"},
	}
	for _, r := range rows {
		if _, err := db.Exec(ctx, `INSERT INTO launch_api.taboola_login_v1 VALUES ($1, 'team', $2, $3, $4, $5::text[], $6)`,
			r.id, r.client, r.secret, r.proxy, r.accounts, at); err != nil {
			t.Fatal(err)
		}
	}

	ls, err := loadContas(ctx, db)
	if err != nil || len(ls) != 3 {
		t.Fatalf("loaded %d: %v", len(ls), err)
	}
	fp := fingerprint(launchRows{Logins: ls})
	if _, err := db.Exec(ctx, `UPDATE launch_api.taboola_login_v1 SET changed_at = now() WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if again, _ := loadContas(ctx, db); fingerprint(launchRows{Logins: again}) == fp {
		t.Error("a changed login kept the same fingerprint")
	}

	s := &setup{spool: collect.Spool{Dir: t.TempDir()}}
	// The base is never reached direct: every request goes to the proxy.
	got := contasCollectors(box, ls, "http://taboola.invalid", s, 40, 8, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(got) != 1 || got[0].Login != "contas-1" {
		t.Fatalf("collectors %v, want only contas-1", got)
	}
	accs, err := got[0].Known(ctx)
	if err != nil || len(accs) != 1 || accs[0].ID != "kept-sc" {
		t.Fatalf("accounts %+v %v, want only kept-sc", accs, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(carried) != 2 || !strings.HasPrefix(carried[0], "POST http://taboola.invalid/backstage/oauth/token") ||
		!strings.Contains(carried[1], "taboola.invalid/backstage/api/1.0/users/current/allowed-accounts") {
		t.Errorf("the proxy carried %q", carried)
	}
}

// fakeTaboola answers as Taboola would and notes what it was asked.
func fakeTaboola(t *testing.T, accounts string) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.String())
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth/token"):
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
		case strings.HasSuffix(r.URL.Path, "/allowed-accounts"):
			_, _ = w.Write([]byte(accounts))
		default:
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

// An account of the server's own login that has a proxy on Contas is read
// only through it, by a client of its own; the env's login leaves it out,
// and one whose proxy does not open is not read at all. What Launch
// published is kept for a start with the database away.
func TestProxiedAccountsNeverGoDirect(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	// Stands in for Launch's view (contract/sql/launch/taboola_account_proxy_v1.sql).
	if _, err := db.Exec(ctx, `CREATE SCHEMA launch_api;
		CREATE TABLE launch_api.taboola_account_proxy_v1 (account text, proxy bytea, set_at timestamptz)`); err != nil {
		t.Fatal(err)
	}
	const all = `{"results":[{"account_id":"plain-sc","type":"PARTNER","time_zone_name":"UTC"},
		{"account_id":"prox-sc","type":"PARTNER","time_zone_name":"UTC"},
		{"account_id":"bad-sc","type":"PARTNER","time_zone_name":"UTC"}]}`
	direct, _ := fakeTaboola(t, all)
	px, carried := fakeTaboola(t, all)

	box, _ := logins.NewBox(make([]byte, 32))
	otherKey := make([]byte, 32)
	otherKey[0] = 1
	other, _ := logins.NewBox(otherKey)
	good, _ := box.Seal([]byte(px.URL), logins.AccountBound("taboola", "prox-sc"))
	bad, _ := other.Seal([]byte(px.URL), logins.AccountBound("taboola", "bad-sc"))
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for acc, p := range map[string][]byte{"prox-sc": good, "bad-sc": bad} {
		if _, err := db.Exec(ctx, `INSERT INTO launch_api.taboola_account_proxy_v1 VALUES ($1, $2, $3)`, acc, p, at); err != nil {
			t.Fatal(err)
		}
	}

	r, err := readLaunch(ctx, db, false)
	if err != nil || len(r.Proxies) != 2 {
		t.Fatalf("read %+v %v", r, err)
	}
	kept := t.TempDir() + "/launch.json"
	if err := keep(kept, r); err != nil {
		t.Fatal(err)
	}
	if k, err := readKept(kept); err != nil || fingerprint(k) != fingerprint(r) {
		t.Fatalf("kept copy differs: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &setup{spool: collect.Spool{Dir: t.TempDir()}, box: box, perMin: 40, rtPerMin: 8,
		envLogins: []envLogin{{"zolta", "id", "secret"}}}
	env := &collect.Taboola{Login: "zolta", API: taboola.New(direct.URL, "id", "secret"), Spool: s.spool,
		Pace: &collect.Pacer{PerMinute: 40, RealtimePerMinute: 8}, Log: log, Now: time.Now, Skip: s.hasProxy}
	s.taboolas = []*collect.Taboola{env}
	s.own = []*collect.Taboola{env}
	s.useLaunch(r, "http://taboola.invalid", log)

	if len(s.taboolas) != 2 || s.taboolas[1].Login != "zolta-prox-sc" {
		t.Fatalf("collectors %v, want zolta and zolta-prox-sc", s.taboolas)
	}
	if err := env.Accounts(ctx); err != nil {
		t.Fatal(err)
	}
	if accs, _ := env.Known(ctx); len(accs) != 1 || accs[0].ID != "plain-sc" {
		t.Errorf("the env login reads %+v, want only plain-sc", accs)
	}
	own := s.taboolas[1]
	if err := own.Accounts(ctx); err != nil {
		t.Fatal(err)
	}
	if accs, _ := own.Known(ctx); len(accs) != 1 || accs[0].ID != "prox-sc" {
		t.Errorf("the proxied client reads %+v, want only prox-sc", accs)
	}
	got := carried()
	if len(got) != 2 || !strings.HasPrefix(got[0], "POST http://taboola.invalid/backstage/oauth/token") {
		t.Errorf("the proxy carried %q", got)
	}
}

// A start waits for a view launch-web has not migrated yet, rather than
// going on without it and restarting once it appears (which a deploy takes
// for a crash loop).
func TestStartWaitsForLaunchsViews(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	defer func(w time.Duration) { launchWait = w }(launchWait)
	launchWait = 30 * time.Second
	go func() {
		time.Sleep(2 * time.Second)
		_, _ = db.Exec(ctx, `CREATE SCHEMA launch_api;
			CREATE TABLE launch_api.taboola_account_proxy_v1 (account text, proxy bytea, set_at timestamptz)`)
	}()
	s := &setup{kept: t.TempDir() + "/launch.json"}
	start := time.Now()
	if _, err := s.readLaunchWaiting(ctx, &lazyDB{pool: db}, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("gave up after %v: %v", time.Since(start), err)
	}
	if _, err := readKept(s.kept); err != nil {
		t.Errorf("nothing kept: %v", err)
	}
}
