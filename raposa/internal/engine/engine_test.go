package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/raposa/internal/files"
	"github.com/Raposa-Industries/adhunters/raposa/internal/lines"
	"github.com/Raposa-Industries/adhunters/raposa/internal/testdb"
)

// cloaker is a site that shows the white page to a link without the ad
// network's click value, and the dark funnel to one with it.
func cloaker(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("tblci") != "" {
			http.Redirect(w, r, "/offer?cid="+r.URL.Query().Get("tblci"), http.StatusFound)
			return
		}
		fmt.Fprint(w, `<html><head><title>7 Morning Habits</title></head><body><h1>7 Morning Habits</h1>`+
			`<p>A short piece about breakfast, walking and sleep, for everyone.</p></body></html>`)
	})
	mux.HandleFunc("/offer", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>She Fixed Her Ringing Ears</title></head><body>`+
			`<h1>She Fixed Her Ringing Ears In 3 Weeks</h1><p>Watch the presentation before it is taken down.</p>`+
			`<a href="/order">Watch the presentation now</a><a href="/order">Order now</a></body></html>`)
	})
	mux.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Order</title></head><body><h1>Order your bottles</h1>`+
			`<p>Three bottles, free shipping.</p></body></html>`)
	})
	mux.HandleFunc("/video.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		fmt.Fprint(w, strings.Repeat("v", 4096))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type fixture struct {
	pool *pgxpool.Pool
	e    *Engine
	site *httptest.Server
	host string
	dir  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	testdb.Tracks(t, pool)
	site := cloaker(t)
	u, _ := url.Parse(site.URL)
	host := u.Hostname()
	ctx := context.Background()

	// The fetch rungs, straight out: a test has no proxy lines.
	mustExec(t, pool, `UPDATE raposa.disguise SET line_role = 'direct'`)
	mustExec(t, pool, `UPDATE raposa.disguise SET enabled = FALSE WHERE engine = 'browser'`)
	mustExec(t, pool, `UPDATE raposa.setting SET value = '3' WHERE key = 'visits_target'`)
	mustExec(t, pool, `UPDATE raposa.setting SET value = '1' WHERE key = 'visits_window_minutes'`)
	mustExec(t, pool, `UPDATE raposa.setting SET value = '1' WHERE key = 'live_link_wait_seconds'`)

	// Tracks saw creative 5 (ad 10) on Fox News, on desktop, in campaign c-1.
	mustExec(t, pool, `INSERT INTO tracks_api.publisher_v1 (id, name, domain) VALUES (1, 'Fox News', 'foxnews.com')`)
	mustExec(t, pool, `INSERT INTO tracks_api.campaign_v1 (id, external_id, account_id) VALUES (1, 'c-1', 7)`)
	mustExec(t, pool, `INSERT INTO tracks_api.ad_v1 (id, creative_id, last_seen_at) VALUES (10, 5, now() - interval '2 minutes')`)
	mustExec(t, pool, `INSERT INTO tracks_api.link_v1 (id, host, sample_url) VALUES (1, $1, $2)`, host, site.URL+"/")
	mustExec(t, pool, `INSERT INTO tracks_api.creative_link_daily_v1 VALUES (CURRENT_DATE, 5, 1, 40, now(), now())`)
	mustExec(t, pool, `INSERT INTO tracks_api.creative_campaign_daily_v1 VALUES (CURRENT_DATE, 5, 1, 40, now(), now())`)
	mustExec(t, pool, `INSERT INTO tracks_api.ad_daily_v1 (day, ad_id, publisher_id, device_id, creative_id, sightings)
		VALUES (CURRENT_DATE, 10, 1, 1, 5, 40)`)
	mustExec(t, pool, `INSERT INTO tracks_api.sighting_v1 (seen_at, ad_id, creative_id, publisher_id, device_id, campaign_id)
		SELECT now() - interval '5 minutes', 10, 5, 1, 1, 1 FROM generate_series(1, 5)`)

	dir := t.TempDir()
	fs, err := files.Open("file://" + filepath.Join(dir, "files"))
	if err != nil {
		t.Fatal(err)
	}
	targets := []Target{{Name: "Fox News", Domain: "foxnews.com", URL: "https://www.foxnews.com/lifestyle",
		TaboolaAccount: "foxnews-foxnews", Path: "/lifestyle", Placement: "rbox-t2m"}}
	e := New(Config{Node: "test", Workers: 1, KeepDir: filepath.Join(dir, "keep"), BrowserAddr: "http://127.0.0.1:1"},
		slog.New(slog.NewTextHandler(io.Discard, nil)), NewStore(pool), lines.New(nil), targets, fs, nil)
	_ = ctx
	return &fixture{pool: pool, e: e, site: site, host: host, dir: dir}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// liveLinks files n live links for the ad, as capture would.
func (f *fixture) liveLinks(t *testing.T, n int) {
	for i := 0; i < n; i++ {
		mustExec(t, f.pool, `INSERT INTO tracks_api.live_link_v1 (host, url, network, campaign_external_id, device, publisher, page_url, ad_id, creative_id, seen_at)
			VALUES ($1, $2, 'taboola', 'c-1', 'desktop', 'Fox News', 'https://www.foxnews.com/lifestyle', 10, 5, now())`,
			f.host, fmt.Sprintf("%s/?tblci=click%d", f.site.URL, i))
	}
}

// drive makes visits until the investigation ends, skipping the waits
// between them. It returns the step results.
func (f *fixture) drive(t *testing.T, id int64, maxSteps int) []string {
	t.Helper()
	ctx := context.Background()
	var results []string
	for i := 0; i < maxSteps; i++ {
		var status string
		if err := f.pool.QueryRow(ctx, `SELECT status FROM raposa.investigation WHERE id = $1`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "waiting" && status != "running" {
			return results
		}
		mustExec(t, f.pool, `UPDATE raposa.investigation SET next_visit_at = now() WHERE id = $1`, id)
		inv, err := f.e.store.Claim(ctx, "test")
		if err != nil {
			t.Fatal(err)
		}
		if inv == nil {
			t.Fatalf("investigation %d is %s but could not be claimed", id, status)
		}
		results = append(results, f.e.step(ctx, inv))
		if results[len(results)-1] == "error" {
			t.Fatalf("a step failed: %s", strings.Join(f.logLines(t, id), "\n"))
		}
	}
	t.Fatalf("investigation %d did not end in %d steps: %v", id, maxSteps, results)
	return nil
}

func (f *fixture) logLines(t *testing.T, id int64) []string {
	rows, err := f.pool.Query(context.Background(), `SELECT line FROM raposa.log WHERE investigation_id = $1 ORDER BY id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		_ = rows.Scan(&l)
		out = append(out, l)
	}
	return out
}

func TestDeepInvestigationFindsTheDarkFunnel(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.liveLinks(t, 20)
	mustExec(t, f.pool, `INSERT INTO raposa.watch (creative_id, pushcut_notification, created_by) VALUES (5, 'Raposa', 'test')`)

	id, err := f.e.store.Request(ctx, 5, "deep", nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	// Asking again while it waits hands back the same one.
	if again, _ := f.e.store.Request(ctx, 5, "quick", nil, "test"); again != id {
		t.Fatalf("a second request queued %d next to %d", again, id)
	}
	f.drive(t, id, 40)

	var status, stage string
	var cloaked bool
	var confidence float64
	var breach *int16
	var white *int32
	var visitsDone, variants int
	var dark []byte
	if err := f.pool.QueryRow(ctx, `
		SELECT v.status, v.stage, v.is_cloaked, v.cloaked_confidence::float8, v.breach_rung, v.white_page_id,
		       v.visits_done, v.variants_count, i.raposa_data
		FROM raposa_api.investigation_v1 v JOIN raposa.investigation i USING (id) WHERE id = $1`, id).
		Scan(&status, &stage, &cloaked, &confidence, &breach, &white, &visitsDone, &variants, &dark); err != nil {
		t.Fatal(err)
	}
	logs := strings.Join(f.logLines(t, id), "\n")
	if status != "completed" || !cloaked || breach == nil || *breach != 2 || white == nil {
		t.Fatalf("status %s cloaked %v breach %v white %v\n%s", status, cloaked, breach, white, logs)
	}
	if confidence != 100 || visitsDone != 3 || variants != 1 {
		t.Fatalf("confidence %.1f, visits %d, variants %d\n%s", confidence, visitsDone, variants, logs)
	}
	var side sideData
	if err := json.Unmarshal(dark, &side); err != nil || len(side.Variants) != 1 || side.Variants[0].Visits != 3 {
		t.Fatalf("raposa_data = %s (%v)", dark, err)
	}

	// Every visit is on record: two reviewer loads, the breakthrough, and
	// three sample visits each with its reviewer twin.
	var ladder, sample, whiteVisits int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE purpose = 'ladder'), count(*) FILTER (WHERE purpose = 'sample'),
		       count(*) FILTER (WHERE outcome = 'white')
		FROM raposa.visit WHERE investigation_id = $1`, id).Scan(&ladder, &sample, &whiteVisits); err != nil {
		t.Fatal(err)
	}
	if ladder != 3 || sample != 6 || whiteVisits != 5 {
		t.Fatalf("ladder %d sample %d white %d\n%s", ladder, sample, whiteVisits, logs)
	}

	// The dark funnel was walked past the landing page on the sample visits.
	var steps int
	if err := f.pool.QueryRow(ctx, `
		SELECT max(steps_count) FROM raposa.visit WHERE investigation_id = $1 AND purpose = 'sample' AND outcome = 'dark'`, id).
		Scan(&steps); err != nil || steps < 2 {
		t.Fatalf("the sample walked %d steps (%v)", steps, err)
	}

	// Evidence: the dark landing page once, and the white page on the ad's
	// own domain once.
	var evidence, darkEvidence int
	var account *int32
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE outcome = 'dark'), max(account_id)
		FROM raposa_api.evidence_v1 WHERE investigation_id = $1`, id).Scan(&evidence, &darkEvidence, &account); err != nil {
		t.Fatal(err)
	}
	if evidence != 2 || darkEvidence != 1 || account == nil || *account != 7 {
		t.Fatalf("evidence %d, dark %d, account %v", evidence, darkEvidence, account)
	}

	// The watch heard about the start, the dark page and the end.
	var kinds string
	if err := f.pool.QueryRow(ctx, `
		SELECT string_agg(ev.kind, ',' ORDER BY ev.id) FROM raposa.delivery d JOIN raposa.event ev ON ev.id = d.event_id
		WHERE ev.investigation_id = $1`, id).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	if kinds != "started,dark_found,finished" {
		t.Fatalf("deliveries: %s", kinds)
	}

	// A deep investigation's pages wait for the keeper; the error pages do not.
	var queued int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM raposa.page WHERE capture_state = 'queued'`).Scan(&queued); err != nil || queued < 3 {
		t.Fatalf("%d pages queued for the keeper (%v)", queued, err)
	}

	// A dark page was found, so no retry.
	var retries int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM raposa.investigation WHERE retry_of = $1`, id).Scan(&retries)
	if retries != 0 {
		t.Fatalf("%d retries queued after a dark page", retries)
	}
}

// With no live link at all, a quick investigation waits for one, records
// each visit that found none, and fails: nothing past the baseline loaded,
// so the ad was not tested.
func TestQuickInvestigationWithoutLiveLinks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id, err := f.e.store.Request(ctx, 5, "quick", nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	var results []string
	for i := 0; i < 40; i++ {
		r := f.drive1(t, id)
		if r == "" {
			break
		}
		results = append(results, r)
		if r == "wait" {
			time.Sleep(1100 * time.Millisecond)
		}
	}
	var status, note string
	if err := f.pool.QueryRow(ctx, `SELECT status, stage_note FROM raposa.investigation WHERE id = $1`, id).Scan(&status, &note); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !strings.Contains(note, "not tested") {
		t.Fatalf("status %s: %s (%v)", status, note, results)
	}
	var noLink int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM raposa.visit WHERE investigation_id = $1 AND error LIKE 'no fresh link%'`, id).Scan(&noLink)
	if noLink != 2 {
		t.Fatalf("%d visits recorded with no live link, want the 2 tries of the one free rung", noLink)
	}
	// Quick investigations are never retried.
	var n int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM raposa.investigation`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d investigations after a failed quick one", n)
	}
}

// drive1 makes one step and returns its result, or "" once it has ended.
func (f *fixture) drive1(t *testing.T, id int64) string {
	t.Helper()
	ctx := context.Background()
	var status string
	_ = f.pool.QueryRow(ctx, `SELECT status FROM raposa.investigation WHERE id = $1`, id).Scan(&status)
	if status != "waiting" && status != "running" {
		return ""
	}
	mustExec(t, f.pool, `UPDATE raposa.investigation SET next_visit_at = now() WHERE id = $1`, id)
	inv, err := f.e.store.Claim(ctx, "test")
	if err != nil || inv == nil {
		t.Fatalf("claim: %v %v", inv, err)
	}
	return f.e.step(ctx, inv)
}

// A deep investigation that finds no dark page is queued again on the paced
// queue, and a stop request ends a running one at its next visit.
func TestRetryAndStop(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// Only the reviewer and one free rung, and the live links go to the
	// white page: nothing is cloaked.
	mustExec(t, f.pool, `UPDATE raposa.disguise SET enabled = FALSE WHERE rung > 2`)
	for i := 0; i < 5; i++ {
		mustExec(t, f.pool, `INSERT INTO tracks_api.live_link_v1 (host, url, network, campaign_external_id, device, publisher, ad_id, creative_id, seen_at)
			VALUES ($1, $2, 'taboola', 'c-1', 'desktop', 'Fox News', 10, 5, now())`, f.host, f.site.URL+"/?plain=1")
	}
	id, _ := f.e.store.Request(ctx, 5, "deep", nil, "test")
	f.drive(t, id, 20)
	var retry int64
	var attempt int
	var at time.Time
	if err := f.pool.QueryRow(ctx, `SELECT id, attempt, next_visit_at FROM raposa.investigation WHERE retry_of = $1`, id).
		Scan(&retry, &attempt, &at); err != nil {
		t.Fatalf("no retry queued: %v\n%s", err, strings.Join(f.logLines(t, id), "\n"))
	}
	if attempt != 2 || time.Until(at) < 25*time.Minute {
		t.Fatalf("retry attempt %d at %s", attempt, at)
	}

	// Stop the retry after its first visit.
	if r := f.drive1(t, retry); r != "visit" {
		t.Fatalf("first step: %s", r)
	}
	if ok, err := f.e.store.Stop(ctx, retry); err != nil || !ok {
		t.Fatalf("stop: %v %v", ok, err)
	}
	if r := f.drive1(t, retry); r != "finished" {
		t.Fatalf("step after stop: %s", r)
	}
	var status string
	_ = f.pool.QueryRow(ctx, `SELECT status FROM raposa.investigation WHERE id = $1`, retry).Scan(&status)
	if status != "stopped" {
		t.Fatalf("status after stop = %s", status)
	}
	// A stopped retry is not retried again.
	var n int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM raposa.investigation`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d investigations", n)
	}
}

