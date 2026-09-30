package migrations_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/launch/internal/testdb"
	"github.com/Raposa-Industries/adhunters/launch/migrations"
)

func TestLint(t *testing.T) {
	migs, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) == 0 {
		t.Fatal("no migrations")
	}
	for _, f := range migrate.Lint(migs) {
		t.Error(f)
	}
}

// Applying the migrations twice changes nothing the second time.
func TestUpTwice(t *testing.T) {
	db := testdb.New(t)
	migs, _ := migrations.Load()
	n, err := migrate.Up(context.Background(), db, slog.New(slog.NewTextHandler(io.Discard, nil)), migrations.Schema, migs)
	if err != nil || n != 0 {
		t.Fatalf("second run applied %d: %v", n, err)
	}
}
