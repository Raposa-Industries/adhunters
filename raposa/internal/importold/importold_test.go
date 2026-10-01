package importold

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/raposa/internal/testdb"
	"github.com/Raposa-Industries/adhunters/shared/files"
)

// oldSchema is the part of the collector's database (e20148c) the import
// reads, column for column.
const oldSchema = `
CREATE SCHEMA spy;
CREATE TABLE spy.creative (id INTEGER PRIMARY KEY, uid UUID NOT NULL DEFAULT gen_random_uuid(), network_id SMALLINT NOT NULL DEFAULT 1,
    creative_key TEXT NOT NULL UNIQUE, image_url TEXT NOT NULL DEFAULT '', first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE spy.ad (id INTEGER PRIMARY KEY, creative_id INTEGER NOT NULL REFERENCES spy.creative(id), headline TEXT NOT NULL);
CREATE TABLE spy.raposa_setting (key TEXT PRIMARY KEY, value TEXT NOT NULL, note TEXT NOT NULL DEFAULT '');
CREATE TABLE spy.raposa_disguise (id SMALLINT PRIMARY KEY, rung SMALLINT NOT NULL, code TEXT NOT NULL UNIQUE);
CREATE TABLE spy.raposa_page (
    id INTEGER PRIMARY KEY, content_hash UUID NOT NULL UNIQUE, url TEXT NOT NULL, host TEXT NOT NULL, path TEXT NOT NULL,
    title TEXT, page_kind TEXT, word_count INTEGER NOT NULL DEFAULT 0, html TEXT, body_text TEXT,
    html_bytes INTEGER NOT NULL DEFAULT 0, headings JSONB NOT NULL DEFAULT '{}', meta_tags JSONB NOT NULL DEFAULT '{}',
    pixels JSONB NOT NULL DEFAULT '{}', checkout_platform TEXT, checkout_merchant_id TEXT,
    prices JSONB NOT NULL DEFAULT '[]', outbound_links JSONB NOT NULL DEFAULT '[]', is_dark BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    times_seen INTEGER NOT NULL DEFAULT 1, page_key TEXT, text_digest TEXT, capture_state TEXT NOT NULL DEFAULT 'html',
    capture_note TEXT, capture_attempts SMALLINT NOT NULL DEFAULT 0, captured_at TIMESTAMPTZ, rendered_html TEXT,
    capture_line TEXT, capture_bytes BIGINT NOT NULL DEFAULT 0);
CREATE TABLE spy.raposa_asset (content_hash UUID PRIMARY KEY, media_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes INTEGER NOT NULL DEFAULT 0, bytes BYTEA, skipped_reason TEXT, first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    object_key TEXT);
CREATE TABLE spy.raposa_page_asset (page_id INTEGER NOT NULL REFERENCES spy.raposa_page(id),
    content_hash UUID NOT NULL REFERENCES spy.raposa_asset(content_hash), role TEXT NOT NULL DEFAULT 'other', source_url TEXT NOT NULL);
CREATE TABLE spy.raposa_job (
    id INTEGER PRIMARY KEY, uid UUID NOT NULL DEFAULT gen_random_uuid(), creative_id INTEGER NOT NULL REFERENCES spy.creative(id),
    ad_id INTEGER REFERENCES spy.ad(id), status TEXT NOT NULL DEFAULT 'PENDING', target_click_url TEXT NOT NULL,
    publisher_referer TEXT NOT NULL DEFAULT '', stop_requested BOOLEAN NOT NULL DEFAULT FALSE, is_cloaked BOOLEAN NOT NULL DEFAULT FALSE,
    cloaked_confidence NUMERIC(5, 2) NOT NULL DEFAULT 0, reviewer_data JSONB NOT NULL DEFAULT '{}', raposa_data JSONB NOT NULL DEFAULT '{}',
    checkout_data JSONB NOT NULL DEFAULT '{}', logs TEXT NOT NULL DEFAULT '', requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ, mode TEXT NOT NULL DEFAULT 'deep', visits_target INTEGER NOT NULL DEFAULT 0,
    visits_done INTEGER NOT NULL DEFAULT 0, stage TEXT NOT NULL DEFAULT 'queued', stage_note TEXT NOT NULL DEFAULT '',
    rung_reached SMALLINT NOT NULL DEFAULT 0, breach_rung SMALLINT, white_page_id INTEGER REFERENCES spy.raposa_page(id),
    variants_count SMALLINT NOT NULL DEFAULT 0, target_device TEXT NOT NULL DEFAULT 'desktop', publisher_id INTEGER,
    bytes_used INTEGER NOT NULL DEFAULT 0, worker_node TEXT NOT NULL DEFAULT '', started_at TIMESTAMPTZ,
    retry_of INTEGER REFERENCES spy.raposa_job(id), attempt SMALLINT NOT NULL DEFAULT 1, origin TEXT NOT NULL DEFAULT 'user');
CREATE TABLE spy.raposa_visit (
    id INTEGER PRIMARY KEY, job_id INTEGER NOT NULL REFERENCES spy.raposa_job(id), purpose TEXT NOT NULL DEFAULT 'ladder',
    rung SMALLINT NOT NULL, disguise_id SMALLINT REFERENCES spy.raposa_disguise(id), attempt SMALLINT NOT NULL DEFAULT 1,
    line_key TEXT NOT NULL DEFAULT '', place TEXT NOT NULL DEFAULT '', exit_ip TEXT, device TEXT NOT NULL DEFAULT '',
    engine TEXT NOT NULL DEFAULT 'fetch', link_kind TEXT NOT NULL DEFAULT 'live', target_url TEXT NOT NULL DEFAULT '',
    referer TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL DEFAULT 'error', landed_page_id INTEGER REFERENCES spy.raposa_page(id),
    steps_count SMALLINT NOT NULL DEFAULT 0, status_code SMALLINT, redirect_hops JSONB NOT NULL DEFAULT '[]',
    bytes_used INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0, error TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE spy.raposa_step (visit_id INTEGER NOT NULL REFERENCES spy.raposa_visit(id), step_no SMALLINT NOT NULL,
    page_id INTEGER NOT NULL REFERENCES spy.raposa_page(id), reached_by TEXT NOT NULL DEFAULT 'cta', clicked_text TEXT,
    clicked_url TEXT, PRIMARY KEY (visit_id, step_no));
CREATE TABLE spy.raposa_variant (id INTEGER PRIMARY KEY, job_id INTEGER NOT NULL REFERENCES spy.raposa_job(id),
    path_hash UUID NOT NULL, page_ids INTEGER[] NOT NULL DEFAULT '{}', first_page_id INTEGER REFERENCES spy.raposa_page(id),
    label TEXT NOT NULL DEFAULT '', visits INTEGER NOT NULL DEFAULT 0, share_pct NUMERIC(5, 2) NOT NULL DEFAULT 0,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), UNIQUE (job_id, path_hash));
`

