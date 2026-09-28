package migrate

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marcosCapistrano/adhunters/kit/pg"
)

// TestUp needs a throwaway database: PG_TEST_URL=postgres://… go test ./...
func TestUp(t *testing.T) {
	url := os.Getenv("PG_TEST_URL")
	if url == "" {
		t.Skip("PG_TEST_URL not set")
	}
	ctx := context.Background()
	pool, err := pg.Open(ctx, pg.Config{URL: url, AppName: "migrate-test", StatementTimeout: time.Minute, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS mtest CASCADE`); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	migs := load(t, map[string]string{
		"0001_thing.sql": "CREATE TABLE mtest.thing (id int PRIMARY KEY, b int);",
		"0002_idx.sql":   "-- migrate: no-transaction\nCREATE INDEX CONCURRENTLY thing_b ON mtest.thing (b);",
	})
	if n, err := Up(ctx, pool, log, "mtest", migs); err != nil || n != 2 {
		t.Fatalf("first run: n=%d err=%v", n, err)
	}
	if n, err := Up(ctx, pool, log, "mtest", migs); err != nil || n != 0 {
		t.Fatalf("second run: n=%d err=%v", n, err)
	}

	changed := load(t, map[string]string{"0001_thing.sql": "CREATE TABLE mtest.thing (id bigint PRIMARY KEY);"})
	if _, err := Up(ctx, pool, log, "mtest", changed); err == nil || !strings.Contains(err.Error(), "changed after it was applied") {
		t.Fatalf("want checksum error, got %v", err)
	}

	bad := append(migs, load(t, map[string]string{"0003_bad.sql": "CREATE TABLE mtest.x (a int); SELECT nope;"})...)
	if _, err := Up(ctx, pool, log, "mtest", bad); err == nil {
		t.Fatal("want error from bad migration")
	}
	var exists bool
	pool.QueryRow(ctx, `SELECT to_regclass('mtest.x') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Fatal("failed migration left a table behind; it must roll back")
	}
	pool.Exec(ctx, `DROP SCHEMA mtest CASCADE`)
}
