package importold

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/Raposa-Industries/adhunters/spy/internal/testdb"
)

// oldSchema is the part of the collector's spy schema the import reads.
const oldSchema = `
CREATE SCHEMA spy;
CREATE TABLE spy.network (id SMALLINT PRIMARY KEY, code TEXT NOT NULL);
INSERT INTO spy.network VALUES (7, 'taboola'), (8, 'newsbreak');
CREATE TABLE spy.operator (id INTEGER PRIMARY KEY, name TEXT NOT NULL, display_name TEXT, kind TEXT NOT NULL, vertical TEXT);
CREATE TABLE spy.account (id SERIAL PRIMARY KEY, network_id SMALLINT NOT NULL, external_id TEXT NOT NULL, operator_id INTEGER);
CREATE TABLE spy.creative (id INTEGER PRIMARY KEY, creative_key TEXT NOT NULL, network_id SMALLINT NOT NULL, vertical TEXT);
CREATE TABLE spy.subvertical (id SMALLINT PRIMARY KEY, vertical TEXT NOT NULL, name TEXT NOT NULL, old_vertical TEXT);
CREATE TABLE spy.creative_vertical (creative_id INTEGER PRIMARY KEY, vertical TEXT, subvertical_id SMALLINT,
    confidence NUMERIC(3, 2) NOT NULL DEFAULT 0, unsure BOOLEAN NOT NULL DEFAULT TRUE, source TEXT,
    health_from_funnel BOOLEAN NOT NULL DEFAULT FALSE);
INSERT INTO spy.operator VALUES (12, 'Acme', 'Acme Health', 'direct', 'Health'), (40, 'Beta', NULL, 'arbitrage', NULL);
INSERT INTO spy.account (network_id, external_id, operator_id) VALUES
    (7, 'tb-1', 12), (7, 'tb-2', 40), (8, 'tb-1', 40), (7, 'tb-unknown', 12), (7, 'tb-3', NULL);
INSERT INTO spy.subvertical VALUES (1, 'health', 'Weight Management & Metabolic Health', 'Weight Loss'),
    (2, 'health', 'Vision & Eye Health', NULL), (3, 'finance', 'Insurance', 'Finance');
INSERT INTO spy.creative VALUES (101, 'key-a', 7, 'Health'), (102, 'key-b', 7, 'Pets'), (103, 'key-c', 7, NULL),
    (104, 'key-d', 7, NULL), (105, 'key-unknown', 7, NULL);
INSERT INTO spy.creative_vertical VALUES
    (101, 'health', 1, 0.9, false, 'headline', false),
    (102, 'health', 2, 0.7, false, 'headline', false),
    (103, 'health', 2, 0.5, true, 'model', true),
    (104, 'finance', 3, 0.8, false, 'landing_page', false),
    (105, 'finance', 3, 0.8, false, 'landing_page', false);
`

func TestRun(t *testing.T) {
	ctx := context.Background()
	old := testdb.Empty(t)
	if _, err := old.Exec(ctx, oldSchema); err != nil {
		t.Fatal(err)
	}
	db := testdb.New(t)
	if _, err := db.Exec(ctx, `
		INSERT INTO tracks_api.account_v1 (id, network_id, external_id) VALUES (1, 1, 'tb-1'), (2, 1, 'tb-2'), (3, 2, 'tb-1');
		INSERT INTO tracks_api.creative_v1 (id, creative_key) VALUES (11, 'key-a'), (12, 'key-b'), (13, 'key-c'), (14, 'key-d');
		INSERT INTO spy.operator (id, name) VALUES (99, 'gone');`); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for run := 0; run < 2; run++ { // the second run must give the same result
		res, err := Run(ctx, old, db, log)
		if err != nil {
			t.Fatal(err)
		}
		want := Result{Operators: Counts{2, 0}, Accounts: Counts{3, 1}, Verticals: Counts{4, 1}}
		if res != want {
			t.Fatalf("run %d: got %+v, want %+v", run, res, want)
		}
	}
	var got string
	if err := db.QueryRow(ctx, `
		SELECT string_agg(format('%s=%s', account_id, operator_id), ' ' ORDER BY account_id) FROM spy.account_operator`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "1=12 2=40 3=40" {
		t.Errorf("accounts: %s", got)
	}
	if err := db.QueryRow(ctx, `
		SELECT string_agg(format('%s:%s/%s/%s', creative_id, vertical, subvertical, shown_vertical), ' | ' ORDER BY creative_id)
		FROM spy.creative_vertical_old`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want := "11:health/Weight Management & Metabolic Health/Weight Loss | 12:health/Vision & Eye Health/Pets | " +
		"13:health/Vision & Eye Health/Vision & Eye Health | 14:finance/Insurance/Insurance"
	if got != want {
		t.Errorf("verticals:\n got %s\nwant %s", got, want)
	}
	if err := db.QueryRow(ctx, `SELECT string_agg(code || ' ' || name, ', ' ORDER BY id) FROM spy.operator`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "OP12 Acme, OP40 Beta" {
		t.Errorf("operators: %s", got)
	}
	// A new operator gets an id past the copied ones.
	var id int
	if err := db.QueryRow(ctx, `INSERT INTO spy.operator (name) VALUES ('new') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 41 {
		t.Errorf("new operator id %d, want 41", id)
	}

	// An empty old grouping is refused and changes nothing.
	if _, err := old.Exec(ctx, `DELETE FROM spy.creative_vertical`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, old, db, log); err == nil {
		t.Fatal("an empty grouping was copied")
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM spy.creative_vertical_old`).Scan(&n); err != nil || n != 4 {
		t.Errorf("verticals after a refused run: %d (%v)", n, err)
	}
}
