package load

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/shared/archive"
	"github.com/Raposa-Industries/adhunters/tracks/internal/rawtest"
	"github.com/Raposa-Industries/adhunters/tracks/internal/testdb"
)

// hourBench is a database with three closed days of hourly counts in August
// 2026 and a loader whose clock reads 2026-10-15.
type hourBench struct {
	t      *testing.T
	ctx    context.Context
	l      *Loader
	month  time.Time
	days   []time.Time
	before string // a fingerprint of every hourly row, taken before anything moved
}

func newHourBench(t *testing.T) *hourBench {
	db := testdb.New(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	l := New(db, &archive.Dir{Root: t.TempDir()}, rawtest.Quiet(), Config{Now: func() time.Time { return now }},
		NewMetrics(prometheus.NewRegistry()))
	b := &hourBench{t: t, ctx: context.Background(), l: l, month: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}
	for d := 10; d <= 12; d++ {
		b.days = append(b.days, time.Date(2026, 8, d, 0, 0, 0, 0, time.UTC))
	}
	b.exec(`SELECT tracks.ensure_month_partitions('2026-08-01', '2026-08-01')`)
	// Every hour of the three days: a few ads, nulls where the tables allow
	// them, and a daily row per ad that adds up to its hours.
	b.exec(`
		INSERT INTO tracks.ad_hourly
		SELECT h, ad, 7, (ad % 2)::smallint, ad * 3 + extract(hour FROM h)::int, ad + 1, ad * 10,
		       CASE WHEN ad = 3 THEN NULL ELSE 1 END, CASE WHEN ad = 3 THEN NULL ELSE 9 END,
		       h + interval '1 minute 2.345678 seconds', h + interval '59 minutes'
		FROM generate_series('2026-08-10 00:00Z'::timestamptz, '2026-08-12 23:00Z', interval '1 hour') h,
		     generate_series(1, 4) ad`)
	b.exec(`
		INSERT INTO tracks.ad_account_brand_hourly
		SELECT hour, ad_id, CASE WHEN ad_id = 2 THEN NULL ELSE ad_id + 100 END, CASE WHEN ad_id = 4 THEN NULL ELSE 50 END,
		       publisher_id, device_id, sightings
		FROM tracks.ad_hourly`)
	b.exec(`
		INSERT INTO tracks.ad_daily
		SELECT (hour AT TIME ZONE 'UTC')::date, ad_id, publisher_id, device_id, 1, sum(sightings), sum(scrapes),
		       sum(feed_position_sum), min(first_seen_at), max(last_seen_at)
		FROM tracks.ad_hourly GROUP BY 1, 2, 3, 4`)
	b.exec(`
		INSERT INTO tracks.hour_state (hour, dirty, closed_at, closes)
		SELECT h, FALSE, now() - interval '1 day', 1
		FROM generate_series('2026-08-10 00:00Z'::timestamptz, '2026-08-12 23:00Z', interval '1 hour') h`)
	b.before = b.rowsPrint()
	return b
}

func (b *hourBench) exec(sql string, args ...any) {
	b.t.Helper()
	if _, err := b.l.db.Exec(b.ctx, sql, args...); err != nil {
		b.t.Fatalf("%.80s: %v", sql, err)
	}
}

func (b *hourBench) int(sql string, args ...any) int64 {
	b.t.Helper()
	var n int64
	if err := b.l.db.QueryRow(b.ctx, sql, args...).Scan(&n); err != nil {
		b.t.Fatalf("%.80s: %v", sql, err)
	}
	return n
}

// rowsPrint is every August hourly row as text, in order.
func (b *hourBench) rowsPrint() string {
	b.t.Helper()
	var s string
	if err := b.l.db.QueryRow(b.ctx, `
		SELECT COALESCE((SELECT string_agg(h::text, ';' ORDER BY hour, ad_id, publisher_id, device_id) FROM tracks.ad_hourly h
		                 WHERE hour >= '2026-08-01Z' AND hour < '2026-09-01Z'), '') || '|' ||
		       COALESCE((SELECT string_agg(h::text, ';' ORDER BY hour, ad_id, account_id, brand_id) FROM tracks.ad_account_brand_hourly h
		                 WHERE hour >= '2026-08-01Z' AND hour < '2026-09-01Z'), '')`).Scan(&s); err != nil {
		b.t.Fatal(err)
	}
	return s
}

func (b *hourBench) writeAll() int {
	b.t.Helper()
	n := 0
	for {
		wrote, err := b.l.WriteHourFiles(b.ctx)
		if err != nil {
			b.t.Fatal(err)
		}
		if !wrote {
			return n
		}
		n++
	}
}

func (b *hourBench) states(bringBack bool) string {
	b.t.Helper()
	rows, err := b.l.db.Query(b.ctx, `SELECT state FROM tracks_api.hourly_days_v1('2026-08-10Z', '2026-08-13Z', $1)`, bringBack)
	if err != nil {
		b.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			b.t.Fatal(err)
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func (b *hourBench) partitions() int64 {
	return b.int(`SELECT count(*) FROM pg_class WHERE relname IN ('ad_hourly_202608', 'ad_account_brand_hourly_202608')`)
}

func TestHourFilesDropAndBringBack(t *testing.T) {
	b := newHourBench(t)

	if n := b.writeAll(); n != 3 {
		t.Fatalf("wrote %d days, want 3", n)
	}
	if n := b.int(`SELECT count(*) FROM tracks.hour_file`); n != 6 {
		t.Fatalf("%d hour files recorded, want 6 (2 tables x 3 days)", n)
	}
	if n := b.int(`SELECT sum(rows) FROM tracks.hour_file WHERE tbl = 'ad_hourly'`); n != 3*24*4 {
		t.Fatalf("ad_hourly files hold %d rows, want %d", n, 3*24*4)
	}
	if n := b.writeAll(); n != 0 {
		t.Fatalf("wrote %d days again with nothing changed", n)
	}

	c, err := b.l.CheckMonth(b.ctx, b.month, 35)
	if err != nil {
		t.Fatal(err)
	}
	if !c.OK() || len(c.Days) != 6 || len(c.Daily) != 3 {
		t.Fatalf("check: ok %v, %d day rows, %d daily: %v", c.OK(), len(c.Days), len(c.Daily), c.Problems)
	}
	for _, d := range c.Daily {
		if d.HourlySightings != d.DailySightings {
			t.Errorf("%s: hourly %d, daily %d", d.Day.Format("2006-01-02"), d.HourlySightings, d.DailySightings)
		}
	}
	if got := b.states(false); got != "database,database,database" {
		t.Fatalf("before the drop: %s", got)
	}

	if _, err := b.l.DropMonth(b.ctx, b.month, 35); err != nil {
		t.Fatal(err)
	}
	if n := b.partitions(); n != 0 {
		t.Fatalf("%d August partitions left after the drop", n)
	}
	if n := b.int(`SELECT sum(sightings) FROM tracks.ad_daily`); n == 0 {
		t.Fatal("the daily counts went with the hourly ones")
	}
	if got := b.states(false); got != "archive,archive,archive" {
		t.Fatalf("after the drop: %s", got)
	}
	if n := b.int(`SELECT hourly_rows FROM tracks.archived_month WHERE month = '2026-08-01'`); n != 3*24*4 {
		t.Fatalf("archived_month says %d rows dropped", n)
	}

	// A page asks for the days back: they come back value for value.
	if got := b.states(true); got != "coming,coming,coming" {
		t.Fatalf("after asking: %s", got)
	}
	for i := 0; i < 3; i++ {
		if ok, err := b.l.BringBack(b.ctx); err != nil || !ok {
			t.Fatalf("bring back %d: %v %v", i, ok, err)
		}
	}
	if got := b.states(false); got != "database,database,database" {
		t.Fatalf("after bringing back: %s", got)
	}
	if got := b.rowsPrint(); got != b.before {
		t.Fatalf("the hours brought back differ from the ones dropped:\n%.300s\nwant\n%.300s", got, b.before)
	}

	// Kept: nothing goes. Once nobody keeps them, they go again.
	if err := b.l.KeepArchived(b.ctx); err != nil {
		t.Fatal(err)
	}
	if n := b.partitions(); n != 2 {
		t.Fatalf("days still kept were dropped (%d partitions)", n)
	}
	b.exec(`UPDATE tracks.hour_bring_back SET keep_until = now() - interval '1 second'`)
	if err := b.l.KeepArchived(b.ctx); err != nil {
		t.Fatal(err)
	}
	if n := b.partitions(); n != 0 {
		t.Fatalf("%d partitions left after the brought-back days expired", n)
	}
	if got := b.states(false); got != "archive,archive,archive" {
		t.Fatalf("after expiry: %s", got)
	}
	if n := b.int(`SELECT count(*) FROM tracks.hour_bring_back`); n != 0 {
		t.Fatalf("%d bring-back rows left", n)
	}
}

func TestDropMonthRefuses(t *testing.T) {
	b := newHourBench(t)

	// No hour files yet.
	if _, err := b.l.DropMonth(b.ctx, b.month, 35); err == nil || !strings.Contains(err.Error(), "no hour file") {
		t.Fatalf("dropped without hour files: %v", err)
	}
	b.writeAll()

	// Not past its keep time.
	if _, err := b.l.DropMonth(b.ctx, b.month, 90); err == nil || !strings.Contains(err.Error(), "until 2026-11-30") {
		t.Fatalf("dropped inside the keep time: %v", err)
	}

	// One value changed after the files were written: the check sees it.
	b.exec(`UPDATE tracks.ad_hourly SET feed_position_max = 8 WHERE hour = '2026-08-11 05:00Z' AND ad_id = 1`)
	c, err := b.l.DropMonth(b.ctx, b.month, 35)
	if err == nil {
		t.Fatal("dropped a month whose rows differ from its hour files")
	}
	var differ int
	for _, d := range c.Days {
		differ += d.HoursDiffer
	}
	if differ != 1 {
		t.Fatalf("%d hours differ, want 1: %v", differ, c.Problems)
	}
	if n := b.partitions(); n != 2 {
		t.Fatalf("a refused drop left %d partitions", n)
	}
	b.exec(`UPDATE tracks.ad_hourly SET feed_position_max = 9 WHERE hour = '2026-08-11 05:00Z' AND ad_id = 1`)

	// A dirty hour: the day is not final.
	b.exec(`UPDATE tracks.hour_state SET dirty = TRUE WHERE hour = '2026-08-12 03:00Z'`)
	if _, err := b.l.DropMonth(b.ctx, b.month, 35); err == nil || !strings.Contains(err.Error(), "not final") {
		t.Fatalf("dropped a month with a dirty hour: %v", err)
	}
}

// A replay closes a day of an archived month again: its new counts stay in
// the database until they are written to a new hour file, then go again.
func TestReplayIntoArchivedMonth(t *testing.T) {
	b := newHourBench(t)
	b.writeAll()
	if _, err := b.l.DropMonth(b.ctx, b.month, 35); err != nil {
		t.Fatal(err)
	}

	// What close_hour does for each hour of the replayed day.
	b.exec(`SELECT tracks.ensure_month_partitions('2026-08-11', '2026-08-11')`)
	b.exec(`
		INSERT INTO tracks.ad_hourly
		SELECT h, 1, 7, 0, 5, 1, 0, NULL, NULL, h, h + interval '1 minute'
		FROM generate_series('2026-08-11 00:00Z'::timestamptz, '2026-08-11 23:00Z', interval '1 hour') h`)
	b.exec(`UPDATE tracks.hour_state SET closed_at = now(), closes = closes + 1
	        WHERE hour >= '2026-08-11Z' AND hour < '2026-08-12Z'`)
	if got := b.states(false); got != "archive,database,archive" {
		t.Fatalf("after the replay: %s", got)
	}
	// Asking for the replayed day brings nothing over its newer counts.
	b.states(true)
	for {
		ok, err := b.l.BringBack(b.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
	}
	if n := b.int(`SELECT count(*) FROM tracks.ad_hourly WHERE hour >= '2026-08-11Z' AND hour < '2026-08-12Z'`); n != 24 {
		t.Fatalf("the replayed day holds %d rows, want its 24 new ones", n)
	}
	b.exec(`UPDATE tracks.hour_bring_back SET keep_until = now() - interval '1 second'`)

	// Not written yet: it stays.
	if err := b.l.KeepArchived(b.ctx); err != nil {
		t.Fatal(err)
	}
	if b.partitions() == 0 {
		t.Fatal("dropped the replayed day before its new hour file was written")
	}
	if n := b.writeAll(); n != 1 {
		t.Fatalf("wrote %d days, want the replayed one", n)
	}
	if n := b.int(`SELECT max(version) FROM tracks.hour_file WHERE day = '2026-08-11'`); n != 2 {
		t.Fatalf("the replayed day's hour file is version %d, want 2", n)
	}
	if err := b.l.KeepArchived(b.ctx); err != nil {
		t.Fatal(err)
	}
	if n := b.partitions(); n != 0 {
		t.Fatalf("%d partitions left once the replayed day was written", n)
	}
	if got := b.states(false); got != "archive,archive,archive" {
		t.Fatalf("after the second drop: %s", got)
	}
	// Bringing it back now gives the replay's counts.
	b.states(true)
	for {
		ok, err := b.l.BringBack(b.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
	}
	if n := b.int(`SELECT sum(sightings) FROM tracks.ad_hourly WHERE hour >= '2026-08-11Z' AND hour < '2026-08-12Z'`); n != 24*5 {
		t.Fatalf("the replayed day came back with %d sightings, want %d", n, 24*5)
	}
}
