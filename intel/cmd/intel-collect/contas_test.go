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
	fp := fingerprint(ls)
	if _, err := db.Exec(ctx, `UPDATE launch_api.taboola_login_v1 SET changed_at = now() WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if again, _ := loadContas(ctx, db); fingerprint(again) == fp {
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