// A step whose claim was taken over writes nothing.
func TestLostClaimWritesNothing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id, _ := f.e.store.Request(ctx, 5, "deep", nil, "test")
	inv, err := f.e.store.Claim(ctx, "test")
	if err != nil || inv == nil || inv.ID != id {
		t.Fatalf("claim: %v %v", inv, err)
	}
	mustExec(t, f.pool, `UPDATE raposa.investigation SET claim_token = gen_random_uuid() WHERE id = $1`, id)
	if r := f.e.step(ctx, inv); r != "lease_lost" {
		t.Fatalf("step = %s", r)
	}
	var status string
	var logs int
	_ = f.pool.QueryRow(ctx, `SELECT status, (SELECT count(*) FROM raposa.log WHERE investigation_id = $1) FROM raposa.investigation WHERE id = $1`, id).Scan(&status, &logs)
	if status != "waiting" || logs != 0 {
		t.Fatalf("status %s, %d log lines after a lost claim", status, logs)
	}
}

// A stopping node lets go of the investigation without recording the visit.
func TestStoppingNodeReleasesTheClaim(t *testing.T) {
	f := newFixture(t)
	id, _ := f.e.store.Request(context.Background(), 5, "deep", nil, "test")
	inv, _ := f.e.store.Claim(context.Background(), "test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := f.e.step(ctx, inv); r != "stopped" {
		t.Fatalf("step = %s", r)
	}
	var token *string
	_ = f.pool.QueryRow(context.Background(), `SELECT claim_token::text FROM raposa.investigation WHERE id = $1`, id).Scan(&token)
	if token != nil {
		t.Fatal("the claim was kept")
	}
}

