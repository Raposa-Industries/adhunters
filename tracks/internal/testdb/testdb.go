// Package testdb gives a test its own throwaway database with the tracks
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

	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/kit/pg"
	"github.com/Raposa-Industries/adhunters/tracks/migrations"
)

// New creates a database, migrates it, and drops it when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	return NewUpTo(t, 0)
}

// NewUpTo is New with the migrations up to version only (0: all of them),
// for a test of a migration over rows saved before it; Migrate applies the
// rest.
func NewUpTo(t testing.TB, version int) *pgxpool.Pool {
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
	name := "tracks_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	// Like load.UTC: tracks sessions always run in UTC.
	q := u.Query()
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	pool, err := pg.Open(ctx, pg.Config{URL: u.String(), AppName: "tracks-test", StatementTimeout: time.Minute, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	migrateUpTo(t, pool, version)
	return pool
}

// Migrate applies the migrations not applied yet.
func Migrate(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	migrateUpTo(t, pool, 0)
}

func migrateUpTo(t testing.TB, pool *pgxpool.Pool, version int) {
	t.Helper()
	migs, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	if version > 0 {
		var upTo []migrate.Migration
		for _, m := range migs {
			if m.Version <= version {
				upTo = append(upTo, m)
			}
		}
		migs = upTo
	}
	if _, err := migrate.Up(context.Background(), pool, slog.New(slog.NewTextHandler(io.Discard, nil)), migrations.Schema, migs); err != nil {
		t.Fatal(err)
	}
}