const oldData = `
INSERT INTO spy.creative (id, creative_key) VALUES (1, 'ck-a'), (2, 'ck-b');
INSERT INTO spy.ad VALUES (1, 1, 'Doctors hate this');
INSERT INTO spy.raposa_setting VALUES ('visits_target', '80'), ('retry_limit', '4');
INSERT INTO spy.raposa_disguise VALUES (11, 1, 'reviewer'), (12, 2, 'isp-fetch'), (19, 9, 'gone');
INSERT INTO spy.raposa_page (id, content_hash, url, host, path, title, word_count, html, body_text, page_key, text_digest, capture_state) VALUES
    (1, md5('white')::uuid, 'https://go.example.com/a', 'go.example.com', '/a', 'Seven habits', 300, '<p>w</p>', 'w', 'go.example.com/a', 'dw', 'html'),
    (2, md5('dark')::uuid, 'https://offer.test/x', 'offer.test', '/x', 'The offer', 500, '<p>d</p>', 'd', NULL, NULL, 'complete'),
    (3, md5('order')::uuid, 'https://offer.test/order', 'offer.test', '/order', 'Order', 60, '<p>o</p>', 'o', 'offer.test/order', 'do', 'queued');
INSERT INTO spy.raposa_asset (content_hash, media_type, size_bytes, bytes, object_key, skipped_reason) VALUES
    (md5('small file')::uuid, 'image/png', 10, 'small file', NULL, NULL),
    (md5('big video')::uuid, 'video/mp4', 9, NULL, 'raposa/files/' || md5('big video'), NULL),
    (md5('https://cdn.test/huge.mp4')::uuid, 'video/mp4', 0, NULL, NULL, 'larger than 400 MB');
INSERT INTO spy.raposa_page_asset VALUES
    (2, md5('small file')::uuid, 'image', 'https://offer.test/a.png'),
    (2, md5('big video')::uuid, 'video', 'https://offer.test/v.mp4'),
    (2, md5('https://cdn.test/huge.mp4')::uuid, 'video', 'https://cdn.test/huge.mp4');
INSERT INTO spy.raposa_job (id, creative_id, ad_id, status, mode, target_click_url, stage, stage_note, is_cloaked,
        cloaked_confidence, breach_rung, white_page_id, visits_target, visits_done, variants_count, logs, started_at, completed_at) VALUES
    (1, 1, 1, 'COMPLETED', 'deep', 'https://go.example.com/a?x=1', 'done', 'dark funnel found', TRUE, 66.67, 2, 1, 3, 3, 1,
     E'line one\nline two\n', now() - interval '2 days', now() - interval '2 days');
INSERT INTO spy.raposa_job (id, creative_id, status, mode, origin, retry_of, attempt, target_click_url, stage, logs) VALUES
    (2, 1, 'RUNNING', 'deep', 'retry', 1, 2, 'https://go.example.com/a', 'climbing', ''),
    (3, 2, 'PENDING', 'quick', 'auto', NULL, 1, 'https://other.test/', 'queued', '');
INSERT INTO spy.raposa_visit (id, job_id, purpose, rung, disguise_id, outcome, landed_page_id, steps_count, error) VALUES
    (1, 1, 'ladder', 1, 11, 'white', 1, 1, NULL),
    (2, 1, 'sample', 2, 12, 'dark', 3, 2, NULL),
    (3, 1, 'sample', 9, 19, 'blocked', NULL, 0, NULL),
    (4, 1, 'sample', 2, 12, 'candidate', 3, 0, NULL),
    (5, 1, 'sample', 2, 12, 'unfinished', NULL, 0, NULL);
INSERT INTO spy.raposa_step VALUES
    (1, 1, 1, 'landing', NULL, NULL),
    (2, 1, 2, 'landing', NULL, NULL),
    (2, 2, 3, 'cta', 'Order now', 'https://offer.test/order');
INSERT INTO spy.raposa_variant (id, job_id, path_hash, page_ids, first_page_id, label, visits, share_pct) VALUES
    (1, 1, md5('path')::uuid, '{2,3}', 2, 'The offer', 2, 100);
`

