package migrations_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

// Every published view and function in contract/sql/launch is created, word
// for word, by a migration. One that changes needs a new version and a new
// file.
func TestContractMatchesMigrations(t *testing.T) {
	migs, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, m := range migs {
		all.WriteString(m.SQL)
	}
	files, err := filepath.Glob("../../contract/sql/launch/*.sql")
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

// Asking twice with the same origin is one request; a bad ask is refused.
func TestNewRequest(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	in := `{"network":"taboola","account":"acme-sc","campaigns":["12"]}`
	var a, b int64
	if err := db.QueryRow(ctx, `SELECT launch_api.new_request_v1('pause', $1, 'ana@team.test', 'desk:1')`, in).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT launch_api.new_request_v1('pause', $1, 'ana@team.test', 'desk:1')`, in).Scan(&b); err != nil || a != b {
		t.Fatalf("asked twice: %d %d %v", a, b, err)
	}
	var state string
	if err := db.QueryRow(ctx, `SELECT state FROM launch_api.request_v1 WHERE id = $1`, a).Scan(&state); err != nil || state != "waiting" {
		t.Fatalf("%q %v", state, err)
	}
	for _, c := range [][2]string{
		{"start", in},
		{"pause", `{"network":"taboola","account":"acme-sc","campaigns":[]}`},
		{"pause", `[]`},
	} {
		if _, err := db.Exec(ctx, `SELECT launch_api.new_request_v1($1, $2, '', 'desk:2')`, c[0], c[1]); err == nil {
			t.Errorf("%s %s accepted", c[0], c[1])
		}
	}
	if _, err := db.Exec(ctx, `SELECT launch_api.new_request_v1('pause', $1, '', '')`, in); err == nil {
		t.Error("no origin accepted")
	}
}
