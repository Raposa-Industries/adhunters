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
