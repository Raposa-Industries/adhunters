package migrations_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/kit/migrate"
	"github.com/Raposa-Industries/adhunters/spy/internal/testdb"
	"github.com/Raposa-Industries/adhunters/spy/migrations"
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

// Every published view and function in contract/sql/spy is created, word
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
	files, err := filepath.Glob("../../contract/sql/spy/*.sql")
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