// The keeper opens a queued page through the runner, stores every file in
// the file store under its md5, and downloads a whole-file video itself.
func TestKeeperKeepsThePageWhole(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.liveLinks(t, 20)
	id, _ := f.e.store.Request(ctx, 5, "deep", nil, "test")
	f.drive(t, id, 40)

	var calls atomic.Int32
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req KeepRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.HTML == "" || req.Dir == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = os.MkdirAll(req.Dir, 0o750)
		p := filepath.Join(req.Dir, "f1")
		_ = os.WriteFile(p, []byte("body{color:red}"), 0o600)
		_ = json.NewEncoder(w).Encode(KeepAnswer{
			OK: true, Bytes: 15, RenderedHTML: "<html>rendered</html>",
			Files: []KeptFile{
				{URL: "https://cdn.example.com/a.css", Role: "stylesheet", MediaType: "text/css", Path: p, Size: 15},
				{URL: "https://cdn.example.com/big.png", Role: "image", SkippedReason: "larger than the limit"},
			},
			Videos: []KeptVideo{{URL: f.site.URL + "/video.mp4", Kind: "file", Download: true}},
		})
	}))
	defer runner.Close()
	f.e.browser = NewBrowserClient(runner.URL)

	kept := 0
	for {
		done, err := f.e.KeepOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !done {
			break
		}
		kept++
	}
	if kept == 0 || int(calls.Load()) != kept {
		t.Fatalf("kept %d pages over %d runner calls", kept, calls.Load())
	}
	var complete, left int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE capture_state = 'complete'),
		count(*) FILTER (WHERE capture_state IN ('queued', 'capturing')) FROM raposa.page`).Scan(&complete, &left)
	if complete != kept || left != 0 {
		t.Fatalf("complete %d, left %d", complete, left)
	}
	var key string
	var size int64
	if err := f.pool.QueryRow(ctx, `SELECT a.object_key, a.size_bytes FROM raposa.asset a
		JOIN raposa.page_asset pa USING (content_hash) WHERE pa.role = 'video' LIMIT 1`).Scan(&key, &size); err != nil {
		t.Fatal(err)
	}
	if size != 4096 || !strings.HasPrefix(key, "files/") {
		t.Fatalf("video kept as %s, %d bytes", key, size)
	}
	rc, err := f.e.files.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if len(b) != 4096 {
		t.Fatalf("stored video is %d bytes", len(b))
	}
	var skipped string
	_ = f.pool.QueryRow(ctx, `SELECT a.skipped_reason FROM raposa.asset a JOIN raposa.page_asset pa USING (content_hash)
		WHERE pa.source_url = 'https://cdn.example.com/big.png' LIMIT 1`).Scan(&skipped)
	if skipped == "" {
		t.Fatal("a file the runner did not keep has no reason")
	}
	// The keep folders are cleaned up.
	entries, _ := os.ReadDir(f.e.cfg.KeepDir)
	if len(entries) != 0 {
		t.Fatalf("%d keep folders left behind", len(entries))
	}
}

// Watches are delivered once to Pushcut, and recorded as skipped on a box
// with no Pushcut key.
func TestNotifier(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var got atomic.Int32
	var lastKey, lastPath string
	push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		lastKey, lastPath = r.Header.Get("API-Key"), r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer push.Close()
	old := pushcutURL
	pushcutURL = push.URL + "/v1/notifications/"
	defer func() { pushcutURL = old }()

	id, _ := f.e.store.Request(ctx, 5, "deep", nil, "test")
	mustExec(t, f.pool, `INSERT INTO raposa.watch (investigation_id, pushcut_notification) VALUES ($1, 'Raposa Alert')`, id)
	mustExec(t, f.pool, `SELECT raposa.emit($1, 'started', 'Raposa started', 'deep')`, id)

	f.e.cfg.PushcutKey = ""
	if n, err := f.e.NotifyOnce(ctx, http.DefaultClient); err != nil || n != 1 {
		t.Fatalf("notify without a key: %d %v", n, err)
	}
	var status string
	_ = f.pool.QueryRow(ctx, `SELECT status FROM raposa.delivery`).Scan(&status)
	if status != "skipped" || got.Load() != 0 {
		t.Fatalf("without a key: status %s, %d sent", status, got.Load())
	}

	f.e.cfg.PushcutKey = "secret"
	mustExec(t, f.pool, `SELECT raposa.emit($1, 'finished', 'Investigation finished', 'done')`, id)
	if n, err := f.e.NotifyOnce(ctx, http.DefaultClient); err != nil || n != 1 {
		t.Fatalf("notify: %d %v", n, err)
	}
	if n, _ := f.e.NotifyOnce(ctx, http.DefaultClient); n != 0 {
		t.Fatalf("delivered twice")
	}
	if got.Load() != 1 || lastKey != "secret" || lastPath != "/v1/notifications/Raposa%20Alert" && lastPath != "/v1/notifications/Raposa Alert" {
		t.Fatalf("pushcut got %d calls, key %q, path %q", got.Load(), lastKey, lastPath)
	}
}
