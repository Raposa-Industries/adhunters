package walk

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/tracks/internal/testdb"
)

// Walk pages saved before tracks migration 12 read the same through
// tracks_api.walk_page_v1 after it, each URL kept once; what an old walker
// still writes to walk_page reaches walk_step; walk_page goes only when every
// row matches its copy.
func TestWalkURLsOnce(t *testing.T) {
	db := testdb.NewUpTo(t, 11)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	count := func(sql string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
		return n
	}
	view := func() string {
		t.Helper()
		var s string
		if err := db.QueryRow(ctx, `SELECT COALESCE(string_agg(row_to_json(v)::text, E'\n' ORDER BY walk_id, step), '')
			FROM tracks_api.walk_page_v1 v`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	long := "https://lander.example/a?" + strings.Repeat("utm_content=x%20y&", 300) // longer than a btree entry may be
	exec(`INSERT INTO tracks.page_version (hash, word_count, first_seen_at, last_seen_at)
		VALUES ('00000000-0000-0000-0000-000000000001', 10, now(), now())`)
	exec(`INSERT INTO tracks.walk (id, record_id, at, ad_id, creative_id, account_id, link_id, outcome, ms)
		SELECT i, 'r' || i, '2026-10-01T12:00:00Z'::timestamptz + i * interval '1 minute', 100 + i, 10, 7, 1, 'ok', 900
		FROM generate_series(1, 6) i`)
	exec(`INSERT INTO tracks.walk_page (walk_id, step, url, final_url, host, status, hops, version_hash, page_type,
		                                checkout_platform, seller_account) VALUES
		(1, 0, 'https://go.example/c?id=1', 'https://lander.example/p', 'lander.example', 200, 2,
		 '00000000-0000-0000-0000-000000000001', 'ADVERTORIAL', NULL, NULL),
		(1, 1, 'https://pay.example/buy', NULL, NULL, NULL, 0, NULL, NULL, NULL, NULL),
		(2, 0, 'https://go.example/c?id=1', 'https://lander.example/p', 'lander.example', 200, 2, NULL, NULL, NULL, NULL),
		(3, 0, $1, $1, 'lander.example', 200, 0, NULL, 'VSL', 'clickbank', 'slimpro'),
		(4, 0, 'https://go.example/ç?q=''a b''', 'https://pay.example/buy', 'pay.example', 404, 1, NULL, 'CHECKOUT', NULL, NULL)`, long)
	before := view()

	testdb.Migrate(t, db)
	if after := view(); after != before {
		t.Fatalf("the view changed:\nbefore\n%s\nafter\n%s", before, after)
	}
	if n := count(`SELECT count(*) FROM tracks.page_url`); n != 5 {
		t.Errorf("%d URLs kept, want 5 (each once)", n)
	}
	if n := count(`SELECT count(*) FROM tracks.walk_step`); n != 5 {
		t.Errorf("%d walk steps, want 5", n)
	}

	// A walker on the old code: its writes to walk_page reach walk_step.
	exec(`INSERT INTO tracks.walk_page (walk_id, step, url, final_url, host, status, hops)
		VALUES (5, 0, 'https://go.example/c?id=1', 'https://new.example/', 'new.example', 200, 1)`)
	exec(`UPDATE tracks.walk_page SET status = 503 WHERE walk_id = 5`)
	exec(`DELETE FROM tracks.walk_page WHERE walk_id = 2`)
	exec(`DELETE FROM tracks.walk_page WHERE walk_id = 1 AND step = 1`)
	exec(`INSERT INTO tracks.walk_page (walk_id, step, url, hops) VALUES (1, 1, 'https://pay.example/buy2', 0)`)
	var old string
	if err := db.QueryRow(ctx, `SELECT COALESCE(string_agg(concat_ws(' ', walk_id, step, url, final_url, status), E'\n' ORDER BY walk_id, step), '')
		FROM tracks.walk_page`).Scan(&old); err != nil {
		t.Fatal(err)
	}
	var now string
	if err := db.QueryRow(ctx, `SELECT COALESCE(string_agg(concat_ws(' ', walk_id, step, url, final_url, status), E'\n' ORDER BY walk_id, step), '')
		FROM tracks_api.walk_page_v1`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	if now != old {
		t.Errorf("the view does not follow walk_page:\nwalk_page\n%s\nview\n%s", old, now)
	}

	o, err := CheckOldPages(ctx, db)
	if err != nil || !o.OK() || o.Rows != 5 || o.Copied != 5 || o.Steps != 5 || o.URLs != 7 {
		t.Fatalf("check: %+v %v", o, err)
	}

	// A copy that differs, or one missing, holds walk_page back.
	exec(`UPDATE tracks.walk_step SET host = 'other.example' WHERE walk_id = 3`)
	if o, err := DropOldPages(ctx, db); err == nil || o.Differ != 1 {
		t.Fatalf("dropped with a different copy: %+v %v", o, err)
	}
	exec(`UPDATE tracks.walk_page SET hops = hops WHERE walk_id = 3`) // the trigger copies it again
	exec(`DELETE FROM tracks.walk_step WHERE walk_id = 4`)
	if o, err := DropOldPages(ctx, db); err == nil || o.Rows-o.Copied != 1 {
		t.Fatalf("dropped with a missing copy: %+v %v", o, err)
	}
	exec(`UPDATE tracks.walk_page SET hops = hops WHERE walk_id = 4`)

	// A walker on the new code writes walk_step only.
	rec := &Record{ID: "r6", At: time.Date(2026, 10, 1, 12, 6, 0, 0, time.UTC), AdID: 106, CreativeID: 10, LinkID: 1,
		Pages: []Page{{Step: 0, URL: "https://go.example/c?id=1", FinalURL: "https://new.example/", Status: 200}}}
	if err := Save(ctx, db, rec, []Parsed{{Step: 0, URL: "https://go.example/c?id=1", FinalURL: "https://new.example/",
		Host: "new.example", Status: 200}}); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM tracks.page_url`); n != 7 {
		t.Errorf("%d URLs after a walk over known ones, want 7", n)
	}

	o, err = DropOldPages(ctx, db)
	if err != nil || o.Rows != 5 {
		t.Fatalf("drop: %+v %v", o, err)
	}
	if n := count(`SELECT count(*) FROM pg_proc WHERE proname = 'walk_page_to_step'`); n != 0 {
		t.Error("the copying trigger's function is still there")
	}
	if n := count(`SELECT count(*) FROM tracks_api.walk_page_v1`); n != 6 {
		t.Errorf("%d pages in the view after the drop, want 6", n)
	}
	if o, err := CheckOldPages(ctx, db); err != nil || !o.Gone || o.OK() {
		t.Errorf("check after the drop: %+v %v", o, err)
	}
	if _, err := DropOldPages(ctx, db); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("a second drop: %v", err)
	}
	if err := Save(ctx, db, rec, nil); err != nil {
		t.Errorf("a walk saved after the drop: %v", err)
	}
	if err := Ready(ctx, db); err != nil {
		t.Error(err)
	}
}
