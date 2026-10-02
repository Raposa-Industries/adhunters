package numbers

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/spy/internal/testdb"
)

// now is a Tuesday, 12:30 UTC.
var now = time.Date(2026, 10, 20, 12, 30, 0, 0, time.UTC)

type bench struct {
	t   *testing.T
	ctx context.Context
	db  *pgxpool.Pool
	r   *Runner
}

func newBench(t *testing.T) *bench {
	db := testdb.New(t)
	b := &bench{t: t, ctx: context.Background(), db: db}
	b.r = New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Now: func() time.Time { return now }}, nil)
	b.exec(`INSERT INTO tracks_api.publisher_v1 (id, name) VALUES (1, 'Fox News'), (2, 'MSN')`)
	b.exec(`INSERT INTO spy.operator (id, name, display_name) VALUES (7, 'today55 · OP7', 'today55')`)
	return b
}

func (b *bench) exec(sql string, args ...any) {
	b.t.Helper()
	if _, err := b.db.Exec(b.ctx, sql, args...); err != nil {
		b.t.Fatalf("%v\n%s", err, sql)
	}
}

func (b *bench) float(sql string, args ...any) float64 {
	b.t.Helper()
	var v *float64
	if err := b.db.QueryRow(b.ctx, sql, args...).Scan(&v); err != nil {
		b.t.Fatalf("%v\n%s", err, sql)
	}
	if v == nil {
		return math.NaN()
	}
	return *v
}

func (b *bench) text(sql string, args ...any) string {
	b.t.Helper()
	var v *string
	if err := b.db.QueryRow(b.ctx, sql, args...).Scan(&v); err != nil {
		b.t.Fatalf("%v\n%s", err, sql)
	}
	if v == nil {
		return "<nil>"
	}
	return *v
}

