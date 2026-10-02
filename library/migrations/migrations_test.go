package migrations_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/library/internal/testdb"
	"github.com/Raposa-Industries/adhunters/library/migrations"
)

func TestLint(t *testing.T) {
	migs, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range migrate.Lint(migs) {
		t.Error(f)
	}
}

// Every published view in contract/sql/library is created, word for word, by
// a migration. One that changes needs a new version and a new file.
func TestContractMatchesMigrations(t *testing.T) {
	migs, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, m := range migs {
		all.WriteString(m.SQL)
	}
	files, err := filepath.Glob("../../contract/sql/library/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no contract files: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(all.String(), string(b)) {
			t.Errorf("%s is not in any migration word for word", filepath.Base(f))
		}
	}
}

func TestApply(t *testing.T) {
	testdb.New(t) // fails the test if a migration does not apply
}

// Tinnitus's code becomes TIN only where it is still TN (migration 0005).
func TestTinnitusCode(t *testing.T) {
	migs, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	code := func(db *pgxpool.Pool) string {
		var c string
		if err := db.QueryRow(ctx, `SELECT code FROM library.vertical WHERE id = 'tinnitus'`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := code(testdb.New(t)); c != "TIN" {
		t.Fatalf("fresh database: tinnitus code %q, want TIN", c)
	}

	// Changed by hand before 0005: it stays.
	db := testdb.Empty(t)
	var before []migrate.Migration
	for _, m := range migs {
		if m.Version < 5 {
			before = append(before, m)
		}
	}
	if _, err := migrate.Up(ctx, db, log, migrations.Schema, before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE library.vertical SET code = 'TNS' WHERE id = 'tinnitus'`); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(ctx, db, log, migrations.Schema, migs); err != nil {
		t.Fatal(err)
	}
	if c := code(db); c != "TNS" {
		t.Fatalf("a hand-set code was changed to %q", c)
	}
}