// laterSchema is what the collector's migrations 043-045 added: the verdict
// rule, screenshots, and continuous sampling in windows.
const laterSchema = `
ALTER TABLE spy.raposa_visit ADD COLUMN mismatch JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE spy.raposa_job ADD COLUMN verdict_rule SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN verdict_evidence JSONB NOT NULL DEFAULT '[]'::jsonb, ADD COLUMN no_verdict BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN continuous BOOLEAN NOT NULL DEFAULT false, ADD COLUMN next_window_at TIMESTAMPTZ,
    ADD COLUMN windows_done INTEGER NOT NULL DEFAULT 0, ADD COLUMN stopped_for_good BOOLEAN NOT NULL DEFAULT false;
CREATE TABLE spy.raposa_shot (id INTEGER PRIMARY KEY, visit_id INTEGER NOT NULL REFERENCES spy.raposa_visit(id),
    side TEXT NOT NULL, page_id INTEGER REFERENCES spy.raposa_page(id),
    content_hash UUID NOT NULL REFERENCES spy.raposa_asset(content_hash), width SMALLINT NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0, taken_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    blocked_requests INTEGER NOT NULL DEFAULT 0, blocked_hosts TEXT[] NOT NULL DEFAULT '{}',
    hidden_overlays SMALLINT NOT NULL DEFAULT 0, collapsed_slots INTEGER NOT NULL DEFAULT 0,
    hidden JSONB NOT NULL DEFAULT '[]'::jsonb, words_settled BOOLEAN, device TEXT, timezone TEXT, line_key TEXT,
    UNIQUE (visit_id, side));
CREATE TABLE spy.raposa_window (id INTEGER PRIMARY KEY, job_id INTEGER NOT NULL REFERENCES spy.raposa_job(id),
    window_no INTEGER NOT NULL, speed_up BOOLEAN NOT NULL DEFAULT false, started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at TIMESTAMPTZ, visits INTEGER NOT NULL DEFAULT 0, dark_visits INTEGER NOT NULL DEFAULT 0,
    reviewer_loads INTEGER NOT NULL DEFAULT 0, res_bytes BIGINT NOT NULL DEFAULT 0, is_cloaked BOOLEAN,
    verdict_evidence JSONB NOT NULL DEFAULT '[]'::jsonb, note TEXT, UNIQUE (job_id, window_no));
ALTER TABLE spy.raposa_visit ADD COLUMN window_id INTEGER REFERENCES spy.raposa_window(id);
`

