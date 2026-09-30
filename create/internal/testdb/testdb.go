// Package testdb gives a test its own throwaway database with Create's
// migrations applied. It needs PG_TEST_URL (any database on a server the test
// may create databases on); without it the test is skipped.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/create/migrations"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/pg"
)

// New creates a database, migrates it, and drops it when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := Empty(t)
	ctx := context.Background()
	migs, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil)), migrations.Schema, migs); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Empty creates a database with nothing in it, and drops it when the test
// ends.
func Empty(t testing.TB) *pgxpool.Pool {
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
	name := "create_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	q := u.Query()
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	pool, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "create-test", StatementTimeout: time.Minute, MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	return pool
}
