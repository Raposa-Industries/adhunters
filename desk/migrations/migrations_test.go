package migrations_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/desk/internal/testdb"
	"github.com/Raposa-Industries/adhunters/desk/migrations"
	"github.com/Raposa-Industries/adhunters/kit/migrate"
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

// Every published view and function in contract/sql/desk is created, word
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
	files, err := filepath.Glob("../../contract/sql/desk/*.sql")
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

// History is only ever added to.
func TestHistoryOnlyAdded(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `INSERT INTO desk.conversation (person) VALUES ('ana@example.com')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO desk.message (conversation_id, author, kind, body) VALUES (1, 'ana@example.com', 'text', 'oi')`); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`UPDATE desk.message SET body = 'changed'`,
		`DELETE FROM desk.message`,
	} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Errorf("%s was allowed", sql)
		}
	}
}

// Another app's to-do lands on the holder's list, and an empty one is refused.
func TestAddTodo(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	var id int64
	if err := pool.QueryRow(ctx, `SELECT desk_api.add_todo_v1(p_title => 'Start the pair in Taboola', p_holder => ' Mari@Example.com ',
		p_made_by => 'launch', p_link => '/launch/requests/7')`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var holder, link string
	if err := pool.QueryRow(ctx, `SELECT holder, link FROM desk_api.todo_v1 WHERE id = $1`, id).Scan(&holder, &link); err != nil {
		t.Fatal(err)
	}
	if holder != "mari@example.com" || link != "/launch/requests/7" {
		t.Errorf("got %q %q", holder, link)
	}
	if _, err := pool.Exec(ctx, `SELECT desk_api.add_todo_v1('  ', 'mari@example.com', 'launch')`); err == nil {
		t.Error("an empty to-do was taken")
	}
}
