package load

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/tracks/archive"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
	"github.com/Raposa-Industries/adhunters/tracks/internal/rawtest"
	"github.com/Raposa-Industries/adhunters/tracks/internal/testdb"
	"github.com/Raposa-Industries/adhunters/tracks/ship"
)

const taboolaURL = "https://trc.taboola.com/foxnews-foxnews/trc/3/json?llvl=2&pubit=i&t=1&data=%7B%22ii%22%3A%22/lifestyle%22%2C%22it%22%3A%22text%22%2C%22u%22%3A%22https%3A//www.foxnews.com/lifestyle%22%2C%22uad%22%3A%7B%22mobile%22%3Afalse%2C%22platform%22%3A%22Windows%22%7D%2C%22r%22%3A%5B%5D%7D"

var day = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)

type bench struct {
	t      *testing.T
	ctx    context.Context
	db     *pgxpool.Pool
	spool  string
	store  archive.Store
	now    time.Time
	loader *Loader
}

func newBench(t *testing.T) *bench {
	b := &bench{t: t, ctx: context.Background(), db: testdb.New(t), spool: t.TempDir(), store: &archive.Dir{Root: t.TempDir()}}
	b.loader = New(b.db, b.store, rawtest.Quiet(), Config{Now: func() time.Time { return b.now }}, NewMetrics(prometheus.NewRegistry()))
	return b
}

func taboolaRec(t *testing.T, at time.Time, device string) *spool.Record {
	body, err := os.ReadFile("../parse/testdata/taboola_feed.js")
	if err != nil {
		t.Fatal(err)
	}
	r := &spool.Record{ID: ulid.Make().String(), At: at, Network: "taboola", Publisher: "Fox News", Device: device,
		Line: "dc-1", URL: taboolaURL, Status: 200, LatencyMS: 300}
	r.SetBody(body)
	return r
}

func newsbreakRec(t *testing.T, at time.Time) *spool.Record {
	body, err := os.ReadFile("../parse/testdata/newsbreak_auction.json")
	if err != nil {
		t.Fatal(err)
	}
	r := &spool.Record{ID: ulid.Make().String(), At: at, Network: "newsbreak", Publisher: "NewsBreak", Device: "phone",
		Line: "dc-2", Status: 200,
		RequestBody: `{"site":{"page":"https://www.newsbreak.com/","publisher":{"domain":"newsbreak.com"}}}`}
	r.SetBody(body)
	return r
}

func failedRec(at time.Time) *spool.Record {
	return &spool.Record{ID: ulid.Make().String(), At: at, Network: "taboola", Publisher: "Fox News", Device: "phone",
		Line: "dc-1", URL: taboolaURL, Error: "context deadline exceeded"}
}

// capture writes recs in minute m, and the shipper archives them.
func (b *bench) capture(m time.Time, recs ...*spool.Record) {
	b.t.Helper()
	rawtest.Seal(b.t, b.spool, "a", m, recs...)
	c := ship.Config{Spool: b.spool, Store: b.store, DB: b.db, Box: "test", Log: rawtest.Quiet()}
	if _, err := ship.Pass(b.ctx, b.ctx, c); err != nil {
		b.t.Fatal(err)
	}
}

func (b *bench) loadAll() int {
	b.t.Helper()
	n := 0
	for {
		ok, err := b.loader.LoadNext(b.ctx)
		if err != nil {
			b.t.Fatal(err)
		}
		if !ok {
			return n
		}
		n++
	}
}

