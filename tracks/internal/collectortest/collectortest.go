// Package collectortest gives a test a throwaway copy of the collector's
// spy tables (the ones tracks-bridge writes and import-old reads), from
// collector_spy.sql. It needs PG_TEST_URL, like testdb; without it the test
// is skipped.
package collectortest

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/kit/pg"
)

//go:embed collector_spy.sql
var schema string

// New creates a database with the collector's spy tables and drops it when
// the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("PG_TEST_URL")
	if base == "" {
		t.Skip("PG_TEST_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "collector_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "collector-test", StatementTimeout: time.Minute, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_trgm"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA spy"); err != nil {
		t.Fatal(err)
	}
	// pg_dump output: many statements, so the simple protocol.
	if _, err := conn.Conn().PgConn().Exec(ctx, schema).ReadAll(); err != nil {
		t.Fatal(err)
	}
	return pool
}
