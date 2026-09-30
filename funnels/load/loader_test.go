package load

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/funnels/edge"
	"github.com/Raposa-Industries/adhunters/funnels/internal/testdb"
	"github.com/Raposa-Industries/adhunters/shared/archive"
	"github.com/Raposa-Industries/adhunters/shared/spool"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func quiet() *slog.Logger        { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func post(e *edge.Edge, body string) { send(e, body, iphone) }

func send(e *edge.Edge, body, ua string) {
	req := httptest.NewRequest("POST", "https://lp.example.com/e", strings.NewReader(body))
	req.Header.Set("User-Agent", ua)
	req.Header.Set("CF-Connecting-IP", "203.0.113.5")
	req.Header.Set("CF-IPCountry", "US")
	e.Handler().ServeHTTP(httptest.NewRecorder(), req)
}

// Beacons go through the real edge and spool, the loader archives, loads
// and closes them, and funnels_api shows the journey, its steps, drop-off
// and the retention curve.
func TestFromBeaconToCounts(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	spoolDir := filepath.Join(dir, "spool")
	clk := &clock{t: time.Date(2026, 9, 30, 14, 10, 5, 0, time.UTC)}
	w, err := spool.Open(spoolDir, "edge", "a", quiet(), spool.Options{Now: clk.now})
	must(t, err)
	e := edge.New(edge.Config{
		Sites: &edge.Sites{Root: filepath.Join(dir, "sites")}, Spool: w, Instance: "a", Version: "test",
		IPKey: []byte("0123456789abcdef"), TrustCloudflare: true, Log: quiet(),
		Metrics: edge.NewMetrics(prometheus.NewRegistry()), Now: clk.now,
	})

	b := func(j, lp string, in int, events string) string {
		return `{"v":1,"j":"` + j + `","site":"lp.example.com","lp":"` + lp + `","url":"/","c":"c-` + j +
			`","s":{"sub1":"50549004","sub4":"777","sub8":"1322"},"in":` + string(rune('0'+in)) + `,"wd":0,"sw":390,"e":[` + events + `]}`
	}
	// Journey A watches to the pitch and reaches checkout.
	post(e, b("journeyAAAA", "vsl", 0, `{"k":"view","t":1},{"k":"video","id":"bp","ev":"load","len":20,"pitch":10},{"k":"video","id":"bp","ev":"autoplay"}`))
	// Journey B leaves after 5 s of the video.
	post(e, b("journeyBBBB", "vsl", 1, `{"k":"view","t":1},{"k":"video","id":"bp","ev":"play","len":20,"pitch":10}`))
	// A bot.
	send(e, b("journeyBOT1", "vsl", 0, `{"k":"view","t":1}`), "curl/8.0")
	// Garbage is kept in the archive and counted, never loaded.
	post(e, `not a beacon`)
	clk.set(time.Date(2026, 9, 30, 14, 11, 5, 0, time.UTC))
	post(e, b("journeyAAAA", "vsl", 1, `{"k":"video","id":"bp","ev":"play"},{"k":"video","id":"bp","ev":"beat","w":[[0,12]]},{"k":"video","id":"bp","ev":"pitch"},{"k":"beat","vis":12000,"sc":40}`))
	post(e, b("journeyBBBB", "vsl", 1, `{"k":"video","id":"bp","ev":"beat","w":[[0,5]]},{"k":"exit","vis":5000,"sc":10}`))
	// Journey A's checkout arrives in the next hour: it still counts in 14:00.
	clk.set(time.Date(2026, 9, 30, 15, 2, 0, 0, time.UTC))
	post(e, b("journeyAAAA", "checkout", 1, `{"k":"view"},{"k":"form","n":"order"}`))
	w.Close()

	store, err := archive.Open("file://" + filepath.Join(dir, "archive"))
	must(t, err)
	l := New(Config{DB: db, Store: store, Spool: spoolDir, Log: quiet(), Metrics: NewMetrics(prometheus.NewRegistry()), Now: clk.now})
	n, err := l.Archive(ctx)
	must(t, err)
	if n != 3 {
		t.Fatalf("archived %d files, want 3", n)
	}
	if n, _ := l.Archive(ctx); n != 0 {
		t.Errorf("archived again: %d", n)
	}
	for {
		more, err := l.LoadOne(ctx)
		must(t, err)
		if !more {
			break
		}
	}
	var bad int
	must(t, db.QueryRow(ctx, `SELECT sum(bad_beacons) FROM funnels.raw_file`).Scan(&bad))
	if bad != 1 {
		t.Errorf("bad beacons = %d", bad)
	}

	// Not due yet: 14:00 closes an hour after 15:00.
	if n, _ := l.CloseDue(ctx); n != 0 {
		t.Errorf("closed %d hours too early", n)
	}
	clk.set(time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC))
	n, err = l.CloseDue(ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("closed %d hours, want 1", n)
	}

	var journeys, bots int
	must(t, db.QueryRow(ctx, `SELECT sum(journeys), sum(bots) FROM funnels_api.journey_hourly_v1 WHERE hour = '2026-09-30 14:00Z'`).Scan(&journeys, &bots))
	if journeys != 2 || bots != 1 {
		t.Errorf("journeys %d bots %d", journeys, bots)
	}
	var lastStep, lastLP string
	var watched int
	var pitch bool
	must(t, db.QueryRow(ctx, `SELECT last_step, last_lp, watched_s, reached_pitch FROM funnels_api.journey_v1 WHERE clickid = 'c-journeyAAAA'`).Scan(&lastStep, &lastLP, &watched, &pitch))
	if lastStep != "form:order" || lastLP != "checkout" || watched != 12 || !pitch {
		t.Errorf("journey A: %s %s %d %v", lastStep, lastLP, watched, pitch)
	}

	steps := map[string][2]int{}
	rows, err := db.Query(ctx, `SELECT lp || '/' || step, reached, stopped FROM funnels_api.step_hourly_v1 WHERE hour = '2026-09-30 14:00Z'`)
	must(t, err)
	for rows.Next() {
		var s string
		var r, st int
		must(t, rows.Scan(&s, &r, &st))
		steps[s] = [2]int{r, st}
	}
	if steps["vsl/view"] != [2]int{2, 0} || steps["vsl/play:bp"] != [2]int{2, 1} || steps["vsl/pitch:bp"] != [2]int{1, 0} ||
		steps["checkout/form:order"] != [2]int{1, 1} || steps["vsl/stay10s"] != [2]int{1, 0} {
		t.Errorf("steps = %v", steps)
	}
	var stopped int
	must(t, db.QueryRow(ctx, `SELECT sum(stopped) FROM funnels_api.step_hourly_v1 WHERE hour = '2026-09-30 14:00Z'`).Scan(&stopped))
	if stopped != 2 {
		t.Errorf("drop-offs add to %d, want one per journey", stopped)
	}

	curve := map[int]int{}
	rows, err = db.Query(ctx, `SELECT second, watching FROM funnels_api.video_second_hourly_v1 WHERE video = 'bp'`)
	must(t, err)
	for rows.Next() {
		var s, n int
		must(t, rows.Scan(&s, &n))
		curve[s] = n
	}
	if curve[0] != 2 || curve[4] != 2 || curve[5] != 1 || curve[11] != 1 || curve[12] != 0 {
		t.Errorf("retention = %v", curve)
	}
	var plays, loads, reached int
	must(t, db.QueryRow(ctx, `SELECT loads, plays, reached_pitch FROM funnels_api.video_hourly_v1 WHERE video = 'bp'`).Scan(&loads, &plays, &reached))
	if loads != 2 || plays != 2 || reached != 1 {
		t.Errorf("video: loads %d plays %d pitch %d", loads, plays, reached)
	}

	// Replaying the whole day gives the same numbers.
	before := snapshot(t, db)
	marked, err := l.Replay(ctx, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	must(t, err)
	if marked != 3 {
		t.Errorf("replay marked %d", marked)
	}
	for {
		more, err := l.LoadOne(ctx)
		must(t, err)
		if !more {
			break
		}
	}
	_, err = l.CloseDue(ctx)
	must(t, err)
	if after := snapshot(t, db); after != before {
		t.Errorf("replay changed the counts:\n%s\n%s", before, after)
	}
	st, err := l.Status(ctx)
	must(t, err)
	if st.Pending != 0 || st.DirtyHours != 0 || st.LastClosedHour == nil {
		t.Errorf("status = %+v", st)
	}
}

// snapshot is every count and journey as one string.
func snapshot(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var out string
	must(t, db.QueryRow(context.Background(), `
		SELECT concat_ws(' | ',
		  (SELECT string_agg(concat_ws(',', hour, site, first_lp, device, journeys, bots, visible_ms), ';' ORDER BY 1) FROM funnels.journey_hourly),
		  (SELECT string_agg(concat_ws(',', lp, step, reached, stopped, bots), ';' ORDER BY 1) FROM funnels.step_hourly),
		  (SELECT string_agg(concat_ws(',', video, loads, plays, watched_s, reached_pitch), ';' ORDER BY 1) FROM funnels.video_hourly),
		  (SELECT string_agg(concat_ws(',', second, watching), ';' ORDER BY 1) FROM funnels.video_second_hourly),
		  (SELECT string_agg(concat_ws(',', id, last_step, visible_ms, bot_reason), ';' ORDER BY 1) FROM funnels.journey))`).Scan(&out))
	return out
}

func TestQuarantinesAFileWhoseBytesChanged(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	spoolDir := filepath.Join(dir, "spool")
	at := time.Date(2026, 9, 30, 14, 10, 5, 0, time.UTC)
	w, err := spool.Open(spoolDir, "edge", "a", quiet(), spool.Options{Now: func() time.Time { return at }})
	must(t, err)
	must(t, w.WriteLine(edge.Stream, line(t, at, iphone, beaconOf("journeyCCCC", "vsl", 0, map[string]any{"k": "view"}))))
	w.Close()
	archDir := filepath.Join(dir, "archive")
	store, err := archive.Open("file://" + archDir)
	must(t, err)
	l := New(Config{DB: db, Store: store, Spool: spoolDir, Log: quiet(), Now: func() time.Time { return at }})
	_, err = l.Archive(ctx)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(archDir, "events/2026/09/30/14/edge-a-1410.ndjson.zst"), []byte("changed"), 0o640))
	if _, err := l.LoadOne(ctx); err == nil {
		t.Fatal("a changed file loaded")
	}
	var q bool
	must(t, db.QueryRow(ctx, `SELECT quarantined_at IS NOT NULL FROM funnels.raw_file`).Scan(&q))
	if !q {
		t.Error("not quarantined")
	}
	if more, err := l.LoadOne(ctx); more || err != nil {
		t.Errorf("quarantined file picked again: %v %v", more, err)
	}
}