func (b *bench) int(q string, args ...any) int64 {
	b.t.Helper()
	var n int64
	if err := b.db.QueryRow(b.ctx, q, args...).Scan(&n); err != nil {
		b.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestLoadCloseAndReplay(t *testing.T) {
	b := newBench(t)
	h14 := day.Add(14 * time.Hour)
	b.capture(h14.Add(3*time.Minute),
		taboolaRec(t, h14.Add(3*time.Minute), "desktop"),
		taboolaRec(t, h14.Add(3*time.Minute+time.Second), "phone"),
		newsbreakRec(t, h14.Add(3*time.Minute+2*time.Second)),
		failedRec(h14.Add(3*time.Minute+3*time.Second)))

	b.now = h14.Add(20 * time.Minute)
	if n := b.loadAll(); n != 2 {
		t.Fatalf("loaded %d files, want 2 (one per network)", n)
	}
	if got := b.int(`SELECT count(*) FROM tracks.scrape`); got != 4 {
		t.Fatalf("%d scrapes, want 4 (the failed one too)", got)
	}
	// Two Taboola scrapes of 2 ads each, one NewsBreak ad.
	if got := b.int(`SELECT count(*) FROM tracks.sighting`); got != 5 {
		t.Fatalf("%d sightings, want 5", got)
	}
	if got := b.int(`SELECT count(*) FROM tracks.ad`); got != 3 {
		t.Fatalf("%d ads, want 3", got)
	}
	if got := b.int(`SELECT count(*) FROM tracks.auction`); got != 4 {
		t.Fatalf("%d auctions, want 4", got)
	}
	if got := b.int(`SELECT count(*) FROM tracks.raw_file WHERE scrapes = rows AND loaded_at IS NOT NULL`); got != 2 {
		t.Fatalf("books do not balance for %d files", 2-got)
	}

	// Before the hour is ripe nothing closes, but the open hour shows it.
	if closed, err := b.loader.CloseReady(b.ctx); err != nil || len(closed) != 0 {
		t.Fatalf("closed %v early, err %v", closed, err)
	}
	if err := b.loader.RefreshOpen(b.ctx); err != nil {
		t.Fatal(err)
	}
	if got := b.int(`SELECT sum(sightings) FROM tracks_api.ad_hourly_v1 WHERE NOT closed`); got != 5 {
		t.Fatalf("open hour has %d sightings, want 5", got)
	}

	b.now = h14.Add(65 * time.Minute)
	closed, err := b.loader.CloseReady(b.ctx)
	if err != nil || len(closed) != 1 || !closed[0].Equal(h14) {
		t.Fatalf("closed %v, err %v", closed, err)
	}
	if got := b.int(`SELECT sum(sightings) FROM tracks_api.ad_hourly_v1 WHERE hour = $1`, h14); got != 5 {
		t.Fatalf("closed hour counts %d sightings, want 5 (and never the open copy too)", got)
	}
	if got := b.int(`SELECT count(*) FROM tracks.ad_hourly_open`); got != 0 {
		t.Fatalf("the open copy of a closed hour stayed: %d rows", got)
	}
	if got := b.int(`SELECT scrapes FROM tracks.publisher_hourly p JOIN tracks.publisher u ON u.id = p.publisher_id
		WHERE u.name = 'Fox News' AND p.device_id = (SELECT id FROM tracks.device WHERE code = 'phone')`); got != 2 {
		t.Fatalf("Fox News on phone: %d scrapes, want 2 (one failed)", got)
	}
	if got := b.int(`SELECT sum(sightings) FROM tracks_api.ad_daily_v1 WHERE day = $1`, day); got != 5 {
		t.Fatalf("day counts %d sightings, want 5", got)
	}
	if got := b.int(`SELECT count(*) FROM tracks_api.creative_campaign_daily_v1 WHERE day = $1`, day); got != 3 {
		t.Fatalf("%d creative campaigns, want 3", got)
	}
	st, err := ReadStatus(b.ctx, b.db, false)
	if err != nil || st.LastClosedHour == nil || !st.LastClosedHour.Equal(h14) || st.Pending != 0 {
		t.Fatalf("status %+v, err %v", st, err)
	}

	// A replay loads the same files again and changes nothing.
	r, err := Replay(b.ctx, b.db, h14, h14.Add(time.Hour), "")
	if err != nil || r.Files != 2 {
		t.Fatalf("replay marked %d files, err %v", r.Files, err)
	}
	b.loadAll()
	if got := b.int(`SELECT count(*) FROM tracks.sighting`); got != 5 {
		t.Fatalf("after replay %d sightings, want 5", got)
	}
	if got := b.int(`SELECT count(*) FROM tracks.scrape`); got != 4 {
		t.Fatalf("after replay %d scrapes, want 4", got)
	}
	if closed, _ := b.loader.CloseReady(b.ctx); len(closed) != 1 {
		t.Fatalf("the replayed hour did not close again: %v", closed)
	}
	if got := b.int(`SELECT sum(sightings) FROM tracks.ad_hourly WHERE hour = $1`, h14); got != 5 {
		t.Fatalf("after replay the hour counts %d, want 5", got)
	}

	// A file that comes late makes its hour close again with it.
	b.capture(h14.Add(59*time.Minute), taboolaRec(t, h14.Add(59*time.Minute), "desktop"))
	b.loadAll()
	if closed, _ := b.loader.CloseReady(b.ctx); len(closed) != 1 {
		t.Fatalf("the late file's hour did not close again: %v", closed)
	}
	if got := b.int(`SELECT sum(sightings) FROM tracks.ad_hourly WHERE hour = $1`, h14); got != 7 {
		t.Fatalf("with the late file the hour counts %d, want 7", got)
	}
	st, err = ReadStatus(b.ctx, b.db, true)
	if err != nil || len(st.UnbalancedFiles)+len(st.UnbalancedHours) != 0 {
		t.Fatalf("books: %+v %v", st, err)
	}
}

func TestEmptyHoursCloseToo(t *testing.T) {
	b := newBench(t)
	h := day.Add(10 * time.Hour)
	b.capture(h.Add(time.Minute), taboolaRec(t, h.Add(time.Minute), "desktop"))
	b.now = h.Add(4*time.Hour + 4*time.Minute)
	b.loadAll()
	closed, err := b.loader.CloseReady(b.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 10:00 had data; 11:00 and 12:00 had none but are over; 13:00 closes at 14:05.
	if len(closed) != 3 || !closed[2].Equal(h.Add(2*time.Hour)) {
		t.Fatalf("closed %v", closed)
	}
}

func TestLiveLinksAreHandedOutOnce(t *testing.T) {
	b := newBench(t)
	m := time.Now().UTC().Truncate(time.Minute)
	b.capture(m, taboolaRec(t, m, "desktop"))
	b.now = m.Add(2 * time.Minute)
	b.loadAll()
	if got := b.int(`SELECT count(*) FROM tracks_api.live_link_v1`); got != 2 {
		t.Fatalf("%d live links, want 2", got)
	}
	take := func(host, campaign string) (string, bool) {
		var url string
		err := b.db.QueryRow(b.ctx, `SELECT url FROM tracks_api.take_live_link_v1($1, $2, 'desktop', false)`, host, campaign).Scan(&url)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false
		}
		if err != nil {
			t.Fatal(err)
		}
		return url, true
	}
	if _, ok := take("www.theconsumerguide.co", "999"); ok {
		t.Fatal("handed out a link of another campaign")
	}
	url, ok := take("www.theconsumerguide.co", "50347138")
	if !ok || url == "" {
		t.Fatal("no link for the campaign asked for")
	}
	if url != "https://theconsumerguide.co/senior/tb/?camp_id=50347138&cid=tbl4LahDwAGYCiC6ln3fftswH4wDskjBit38WAXqbBwg&pid=1151445&site_id=1234567&tblci=GiDx#tblciGiDx" {
		t.Fatalf("the live link lost its click values: %s", url)
	}
	if _, ok := take("theconsumerguide.co", ""); ok {
		t.Fatal("a link was handed out twice")
	}

	// An old file gives no live links.
	b2 := newBench(t)
	b2.capture(m, taboolaRec(t, m, "desktop"))
	b2.now = m.Add(20 * time.Minute)
	b2.loadAll()
	if got := b2.int(`SELECT count(*) FROM tracks.live_link`); got != 0 {
		t.Fatalf("%d live links from a file 20 minutes old", got)
	}
}

func TestKeepDropsFinalDaysAndReplayBringsThemBack(t *testing.T) {
	b := newBench(t)
	h := day.Add(22 * time.Hour)
	b.capture(h.Add(5*time.Minute), taboolaRec(t, h.Add(5*time.Minute), "desktop"))
	b.now = day.Add(24*time.Hour + 2*time.Hour) // the day is over and ripe
	b.loadAll()
	if _, err := b.loader.CloseReady(b.ctx); err != nil {
		t.Fatal(err)
	}
	daily := b.int(`SELECT sum(sightings) FROM tracks.ad_daily WHERE day = $1`, day)
	if daily != 2 {
		t.Fatalf("day counts %d, want 2", daily)
	}

	b.now = day.AddDate(0, 0, 5)
	if err := b.loader.Keep(b.ctx); err != nil {
		t.Fatal(err)
	}
	if got := b.int(`SELECT count(*) FROM pg_class WHERE relname = 'sighting_20260920'`); got != 0 {
		t.Fatal("sightings past 3 days kept")
	}
	if got := b.int(`SELECT count(*) FROM pg_class WHERE relname = 'scrape_20260920'`); got != 1 {
		t.Fatal("scrapes dropped before 35 days")
	}
	if got := b.int(`SELECT sum(sightings) FROM tracks.ad_daily WHERE day = $1`, day); got != daily {
		t.Fatal("day counts changed when sightings were dropped")
	}

	// A late file for that day waits for a replay instead of half-counting it.
	b.capture(h.Add(30*time.Minute), taboolaRec(t, h.Add(30*time.Minute), "phone"))
	if ok, err := b.loader.LoadNext(b.ctx); ok || err == nil {
		t.Fatalf("a late file for a dropped day loaded: %v %v", ok, err)
	}
	if got := b.int(`SELECT count(*) FROM tracks.raw_file WHERE quarantined_at IS NOT NULL`); got != 1 {
		t.Fatal("the late file was not set aside")
	}

	// Replaying one hour of that day brings back the whole day, late file too.
	r, err := Replay(b.ctx, b.db, h, h.Add(time.Hour), "taboola")
	if err != nil || len(r.WholeDays) != 1 || r.Files != 2 {
		t.Fatalf("replay %+v, err %v", r, err)
	}
	b.loadAll()
	if _, err := b.loader.CloseReady(b.ctx); err != nil {
		t.Fatal(err)
	}
	if got := b.int(`SELECT sum(sightings) FROM tracks.ad_daily WHERE day = $1`, day); got != 4 {
		t.Fatalf("after replay the day counts %d, want 4", got)
	}
}

func TestMissingFileIsQuarantined(t *testing.T) {
	b := newBench(t)
	if _, err := b.db.Exec(b.ctx, `INSERT INTO tracks.raw_file (key, network, instance, minute, rows, bytes, sha256, shipped_by)
		VALUES ('taboola/2026/09/20/10/capture-a-1000.ndjson.zst', 'taboola', 'a', $1, 1, 1, 'x', 'test')`, day.Add(10*time.Hour)); err != nil {
		t.Fatal(err)
	}
	b.now = day.Add(11 * time.Hour)
	if _, err := b.loader.LoadNext(b.ctx); err == nil {
		t.Fatal("a file missing from the archive loaded")
	}
	if got := b.int(`SELECT count(*) FROM tracks.raw_file WHERE quarantined_at IS NOT NULL AND last_error LIKE 'not in the archive%'`); got != 1 {
		t.Fatal("not quarantined")
	}
	if ok, err := b.loader.LoadNext(b.ctx); ok || err != nil {
		t.Fatalf("the loader did not move on: %v %v", ok, err)
	}
}
