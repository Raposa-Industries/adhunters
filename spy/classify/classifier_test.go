package classify

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/spy/internal/testdb"
)

// seed adds a creative with one ad per headline.
func seed(t *testing.T, db *pgxpool.Pool, id int, brand string, headlines ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO tracks_api.creative_v1 (id, creative_key) VALUES ($1, $2)`, id, fmt.Sprint("c", id)); err != nil {
		t.Fatal(err)
	}
	var brandID *int
	if brand != "" {
		b := id
		brandID = &b
		if _, err := db.Exec(ctx, `INSERT INTO tracks_api.brand_v1 (id, name) VALUES ($1, $2)`, id, brand); err != nil {
			t.Fatal(err)
		}
	}
	for i, h := range headlines {
		if _, err := db.Exec(ctx, `INSERT INTO tracks_api.ad_v1 (id, creative_id, headline, brand_id) VALUES ($1, $2, $3, $4)`,
			id*10+i, id, h, brandID); err != nil {
			t.Fatal(err)
		}
	}
}

type class struct {
	vertical, category, source string
	confidence                 float64
	unsure                     bool
}

func read(t *testing.T, db *pgxpool.Pool, id int) class {
	t.Helper()
	var c class
	if err := db.QueryRow(context.Background(), `
		SELECT COALESCE(vertical_id, ''), COALESCE(category_id, ''), COALESCE(source, ''), confidence::float8, unsure
		FROM spy.creative_class WHERE creative_id = $1`, id).
		Scan(&c.vertical, &c.category, &c.source, &c.confidence, &c.unsure); err != nil {
		t.Fatalf("creative %d: %v", id, err)
	}
	return c
}

func TestClassifier(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Creatives the rules are sure about. Each vertical has its own other
	// words (cardiologist, audiologist, belly), which the model can learn.
	id := 1
	for i := 0; i < 40; i++ {
		seed(t, db, id, "", fmt.Sprintf("Cardiologist warns: high blood pressure trick number %d", i))
		id++
		seed(t, db, id, "", fmt.Sprintf("Audiologist explains: tinnitus relief secret %d", i))
		id++
		seed(t, db, id, "", fmt.Sprintf("Melt stubborn belly fat, weight loss hack %d", i))
		id++
	}
	// Unsure: no keyword, only the other words.
	seed(t, db, 1001, "", "Cardiologist shares this morning trick")
	seed(t, db, 1002, "", "Audiologist shares this nightly routine")
	// Nothing to go on at all.
	seed(t, db, 1003, "", "You won't believe what happened next")
	// Junk-free, sure, from its brand.
	seed(t, db, 1004, "Tinnitus Relief Daily", "Tinnitus sufferers try this")

	c, err := New(db, log, Config{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Read != 124 || !res.Trained {
		t.Fatalf("first run: %+v", res)
	}

	if got := read(t, db, 1); got.vertical != "blood-pressure" || got.category != "heart" || got.unsure || got.source != "ad" {
		t.Errorf("sure creative: %+v", got)
	}
	if got := read(t, db, 1004); got.vertical != "tinnitus" || got.unsure {
		t.Errorf("brand and headline: %+v", got)
	}
	if got := read(t, db, 1003); got.vertical != "" || !got.unsure {
		t.Errorf("nothing to go on: %+v", got)
	}
	// What the model would have said is kept, to see why it declined:
	// here nothing, as it knows none of the words.
	var guess string
	if err := db.QueryRow(ctx, `SELECT model_top::text FROM spy.creative_class WHERE creative_id = 1003`).
		Scan(&guess); err != nil || guess != `{"top": [], "model": 1}` {
		t.Errorf("declined creative's look: %q %v", guess, err)
	}
	if got := read(t, db, 1001); got.vertical != "blood-pressure" || got.source != "model" || got.confidence < minAnswer {
		t.Errorf("model should answer from 'cardiologist': %+v", got)
	}
	if got := read(t, db, 1002); got.vertical != "tinnitus" || got.source != "model" {
		t.Errorf("model should answer from 'audiologist': %+v", got)
	}

	// Every number reads Spy's own vertical through spy.creative_vertical.
	var v string
	if err := db.QueryRow(ctx, `SELECT vertical FROM spy.creative_vertical WHERE creative_id = 1001`).Scan(&v); err != nil || v != "blood-pressure" {
		t.Errorf("spy.creative_vertical: %q %v", v, err)
	}

	// Nothing new: nothing read, nothing asked.
	res, err = c.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Read != 0 || res.Answered != 0 || res.Trained {
		t.Errorf("second run should do nothing: %+v", res)
	}

	// A new ad makes a creative read again; the rules are now sure.
	if _, err := db.Exec(ctx, `INSERT INTO tracks_api.ad_v1 (id, creative_id, headline) VALUES (99999, 1001, 'Lower blood pressure in 7 days')`); err != nil {
		t.Fatal(err)
	}
	res, err = c.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Read != 1 {
		t.Errorf("a new ad should read one creative: %+v", res)
	}
	if got := read(t, db, 1001); got.vertical != "blood-pressure" || got.source != "ad" || got.unsure {
		t.Errorf("after a new ad: %+v", got)
	}
}

// A model trained while the first reads were filling in is retrained once
// the sure creatives have grown enough, not a day later.
func TestClassifierRetrainsWhenExamplesGrow(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	c, err := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	if err != nil {
		t.Fatal(err)
	}
	id := 1
	add := func(n int) {
		for i := 0; i < n; i++ {
			seed(t, db, id, "", fmt.Sprintf("Cardiologist warns: high blood pressure trick number %d", id))
			seed(t, db, id+1, "", fmt.Sprintf("Audiologist explains: tinnitus relief secret %d", id))
			seed(t, db, id+2, "", fmt.Sprintf("Melt stubborn belly fat, weight loss hack %d", id))
			id += 3
		}
	}
	add(20)
	for i, want := range []bool{true, false} {
		res, err := c.Run(ctx)
		if err != nil || res.Trained != want {
			t.Fatalf("run %d with 60 creatives: %+v, %v", i, res, err)
		}
	}
	add(30) // 150 now: 2.5 times, but only 90 more
	if res, err := c.Run(ctx); err != nil || res.Trained {
		t.Fatalf("90 more should not retrain: %+v, %v", res, err)
	}
	add(40) // 270 now: 210 more
	if res, err := c.Run(ctx); err != nil || !res.Trained {
		t.Fatalf("210 more should retrain: %+v, %v", res, err)
	}
	if res, err := c.Run(ctx); err != nil || res.Trained {
		t.Fatalf("and then not again: %+v, %v", res, err)
	}
}

// Landing page titles from Raposa count when the login may read them.
func TestClassifierPages(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `
		CREATE SCHEMA raposa_api;
		CREATE TABLE raposa_api.evidence_v1 (id BIGINT PRIMARY KEY, creative_id INTEGER, title TEXT)`); err != nil {
		t.Fatal(err)
	}
	seed(t, db, 1, "", "Seniors are loving this")
	c, err := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(t, db, 1); got.vertical != "" {
		t.Fatalf("before the page: %+v", got)
	}
	if _, err := db.Exec(ctx, `INSERT INTO raposa_api.evidence_v1 VALUES (5, 1, 'Ringing in your ears? Tinnitus breakthrough')`); err != nil {
		t.Fatal(err)
	}
	res, err := c.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, db, 1); res.Read != 1 || got.vertical != "tinnitus" || got.source != "page" {
		t.Errorf("after the page: %+v %+v", res, got)
	}
}

// A creative whose ads say nothing is classified from its walked landing
// page, and read again when the page changes.
func TestClassifierReadsWalkedPage(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	seed(t, db, 1, "", "You won't believe what happened next")
	if _, err := db.Exec(ctx, `
		INSERT INTO tracks_api.page_version_v1 (hash, title, headings, text) VALUES
		    ('00000000-0000-0000-0000-000000000001', 'A doctor''s story', '{"h1": ["The ringing in your ears"]}',
		     'Tinnitus keeps millions awake. This tinnitus routine takes a minute.'),
		    ('00000000-0000-0000-0000-000000000002', 'Blood pressure breakthrough', '{}', 'Hypertension and blood pressure.');
		INSERT INTO spy.creative_page (creative_id, version_hash, walked_at, changed_at)
		VALUES (1, '00000000-0000-0000-0000-000000000001', now(), now() - interval '1 hour');`); err != nil {
		t.Fatal(err)
	}
	c, err := New(db, log, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(t, db, 1); got.vertical != "tinnitus" || (got.source != "body" && got.source != "page") {
		t.Fatalf("from the page: %+v", got)
	}
	if res, err := c.Run(ctx); err != nil || res.Read != 0 {
		t.Fatalf("nothing changed, yet %d read (%v)", res.Read, err)
	}
	if _, err := db.Exec(ctx, `UPDATE spy.creative_page SET version_hash = '00000000-0000-0000-0000-000000000002', changed_at = now()`); err != nil {
		t.Fatal(err)
	}
	if res, err := c.Run(ctx); err != nil || res.Read != 1 {
		t.Fatalf("the page changed, %d read (%v)", res.Read, err)
	}
	if got := read(t, db, 1); got.vertical != "blood-pressure" {
		t.Errorf("after the page changed: %+v", got)
	}
}