// ad adds a creative and its ad, first seen 30 days ago.
func (b *bench) ad(creative, ad, account int, key, headline string) {
	b.exec(`INSERT INTO tracks_api.creative_v1 (id, creative_key, first_seen_at, last_seen_at)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, creative, key, now.AddDate(0, 0, -30), now)
	b.exec(`INSERT INTO tracks_api.ad_v1 (id, creative_id, headline, account_id, first_seen_at, last_seen_at)
		VALUES ($1, $2, $3, NULLIF($4, 0), $5, $6)`, ad, creative, headline, account, now.AddDate(0, 0, -30), now)
}

// hour records one closed (or open) hour: sightings of ads on one publisher
// and device, with that many scrapes.
func (b *bench) hour(h time.Time, pub, dev, scrapes int, closed bool, sightings map[int]int) {
	total := 0
	for ad, s := range sightings {
		total += s
		b.exec(`INSERT INTO tracks_api.ad_hourly_v1 (hour, ad_id, publisher_id, device_id, sightings, first_seen_at, last_seen_at, closed)
			VALUES ($1::timestamptz, $2, $3, $4, $5, $1::timestamptz, $1::timestamptz + interval '59 minutes', $6)`, h, ad, pub, dev, s, closed)
		b.exec(`INSERT INTO tracks_api.ad_account_brand_hourly_v1 (hour, ad_id, account_id, publisher_id, device_id, sightings)
			SELECT $1, $2, account_id, $3, $4, $5 FROM tracks_api.ad_v1 WHERE id = $2`, h, ad, pub, dev, s)
	}
	b.exec(`INSERT INTO tracks_api.scrape_coverage_v2 (hour, publisher_id, device_id, scrapes, sightings, closed)
		VALUES ($1, $2, $3, $4, $5, $6)`, h, pub, dev, scrapes, total, closed)
	if closed {
		b.exec(`INSERT INTO tracks_api.closed_hour_v1 (hour, closed_at) VALUES ($1, '2026-01-01') ON CONFLICT DO NOTHING`, h)
	}
}

// day records one day of an ad on one publisher and device.
func (b *bench) day(d time.Time, ad, pub, dev, sightings int) {
	b.exec(`INSERT INTO tracks_api.ad_daily_v1 (day, ad_id, publisher_id, device_id, creative_id, sightings, scrapes,
			first_seen_at, last_seen_at)
		SELECT $1::date, $2, $3, $4, creative_id, $5, $5, $1::timestamptz + interval '1 hour', $1::timestamptz + interval '20 hours'
		FROM tracks_api.ad_v1 WHERE id = $2`, d, ad, pub, dev, sightings)
	b.exec(`INSERT INTO tracks_api.ad_account_daily_v1 (day, ad_id, account_id, publisher_id, device_id, creative_id,
			sightings, first_seen_at, last_seen_at)
		SELECT $1::date, $2, account_id, $3, $4, creative_id, $5, $1::timestamptz + interval '1 hour', $1::timestamptz + interval '20 hours'
		FROM tracks_api.ad_v1 WHERE id = $2`, d, ad, pub, dev, sightings)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestReadModel(t *testing.T) {
	b := newBench(t)
	today := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	b.ad(10, 100, 500, "ck-10", "Lose belly fat")
	b.ad(10, 101, 501, "ck-10", "Other headline")
	b.ad(11, 110, 500, "fallback-x", "Default title")
	b.exec(`INSERT INTO tracks_api.account_v1 (id, external_id) VALUES (500, 'today55-sc'), (501, 'other-sc')`)
	b.exec(`INSERT INTO spy.account_operator VALUES (500, 7)`)
	b.exec(`INSERT INTO spy.creative_class (creative_id, category_id, vertical_id, confidence, source, rules_hash,
		    input_ad_id, classified_at, needs_model)
		VALUES (10, 'metabolism', 'weight-loss', 0.9, 'ad', '', 0, now(), false)`)
	b.day(today, 100, 1, 2, 30)
	b.day(today.AddDate(0, 0, -1), 101, 2, 1, 20)
	b.day(today.AddDate(0, 0, -10), 100, 1, 1, 50)
	b.day(today, 110, 1, 1, 5)
	b.hour(today.Add(9*time.Hour), 1, 2, 100, true, map[int]int{100: 30})
	b.hour(today.Add(9*time.Hour), 1, 1, 100, true, map[int]int{110: 5})

	if _, err := b.r.ReadModel(b.ctx); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]float64{
		`SELECT sightings_total FROM spy.creative_stats WHERE creative_id = 10`:     100,
		`SELECT sightings_today FROM spy.creative_stats WHERE creative_id = 10`:     30,
		`SELECT sightings_yesterday FROM spy.creative_stats WHERE creative_id = 10`: 20,
		`SELECT sightings_7d FROM spy.creative_stats WHERE creative_id = 10`:        50,
		`SELECT sightings_prev_7d FROM spy.creative_stats WHERE creative_id = 10`:   50,
		`SELECT operator_id FROM spy.creative_stats WHERE creative_id = 10`:         7,
		`SELECT top_ad_id FROM spy.creative_stats WHERE creative_id = 10`:           100,
		`SELECT sightings_total FROM spy.operator_stats WHERE operator_id = 7`:      85,
		`SELECT creatives_count FROM spy.operator_stats WHERE operator_id = 7`:      1, // the junk one is left out
		`SELECT sightings_30d FROM spy.publisher_stats WHERE publisher_id = 1`:      35,
		`SELECT scrapes_30d FROM spy.publisher_stats WHERE publisher_id = 1`:        200,
	} {
		got := b.float(q)
		if !(near(got, want) || math.IsNaN(got) && math.IsNaN(want)) {
			t.Errorf("%s = %v, want %v", q, got, want)
		}
	}
	if got := b.text(`SELECT is_new::text || running::text FROM spy.creative_stats WHERE creative_id = 10`); got != "falsetrue" {
		t.Errorf("new and running: %s, want false and true", got)
	}
	if got := b.float(`SELECT count(*) FROM spy.creative_day WHERE creative_id = 10`); got != 3 {
		t.Errorf("%v creative days, want 3", got)
	}
	if got := b.text(`SELECT vertical FROM spy.creative_stats WHERE creative_id = 10`); got != "weight-loss" {
		t.Errorf("vertical %s", got)
	}
	if got := b.text(`SELECT is_junk::text FROM spy.creative_stats WHERE creative_id = 11`); got != "true" {
		t.Errorf("the fallback creative is not junk: %s", got)
	}
}

func TestLast24Hours(t *testing.T) {
	b := newBench(t)
	b.ad(10, 100, 500, "ck-10", "Lose belly fat")
	b.exec(`INSERT INTO spy.account_operator VALUES (500, 7)`)
	end := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	// 5 an hour in the last 24 hours, 2 an hour in the same hours a week
	// before (the other two usual weeks were not checked).
	for h := end.Add(-24 * time.Hour); h.Before(end); h = h.Add(time.Hour) {
		b.hour(h, 1, 1, 100, true, map[int]int{100: 5})
		b.hour(h.AddDate(0, 0, -7), 1, 1, 100, true, map[int]int{100: 2})
	}
	// Tracks closes every hour, seen or not.
	b.exec(`INSERT INTO tracks_api.closed_hour_v1 (hour, closed_at)
		SELECT g, '2026-01-01' FROM generate_series($1::timestamptz, $2::timestamptz, interval '1 hour') g
		ON CONFLICT DO NOTHING`, end.AddDate(0, 0, -8), end.Add(-time.Hour))
	// The open hour is not in the windows.
	b.hour(end, 1, 1, 50, false, map[int]int{100: 99})

	if n, err := b.r.Recent(b.ctx); err != nil || n != 1 {
		t.Fatalf("wrote %d creatives, err %v", n, err)
	}
	for q, want := range map[string]float64{
		`SELECT extract(epoch FROM window_end) FROM spy.recent_window`:           float64(end.Unix()),
		`SELECT sightings FROM spy.creative_recent WHERE creative_id = 10`:       120,
		`SELECT presence FROM spy.creative_recent WHERE creative_id = 10`:        5, // 120 per 2,400 checks
		`SELECT usual_periods FROM spy.creative_recent WHERE creative_id = 10`:   3,
		`SELECT sightings_usual FROM spy.creative_recent WHERE creative_id = 10`: 48,
		`SELECT momentum FROM spy.creative_recent WHERE creative_id = 10`:        2.5,
		`SELECT momentum FROM spy.operator_recent WHERE operator_id = 7`:         2.5,
		`SELECT sightings FROM spy.operator_recent WHERE operator_id = 7`:        120,
	} {
		if got := b.float(q); !near(got, want) {
			t.Errorf("%s = %v, want %v", q, got, want)
		}
	}
	if got := b.text(`SELECT momentum_word || ' ' || momentum_sure FROM spy.creative_recent`); got != "rising clear" {
		t.Errorf("last 24 hours: %s, want rising clear", got)
	}
	if n, err := b.r.Recent(b.ctx); err != nil || n != 0 {
		t.Fatalf("a second run wrote %d, err %v; the windows were current", n, err)
	}
}

// directionHistory gives three ads the same usual value: 10 sightings in 100
// scrapes on Fox News desktop, every hour from 06:00 to 12:00 of the same
// weekday 1, 2 and 3 weeks back.
func directionHistory(b *bench) {
	for _, ad := range []int{100, 102, 103} {
		b.ad(ad, ad, 0, fmt.Sprintf("ck-%d", ad), "Headline")
	}
	today := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for w := 1; w <= 3; w++ {
		d := today.AddDate(0, 0, -7*w)
		for h := 6; h <= 12; h++ {
			b.hour(d.Add(time.Duration(h)*time.Hour), 1, 1, 100, true, map[int]int{100: 10, 102: 10, 103: 10})
		}
		for _, ad := range []int{100, 102, 103} {
			b.day(d, ad, 1, 1, 70)
		}
	}
}

func TestDirection(t *testing.T) {
	b := newBench(t)
	directionHistory(b)
	today := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for h := 6; h <= 10; h++ {
		b.hour(today.Add(time.Duration(h)*time.Hour), 1, 1, 100, true, map[int]int{100: 10, 103: 10})
	}
	// The window: 11:00 closed and 12:00 open, half scraped. 100 is up 4x,
	// 103 as usual, 102 not seen since 06:00.
	b.hour(today.Add(11*time.Hour), 1, 1, 100, true, map[int]int{100: 40, 103: 10})
	b.hour(today.Add(12*time.Hour), 1, 1, 50, false, map[int]int{100: 20, 103: 5})

	if _, err := b.r.Direction(b.ctx, false); err != nil {
		t.Fatal(err)
	}
	for ad, want := range map[int]string{100: "rising", 102: "stopped", 103: "steady"} {
		if got := b.text(`SELECT direction FROM spy.direction_stats WHERE kind = 'ad' AND key = $1`, strconv.Itoa(ad)); got != want {
			t.Errorf("ad %d is %s, want %s (%s)", ad, got, want,
				b.text(`SELECT reason_text FROM spy.direction_stats WHERE kind = 'ad' AND key = $1`, strconv.Itoa(ad)))
		}
	}
	if got := b.float(`SELECT ratio FROM spy.direction_stats WHERE kind = 'ad' AND key = '100'`); !near(got, 4) {
		t.Errorf("ad 100 ratio %v, want 4", got)
	}
	if got := b.text(`SELECT reason_text FROM spy.direction_stats WHERE kind = 'ad' AND key = '100'`); got !=
		"4.0× usual for Tue 08–09h (0.400 vs 0.100 per scrape). All ads up 1.7×, so a real push." {
		t.Errorf("sentence: %s", got)
	}
	// The hours before the window were counted for good: all of them closed.
	if got := b.float(`SELECT count(*) FROM spy.direction_hour_mark WHERE job = 'seen'`); got != 5 {
		t.Errorf("%v hours marked seen, want 5 (06:00 to 10:00)", got)
	}
	if got := b.float(`SELECT count(*) FROM spy.direction_event`); got != 0 {
		t.Errorf("%v events on the first run, want none", got)
	}

	// 103 jumps in the open hour: a change, so an event.
	b.exec(`UPDATE tracks_api.ad_hourly_v1 SET sightings = 60 WHERE ad_id = 103 AND NOT closed`)
	if _, err := b.r.Direction(b.ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := b.text(`SELECT from_direction || '>' || to_direction FROM spy.direction_event WHERE key = '103'`); got != "steady>rising" {
		t.Errorf("event %s, want steady>rising", got)
	}
	// 102 was not seen in the last 7 days, so it has no size.
	if got := b.float(`SELECT count(*) FROM spy.size_stats WHERE kind = 'ad'`); got != 2 {
		t.Errorf("%v ads sized, want 2", got)
	}
}

// The fill rewrite gives the same usual values as the collector's query.
func TestFillUsualMatchesTheCollector(t *testing.T) {
	b := newBench(t)
	directionHistory(b)
	// Some weeks without scrapes, some publishers that are not usual.
	b.hour(time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC), 2, 2, 30, true, map[int]int{100: 3})
	b.exec(`DELETE FROM tracks_api.scrape_coverage_v2 WHERE hour = '2026-10-06 10:00+00'`)
	if _, err := b.db.Exec(b.ctx, `SELECT spy.direction_rebuild($1)`, now); err != nil {
		t.Fatal(err)
	}
	b.exec(collectorFillUsual)
	for h := 6; h <= 12; h++ {
		slot := time.Date(2026, 10, 20, h, 0, 0, 0, time.UTC)
		b.exec(`SELECT spy.direction_fill_usual($1)`, slot)
		b.exec(`CREATE TEMP TABLE IF NOT EXISTS mine AS SELECT * FROM spy.direction_usual WITH NO DATA`)
		b.exec(`INSERT INTO mine SELECT * FROM spy.direction_usual WHERE hour = $1`, slot)
		b.exec(`SELECT collector_fill_usual($1)`, slot)
	}
	var diff int
	if err := b.db.QueryRow(b.ctx, `
		SELECT count(*) FROM ((SELECT * FROM mine EXCEPT SELECT * FROM spy.direction_usual)
		                      UNION ALL (SELECT * FROM spy.direction_usual EXCEPT SELECT * FROM mine)) x`).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	if n := b.float(`SELECT count(*) FROM spy.direction_usual`); diff != 0 || n == 0 {
		t.Fatalf("%d rows differ of %v", diff, n)
	}
}

// The collector's direction_fill_usual (025), on the Tracks views, writing
// over spy.direction_usual, for comparison only.
const collectorFillUsual = `
CREATE FUNCTION collector_fill_usual(p_hour TIMESTAMPTZ) RETURNS INTEGER LANGUAGE plpgsql AS $$
DECLARE
    cfg JSONB := spy.cfg();
    hours TIMESTAMPTZ[];
    n INTEGER;
BEGIN
    DELETE FROM spy.direction_usual WHERE hour = p_hour;
    hours := ARRAY(SELECT p_hour - make_interval(days => 7 * k) FROM generate_series(1, (cfg->>'usual_weeks')::int) k);
    INSERT INTO spy.direction_usual (hour, kind, key, weeks, sightings, scrapes, sightings_all)
    WITH past AS (SELECT unnest(hours) AS h),
    ah AS (SELECT x.hour AS h, x.ad_id, x.publisher_id, x.device_id, x.sightings FROM tracks_api.ad_hourly_v1 x WHERE x.hour = ANY (hours)),
    per AS (SELECT m.kind, m.key, ah.h, ah.publisher_id, ah.device_id, sum(ah.sightings) AS s
            FROM ah JOIN spy.direction_member m ON m.ad_id = ah.ad_id GROUP BY 1, 2, 3, 4, 5),
    counts AS (SELECT p.kind, p.key, p.h, sum(p.s) AS s_all, COALESCE(sum(p.s) FILTER (WHERE up.kind IS NOT NULL), 0) AS s_usual
               FROM per p LEFT JOIN spy.direction_usual_publisher up
                 ON up.kind = p.kind AND up.key = p.key AND up.publisher_id = p.publisher_id AND up.device_id = p.device_id
               GROUP BY 1, 2, 3),
    ph AS (SELECT hour AS h, publisher_id, device_id, scrapes FROM tracks_api.scrape_coverage_v2 WHERE hour = ANY (hours)),
    scr AS (SELECT up.kind, up.key, ph.h, sum(ph.scrapes) AS sc FROM spy.direction_usual_publisher up
            JOIN ph ON ph.publisher_id = up.publisher_id AND ph.device_id = up.device_id
            WHERE (up.kind, up.key) IN (SELECT kind, key FROM counts) GROUP BY 1, 2, 3),
    weekly AS (SELECT sb.kind, sb.key, p.h, COALESCE(c.s_usual, 0) AS s_usual, COALESCE(c.s_all, 0) AS s_all, sc.sc
               FROM (SELECT DISTINCT kind, key FROM counts) sb CROSS JOIN past p
               JOIN spy.direction_subject s ON s.kind = sb.kind AND s.key = sb.key
               JOIN scr sc ON sc.kind = sb.kind AND sc.key = sb.key AND sc.h = p.h
               LEFT JOIN counts c ON c.kind = sb.kind AND c.key = sb.key AND c.h = p.h
               WHERE s.first_seen_at < p.h + interval '1 hour' AND sc.sc > 0)
    SELECT p_hour, kind, key, count(*), sum(s_usual), sum(sc), sum(s_all) FROM weekly GROUP BY kind, key;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END;
$$;`

// Size counts a subject inside its market only. Operator 7's market is the
// small vertical most of its first ads were in, but nearly all its sightings
// are in the big one: counted whole, it took 250,000% of the small vertical
// and the Direction job failed on numeric field overflow.
func TestSizeInsideTheMarket(t *testing.T) {
	b := newBench(t)
	b.exec(`INSERT INTO spy.operator (id, name, display_name) VALUES (8, 'other · OP8', 'other')`)
	today := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	// ad: creative, operator, vertical, sightings in the last 24 hours
	ads := []struct {
		ad, creative, operator int
		vertical               string
		sightings              int
	}{{100, 10, 7, "small", 1}, {101, 11, 7, "small", 1}, {102, 12, 7, "big", 5000}, {103, 13, 8, "big", 100}}
	hour := map[int]int{}
	for _, a := range ads {
		b.ad(a.creative, a.ad, a.operator, fmt.Sprintf("ck%d", a.creative), fmt.Sprintf("h%d", a.ad))
		ad, creative := strconv.Itoa(a.ad), strconv.Itoa(a.creative)
		b.exec(`INSERT INTO spy.direction_member (ad_id, kind, key) VALUES ($1, 'ad', $2), ($1, 'creative', $3),
			($1, 'operator', $4), ($1, 'vertical', $5), ($1, 'network', '*')`, a.ad, ad, creative, strconv.Itoa(a.operator), a.vertical)
		b.exec(`INSERT INTO spy.direction_subject (kind, key, market_kind, market_key, first_seen_at)
			VALUES ('ad', $1, 'vertical', $3, $4), ('creative', $2, 'vertical', $3, $4)`, ad, creative, a.vertical, now)
		hour[a.ad] = a.sightings
		b.day(today, a.ad, 1, 1, a.sightings)
	}
	b.hour(today.Add(10*time.Hour), 1, 1, 100, true, hour)
	b.exec(`INSERT INTO spy.direction_subject (kind, key, market_kind, market_key, first_seen_at) VALUES
		('operator', '7', 'vertical', 'small', $1), ('operator', '8', 'vertical', 'big', $1),
		('vertical', 'small', 'network', '*', $1), ('vertical', 'big', 'network', '*', $1), ('network', '*', NULL, NULL, $1)`, now)

	if _, err := b.db.Exec(b.ctx, `SELECT spy.refresh_size($1)`, now); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind, key       string
		sightings, rank int
		share           float64
	}{
		{"operator", "7", 2, 1, 100},          // its 2 sightings in the small vertical, all of it
		{"operator", "8", 100, 1, 100.0 / 51}, // the only operator whose market is the big vertical
		{"creative", "12", 5000, 1, 5000.0 / 51},
		{"creative", "13", 100, 2, 100.0 / 51},
		{"vertical", "big", 5100, 1, 5100.0 / 51.02},
	} {
		got := b.text(`SELECT sightings_24h || ' ' || rank_24h || ' ' || share_24h_pct FROM spy.size_stats WHERE kind = $1 AND key = $2`, c.kind, c.key)
		var s, r int
		var share float64
		if _, err := fmt.Sscan(got, &s, &r, &share); err != nil || s != c.sightings || r != c.rank || math.Abs(share-c.share) > 0.0001 {
			t.Errorf("%s %s: sightings, rank and share %q, want %d %d %.4f", c.kind, c.key, got, c.sightings, c.rank, c.share)
		}
	}
	if got := b.float(`SELECT max(share_7d_before_pct) FROM spy.size_stats`); got > 100 {
		t.Errorf("shares of those ranked above add up to %v%%", got)
	}
}