const laterData = `
UPDATE spy.raposa_job SET verdict_rule = 3, verdict_evidence = '[{"page": 2, "reviewer_loads": 10}]',
    continuous = true, windows_done = 1 WHERE id = 1;
INSERT INTO spy.raposa_window (id, job_id, window_no, visits, dark_visits, is_cloaked) VALUES (7, 1, 1, 2, 1, true);
UPDATE spy.raposa_visit SET window_id = 7 WHERE id IN (2, 4);
UPDATE spy.raposa_visit SET mismatch = '[{"kind": "pixels"}]' WHERE id = 1;
INSERT INTO spy.raposa_asset (content_hash, media_type, size_bytes, bytes) VALUES
    (md5('shot dark')::uuid, 'image/png', 9, 'shot dark'), (md5('shot white')::uuid, 'image/png', 10, 'shot white');
INSERT INTO spy.raposa_shot (id, visit_id, side, page_id, content_hash, width, height, blocked_hosts) VALUES
    (1, 2, 'dark', NULL, md5('shot dark')::uuid, 390, 2000, '{ads.test}'),
    (2, 2, 'white', 1, md5('shot white')::uuid, 390, 1800, '{}');
`

func exec(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%.80s: %v", sql, err)
	}
}

func md5hex(s string) string {
	b := md5.Sum([]byte(s))
	return hex.EncodeToString(b[:])
}

