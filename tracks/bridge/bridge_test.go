package bridge

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/shared/archive"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
	"github.com/Raposa-Industries/adhunters/tracks/internal/collectortest"
	"github.com/Raposa-Industries/adhunters/tracks/internal/rawtest"
	"github.com/Raposa-Industries/adhunters/tracks/internal/testdb"
	"github.com/Raposa-Industries/adhunters/tracks/ship"
)

const taboolaURL = "https://trc.taboola.com/foxnews-foxnews/trc/3/json?llvl=2&pubit=i&t=1&data=%7B%22ii%22%3A%22/lifestyle%22%2C%22it%22%3A%22text%22%2C%22u%22%3A%22https%3A//www.foxnews.com/lifestyle%22%2C%22uad%22%3A%7B%22mobile%22%3Afalse%2C%22platform%22%3A%22Windows%22%7D%2C%22r%22%3A%5B%5D%7D"

var h14 = time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)

type bench struct {
	t      *testing.T
	ctx    context.Context
	db     *pgxpool.Pool
	old    *pgxpool.Pool
	spool  string
	store  archive.Store
	bridge *Bridge
}

func newBench(t *testing.T, from time.Time) *bench {
	b := &bench{t: t, ctx: context.Background(), db: testdb.New(t), old: collectortest.New(t), spool: t.TempDir(),
		store: &archive.Dir{Root: t.TempDir()}}
	b.bridge = New(b.db, b.old, b.store, rawtest.Quiet(), Config{From: from, Version: "test"}, NewMetrics(prometheus.NewRegistry()))
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

func (b *bench) bridgeAll() int {
	b.t.Helper()
	n := 0
	for {
		ok, err := b.bridge.Next(b.ctx)
		if err != nil {
			b.t.Fatal(err)
		}
		if !ok {
			return n
		}
		n++
	}
}

func one(t *testing.T, db *pgxpool.Pool, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestBridgeWritesLikeTheCollector(t *testing.T) {
	b := newBench(t, h14)
	// Before the starting minute: the collector's own scrapes, never bridged.
	b.capture(h14.Add(-time.Minute), taboolaRec(t, h14.Add(-time.Minute), "desktop"))
	desktop := taboolaRec(t, h14.Add(3*time.Minute), "desktop")
	b.capture(h14.Add(3*time.Minute),
		desktop,
		taboolaRec(t, h14.Add(3*time.Minute+time.Second), "phone"),
		newsbreakRec(t, h14.Add(3*time.Minute+2*time.Second)),
		failedRec(h14.Add(3*time.Minute+3*time.Second)))

	if n := b.bridgeAll(); n != 2 {
		t.Fatalf("bridged %d files, want 2 (one per network, none before the start)", n)
	}
	old := func(q string, args ...any) int64 { return one(t, b.old, q, args...) }
	// The collector stored answered scrapes only: the failed one stays out.
	if got := old(`SELECT count(*) FROM spy.scrape`); got != 3 {
		t.Fatalf("%d scrapes, want 3", got)
	}
	// Two Taboola scrapes of 2 ads each, one NewsBreak ad (as the loader counts them).
	if got := old(`SELECT count(*) FROM spy.sighting`); got != 5 {
		t.Fatalf("%d sightings, want 5", got)
	}
	if got := old(`SELECT count(*) FROM spy.ad`); got != 3 {
		t.Fatalf("%d ads, want 3", got)
	}
	if got := old(`SELECT sum(sightings) FROM spy.ad_hourly WHERE hour = $1`, h14); got != 5 {
		t.Fatalf("hourly counts %d, want 5", got)
	}
	if got := old(`SELECT sum(sightings) FROM spy.ad_daily`); got != 5 {
		t.Fatalf("daily counts %d, want 5", got)
	}
	if got := old(`SELECT sum(scrapes) FROM spy.publisher_hourly`); got != 3 {
		t.Fatalf("publisher_hourly counts %d scrapes, want 3", got)
	}
	if got := old(`SELECT count(*) FROM spy.scrape WHERE batch_uid = $1 AND worker_node = 'tracks:a'`, BatchUID(desktop.ID)); got != 1 {
		t.Fatal("the scrape is not keyed by its capture record")
	}
	if got := old(`SELECT count(*) FROM spy.walk_queue WHERE click_url <> '' AND referer <> ''`); got != 3 {
		t.Fatalf("%d links for the walker, want 3 (one per ad)", got)
	}
	if got := one(t, b.db, `SELECT count(*) FROM tracks.bridge_file WHERE bridged_at IS NOT NULL AND scrapes > 0`); got != 2 {
		t.Fatalf("%d files recorded as bridged, want 2", got)
	}

	// Writing the same files again (redo, or a restart mid-file) stores nothing twice.
	n, err := Redo(b.ctx, b.db, h14, h14.Add(time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("redo forgot %d files, err %v", n, err)
	}
	b.bridge.rescan = time.Time{}
	b.bridge.w = NewSpyWriter(b.old) // a fresh start: empty caches
	if n := b.bridgeAll(); n != 2 {
		t.Fatalf("redo bridged %d files, want 2", n)
	}
	if got := old(`SELECT sum(sightings) FROM spy.ad_hourly`); got != 5 {
		t.Fatalf("after redo the hourly counts are %d, want 5", got)
	}
	if got := old(`SELECT count(*) FROM spy.scrape`); got != 3 {
		t.Fatalf("after redo %d scrapes, want 3", got)
	}

	s, err := ReadStatus(b.ctx, b.db, h14, 0)
	if err != nil || s.Pending != 0 || s.LastBridgedMinute == nil || !s.LastBridgedMinute.Equal(h14.Add(3*time.Minute)) {
		t.Fatalf("status %+v, err %v", s, err)
	}
}

func TestBridgeQuarantinesABadFile(t *testing.T) {
	b := newBench(t, h14)
	b.capture(h14, taboolaRec(t, h14, "desktop"))
	if _, err := b.db.Exec(b.ctx, `UPDATE tracks.raw_file SET sha256 = 'x'`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.bridge.Next(b.ctx); err == nil {
		t.Fatal("a file whose checksum does not match was written")
	}
	if got := one(t, b.db, `SELECT count(*) FROM tracks.bridge_file WHERE quarantined_at IS NOT NULL`); got != 1 {
		t.Fatal("the bad file was not quarantined")
	}
	if ok, err := b.bridge.Next(b.ctx); ok || err != nil {
		t.Fatalf("the quarantined file was taken again: %v %v", ok, err)
	}
}

func TestBatchUIDIsStable(t *testing.T) {
	id := ulid.Make().String()
	if BatchUID(id) != BatchUID(id) || BatchUID(id) == BatchUID(ulid.Make().String()) {
		t.Fatal("batch ids must be one per record")
	}
	if BatchUID("not-a-ulid") != BatchUID("not-a-ulid") {
		t.Fatal("a record id that is not a ULID must still map to one batch id")
	}
}
