package migrate

import (
	"strings"
	"testing"
	"testing/fstest"
)

func load(t *testing.T, files map[string]string) []Migration {
	t.Helper()
	fsys := fstest.MapFS{}
	for n, s := range files {
		fsys["m/"+n] = &fstest.MapFile{Data: []byte(s)}
	}
	migs, err := Load(fsys, "m")
	if err != nil {
		t.Fatal(err)
	}
	return migs
}

func TestLoadOrdersAndMarksNoTx(t *testing.T) {
	migs := load(t, map[string]string{
		"0002_idx.sql":   "-- migrate: no-transaction\nCREATE INDEX CONCURRENTLY a_b ON a (b);",
		"0001_table.sql": "CREATE TABLE a (b int);",
	})
	if migs[0].Version != 1 || migs[1].Version != 2 || migs[0].NoTx || !migs[1].NoTx {
		t.Fatalf("%+v", migs)
	}
}

func TestLoadRejectsBadNames(t *testing.T) {
	_, err := Load(fstest.MapFS{"m/1_x.sql": {Data: []byte("")}}, "m")
	if err == nil {
		t.Fatal("want error")
	}
}

func TestLint(t *testing.T) {
	cases := []struct {
		sql  string
		want string // substring of the only finding, or "" for none
	}{
		{"CREATE TABLE a (b int);", ""},
		{"CREATE INDEX a_b ON a (b);", "without CONCURRENTLY"},
		{"-- lint: new-table\nCREATE TABLE a (b int);\nCREATE INDEX a_b ON a (b);", ""},
		{"-- migrate: no-transaction\nCREATE UNIQUE INDEX CONCURRENTLY a_b ON a (b);", ""},
		{"CREATE INDEX CONCURRENTLY a_b ON a (b);", "cannot run in a transaction"},
		{"ALTER TABLE a DROP COLUMN b;", "decision"},
		{"-- decision: decisions/0007-drop-b.md\nALTER TABLE a DROP COLUMN b;", ""},
		{"ALTER TABLE a RENAME COLUMN b TO c;", "decision"},
		{"ALTER TABLE a ALTER COLUMN b TYPE bigint;", "decision"},
		{"-- DROP TABLE a; is only a comment here\nSELECT 1;", ""},
	}
	for _, c := range cases {
		f := Lint(load(t, map[string]string{"0001_x.sql": c.sql}))
		switch {
		case c.want == "" && len(f) != 0:
			t.Errorf("%q: unexpected %v", c.sql, f)
		case c.want != "" && (len(f) != 1 || !strings.Contains(f[0].Problem, c.want)):
			t.Errorf("%q: got %v, want one finding about %q", c.sql, f, c.want)
		}
	}
}