func TestImport(t *testing.T) {
	ctx := context.Background()
	old := testdb.Empty(t)
	exec(t, old, oldSchema)
	exec(t, old, oldData)
	db := testdb.New(t)
	testdb.Tracks(t, db)
	exec(t, db, `INSERT INTO tracks_api.creative_v1 (id, creative_key) VALUES (5, 'ck-a')`)
	exec(t, db, `INSERT INTO tracks_api.ad_v1 (id, creative_id, headline) VALUES (9, 5, 'Another'), (10, 5, 'Doctors hate this')`)
	// Raposa here already has the order page.
	exec(t, db, `INSERT INTO raposa.page (content_hash, page_key, text_digest, url, host, path, title)
		VALUES (md5('order')::uuid, 'offer.test/order', 'do', 'https://offer.test/order', 'offer.test', '/order', 'Order')`)

	dir := t.TempDir()
	oldFiles, err := files.Open("file://" + filepath.Join(dir, "old"))
	if err != nil {
		t.Fatal(err)
	}
	// The collector's object storage, opened at raposa/.
	video := filepath.Join(dir, "video")
	if err := os.WriteFile(video, []byte("big video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := oldFiles.Put(ctx, md5hex("big video"), "video/mp4", video, 9); err != nil {
		t.Fatal(err)
	}
	newFiles, err := files.Open("file://" + filepath.Join(dir, "new"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Old: old, New: db, OldFiles: oldFiles, Files: newFiles, TmpDir: dir, DryRun: true}

	// A dry run writes nothing.
	r, err := Run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.Copied != 2 || r.UnknownCreative != 1 || r.Failed != 0 {
		t.Fatalf("dry run: %+v", r)
	}
	var n int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM raposa.investigation`).Scan(&n)
	if n != 0 {
		t.Fatalf("the dry run wrote %d investigations", n)
	}

	cfg.DryRun = false
	r, err = Run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.Jobs != 3 || r.Copied != 2 || r.UnknownCreative != 1 || r.UnknownKeys[0] != "ck-b" || r.Failed != 0 {
		t.Fatalf("report: %+v", r)
	}
	if r.Visits != 5 || r.PagesNew != 2 || r.Files != 2 || r.FileBytes != 19 {
		t.Fatalf("counts: %+v", r)
	}
	if len(r.SettingsDiffer) != 1 || !strings.HasPrefix(r.SettingsDiffer[0], "visits_target") {
		t.Fatalf("settings: %v", r.SettingsDiffer)
	}

	var first, second int64
	_ = db.QueryRow(ctx, `SELECT investigation_id FROM raposa.imported_investigation WHERE old_id = 1`).Scan(&first)
	_ = db.QueryRow(ctx, `SELECT investigation_id FROM raposa.imported_investigation WHERE old_id = 2`).Scan(&second)

	var creative, ad int32
	var status, scope, stage string
	var cloaked bool
	var white *int32
	if err := db.QueryRow(ctx, `SELECT creative_id, ad_id, status, burn_scope, is_cloaked, white_page_id, stage
		FROM raposa.investigation WHERE id = $1`, first).Scan(&creative, &ad, &status, &scope, &cloaked, &white, &stage); err != nil {
		t.Fatal(err)
	}
	if creative != 5 || ad != 10 || status != "completed" || scope != "site:example.com" || !cloaked || white == nil || stage != "done" {
		t.Fatalf("first: creative %d ad %d %s %s cloaked %v white %v %s", creative, ad, status, scope, cloaked, white, stage)
	}
	var retryOf int64
	var origin string
	if err := db.QueryRow(ctx, `SELECT status, origin, retry_of FROM raposa.investigation WHERE id = $1`, second).Scan(&status, &origin, &retryOf); err != nil {
		t.Fatal(err)
	}
	if status != "stopped" || origin != "retry" || retryOf != first {
		t.Fatalf("second: %s %s retry_of %d, want stopped retry %d", status, origin, retryOf, first)
	}

	// Visits, steps and the variant point at raposa's pages.
	var blocked, steps, badSteps int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM raposa.visit WHERE investigation_id = $1 AND outcome = 'error' AND error = 'blocked' AND disguise_id IS NULL`, first).Scan(&blocked)
	_ = db.QueryRow(ctx, `SELECT count(*) FROM raposa.step s JOIN raposa.visit v ON v.id = s.visit_id WHERE v.investigation_id = $1`, first).Scan(&steps)
	_ = db.QueryRow(ctx, `SELECT count(*) FROM raposa.step s JOIN raposa.page p ON p.id = s.page_id
		WHERE s.clicked_text = 'Order now' AND p.content_hash <> md5('order')::uuid`).Scan(&badSteps)
	if blocked != 1 || steps != 3 || badSteps != 0 {
		t.Fatalf("blocked %d, steps %d, steps on the wrong page %d", blocked, steps, badSteps)
	}
	// The collector's later outcomes: a candidate counts as dark, an
	// unfinished page as an error.
	var dark, unfinished int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM raposa.visit WHERE investigation_id = $1 AND outcome = 'dark'`, first).Scan(&dark)
	_ = db.QueryRow(ctx, `SELECT count(*) FROM raposa.visit WHERE investigation_id = $1 AND outcome = 'error' AND error = 'unfinished'`, first).Scan(&unfinished)
	if dark != 2 || unfinished != 1 {
		t.Fatalf("dark %d (want the dark visit and the candidate), unfinished %d", dark, unfinished)
	}
	var pages int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM raposa.page`).Scan(&pages)
	if pages != 3 {
		t.Fatalf("%d pages, want 3 (the order page once)", pages)
	}
	var variantOK bool
	_ = db.QueryRow(ctx, `SELECT v.page_ids = ARRAY[(SELECT id FROM raposa.page WHERE content_hash = md5('dark')::uuid),
		(SELECT id FROM raposa.page WHERE content_hash = md5('order')::uuid)] FROM raposa.variant v WHERE investigation_id = $1`, first).Scan(&variantOK)
	if !variantOK {
		t.Fatal("the variant's pages were not mapped")
	}
	var digest, key string
	_ = db.QueryRow(ctx, `SELECT text_digest, page_key FROM raposa.page WHERE content_hash = md5('dark')::uuid`).Scan(&digest, &key)
	if digest == "" || key != "offer.test/x" {
		t.Fatalf("dark page: digest %q key %q", digest, key)
	}

	// The files: both with bytes are in the new store; the skipped one says why.
	for _, s := range []string{"small file", "big video"} {
		rc, err := newFiles.Get(ctx, files.Key(md5hex(s)))
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		if string(b) != s {
			t.Fatalf("stored %q, want %q", b, s)
		}
	}
	var skipped string
	_ = db.QueryRow(ctx, `SELECT skipped_reason FROM raposa.asset WHERE object_key IS NULL`).Scan(&skipped)
	if skipped != "larger than 400 MB" {
		t.Fatalf("skipped reason %q", skipped)
	}
	var uses int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM raposa.page_asset`).Scan(&uses)
	if uses != 3 {
		t.Fatalf("%d page files, want 3", uses)
	}
	var lines []string
	rows, _ := db.Query(ctx, `SELECT line FROM raposa.log WHERE investigation_id = $1 ORDER BY id`, first)
	for rows.Next() {
		var l string
		_ = rows.Scan(&l)
		lines = append(lines, l)
	}
	rows.Close()
	// What raposa has no column for comes first, then the collector's log.
	if len(lines) != 5 || !strings.HasPrefix(lines[0], "visits the collector called candidate") ||
		!strings.Contains(lines[1], "disguise gone") || lines[2] != "line one" || !strings.Contains(lines[4], "job 1") {
		t.Fatalf("log %q", lines)
	}

	// Again: nothing new, until Tracks knows the other creative.
	r, err = Run(ctx, cfg)
	if err != nil || r.Copied != 0 || r.AlreadyCopied != 2 || r.UnknownCreative != 1 {
		t.Fatalf("second run: %+v %v", r, err)
	}
	exec(t, db, `INSERT INTO tracks_api.creative_v1 (id, creative_key) VALUES (6, 'ck-b')`)
	r, err = Run(ctx, cfg)
	if err != nil || r.Copied != 1 || r.UnknownCreative != 0 {
		t.Fatalf("third run: %+v %v", r, err)
	}
	_ = db.QueryRow(ctx, `SELECT status FROM raposa.investigation i JOIN raposa.imported_investigation m
		ON m.investigation_id = i.id WHERE m.old_id = 3`).Scan(&status)
	if status != "waiting" {
		t.Fatalf("a job waiting in the collector is %s here, want waiting", status)
	}
}

// A file the collector kept in its object storage cannot be copied without
// that store: the job is not copied, and says why.
func TestImportNeedsTheOldStore(t *testing.T) {
	ctx := context.Background()
	old := testdb.Empty(t)
	exec(t, old, oldSchema)
	exec(t, old, oldData)
	db := testdb.New(t)
	testdb.Tracks(t, db)
	exec(t, db, `INSERT INTO tracks_api.creative_v1 (id, creative_key) VALUES (5, 'ck-a')`)
	dir := t.TempDir()
	newFiles, _ := files.Open("file://" + filepath.Join(dir, "new"))
	r, err := Run(ctx, Config{Old: old, New: db, Files: newFiles, TmpDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if r.Failed != 1 || r.Copied != 1 || !strings.Contains(r.Errors[0], "-old-files") {
		t.Fatalf("%+v", r)
	}
	var n int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM raposa.page`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d pages written; the failed job's must not be", n)
	}
}

// A collector with migrations 043-045: the screenshots become page files,
// and the verdict rule, the windows and a visit's mismatch go into the log.
func TestImportKeepsTheRest(t *testing.T) {
	ctx := context.Background()
	old := testdb.Empty(t)
	exec(t, old, oldSchema)
	exec(t, old, oldData)
	exec(t, old, laterSchema)
	exec(t, old, laterData)
	db := testdb.New(t)
	testdb.Tracks(t, db)
	exec(t, db, `INSERT INTO tracks_api.creative_v1 (id, creative_key) VALUES (5, 'ck-a')`)
	dir := t.TempDir()
	oldFiles, _ := files.Open("file://" + filepath.Join(dir, "old"))
	video := filepath.Join(dir, "video")
	if err := os.WriteFile(video, []byte("big video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := oldFiles.Put(ctx, md5hex("big video"), "video/mp4", video, 9); err != nil {
		t.Fatal(err)
	}
	newFiles, _ := files.Open("file://" + filepath.Join(dir, "new"))
	cfg := Config{Old: old, New: db, OldFiles: oldFiles, Files: newFiles, TmpDir: dir, DryRun: true}
	for _, dry := range []bool{true, false} {
		cfg.DryRun = dry
		r, err := Run(ctx, cfg)
		if err != nil || r.Copied != 2 || r.Failed != 0 {
			t.Fatalf("dry run %v: %+v %v", dry, r, err)
		}
	}
	var first int64
	_ = db.QueryRow(ctx, `SELECT investigation_id FROM raposa.imported_investigation WHERE old_id = 1`).Scan(&first)

	// The screenshots: the dark one on the page its visit landed on, the
	// white one on the page the collector named, both stored.
	var onLanded, onWhite int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM raposa.page_asset pa JOIN raposa.visit v ON v.landed_page_id = pa.page_id
		WHERE pa.role = 'screenshot' AND pa.content_hash = md5('shot dark')::uuid AND v.investigation_id = $1
		AND pa.source_url = 'screenshot:visit-' || v.id || ':dark'`, first).Scan(&onLanded)
	_ = db.QueryRow(ctx, `SELECT count(*) FROM raposa.page_asset pa JOIN raposa.investigation i ON i.white_page_id = pa.page_id
		WHERE pa.role = 'screenshot' AND pa.content_hash = md5('shot white')::uuid AND i.id = $1`, first).Scan(&onWhite)
	if onLanded != 1 || onWhite != 1 {
		t.Fatalf("screenshots: %d on the landed page, %d on the white page", onLanded, onWhite)
	}
	for _, s := range []string{"shot dark", "shot white"} {
		rc, err := newFiles.Get(ctx, files.Key(md5hex(s)))
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		_ = rc.Close()
	}

	var visit2, visit4 int64
	_ = db.QueryRow(ctx, `SELECT min(id) + 1, min(id) + 3 FROM raposa.visit WHERE investigation_id = $1`, first).Scan(&visit2, &visit4)
	var log string
	_ = db.QueryRow(ctx, `SELECT string_agg(line, E'\n' ORDER BY id) FROM raposa.log WHERE investigation_id = $1`, first).Scan(&log)
	for _, want := range []string{
		`"verdict_rule": 3`,
		`"verdict_evidence": [{"page": 2, "reviewer_loads": 10}]`,
		`"windows_done": 1`,
		"the collector's sampling round 1 (spy.raposa_window), as it kept it: {",
		`"dark_visits": 1`,
		fmt.Sprintf("its visits: %d, %d", visit2, visit4),
		`(the collector's 1), kept as it was: {"mismatch": [{"kind": "pixels"}]}`,
		fmt.Sprintf("screenshot of the dark page at visit %d: file %s", visit2, uuidOf("shot dark")),
		`"blocked_hosts": ["ads.test"]`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the log has no %q:\n%s", want, log)
		}
	}
	// The columns copied into place are not repeated.
	if strings.Contains(log, `"window_id"`) || strings.Contains(log, `"stage"`) || strings.Contains(log, `"white_page_id"`) {
		t.Errorf("the log repeats a copied column:\n%s", log)
	}
}

func uuidOf(s string) string {
	h := md5.Sum([]byte(s))
	return uuidText(h)
}

func TestRanges(t *testing.T) {
	for _, c := range []struct {
		ids  []int64
		want string
	}{
		{nil, "none"},
		{[]int64{4}, "4"},
		{[]int64{3, 4, 5, 9, 12, 13, 14}, "3-5, 9, 12-14"},
		{[]int64{1, 3}, "1, 3"},
	} {
		if got := ranges(c.ids); got != c.want {
			t.Errorf("ranges(%v) = %q, want %q", c.ids, got, c.want)
		}
	}
}
