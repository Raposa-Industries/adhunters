package numbers

// TestVolume times every numbers job on made-up data at the volume Tracks
// measured on 27 Sep 2026 (tracks/measure/hourclose-direction): 29,274 ads,
// about 53,000 ad_hourly rows an hour, 28 publishers on 2 devices, 22 days
// of daily counts. It also compares the new Direction fill with the
// collector's query, which took about 4 minutes a slot hour on a CX43.
//
// It takes a few minutes and a few GB, so it runs only when asked:
//
//	SPY_VOLUME=1 PG_TEST_URL=... go test -run TestVolume -v -timeout 60m ./numbers/
//
// SPY_VOLUME=data SPY_KEEP_DB=1 makes the data only and keeps the database.
//
// spy/measure/ keeps the results.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestVolume(t *testing.T) {
	if os.Getenv("SPY_VOLUME") == "" {
		t.Skip("set SPY_VOLUME=1 to run")
	}
	b := newBench(t)
	end := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) // where closed hours stop
	step := func(name string, sql string, args ...any) time.Duration {
		t.Helper()
		start := time.Now()
		for _, stmt := range strings.Split(sql, ";\n") {
			if strings.Contains(stmt, "$1") {
				b.exec(stmt, args...)
			} else {
				b.exec(stmt)
			}
		}
		took := time.Since(start)
		t.Logf("| %s | %d |", name, took.Milliseconds())
		return took
	}
	t.Logf("| step | ms |")
	t.Logf("|---|---:|")
	step("make lookups", `
		SELECT setseed(0.27);
		INSERT INTO tracks_api.publisher_v1 (id, name) SELECT g, 'pub ' || g FROM generate_series(3, 28) g;
		INSERT INTO tracks_api.creative_v1 (id, creative_key, first_seen_at, last_seen_at)
		SELECT g, 'ck-' || g, $1::timestamptz - make_interval(days => 1 + g % 60), $1 FROM generate_series(1, 12000) g;
		INSERT INTO tracks_api.account_v1 (id, external_id) SELECT g, 'acc-' || g FROM generate_series(1, 2000) g;
		INSERT INTO spy.operator (id, name, display_name) SELECT g, 'op ' || g, 'op ' || g FROM generate_series(8, 300) g;
		INSERT INTO spy.account_operator SELECT g, 8 + g % 293 FROM generate_series(1, 2000) g;
		INSERT INTO spy.creative_vertical (creative_id, vertical, shown_vertical, confidence, unsure)
		SELECT g, 'v' || g % 25, 'v' || g % 25, 0.9, false FROM generate_series(1, 12000) g;
		INSERT INTO tracks_api.ad_v1 (id, creative_id, headline, account_id, first_seen_at, last_seen_at)
		SELECT g, 1 + (g * 7919) % 12000, 'headline ' || g, 1 + g % 2000,
		       $1::timestamptz - make_interval(days => 1 + (1 + (g * 7919) % 12000) % 60), $1
		FROM generate_series(1, 29274) g`, end)
	step("make hourly counts (96 hours)", `
		SELECT setseed(0.5);
		INSERT INTO tracks_api.ad_hourly_v1 (hour, ad_id, publisher_id, device_id, sightings, first_seen_at, last_seen_at, closed)
		SELECT h, a, 1 + (a * 13 + k * 7) % 28, 1 + (a + k) % 2, 1 + floor(random() * random() * 40)::int, h, h + interval '50 minutes', TRUE
		FROM generate_series(0, 3) w
		CROSS JOIN generate_series($1::timestamptz - interval '24 hours', $1::timestamptz - interval '1 hour', interval '1 hour') h0
		CROSS JOIN LATERAL (SELECT h0 - make_interval(days => 7 * w) AS h) hh
		CROSS JOIN generate_series(1, 29274) a
		CROSS JOIN generate_series(0, 3) k
		WHERE random() < 0.46;
		INSERT INTO tracks_api.ad_account_brand_hourly_v1 (hour, ad_id, account_id, publisher_id, device_id, sightings)
		SELECT h.hour, h.ad_id, a.account_id, h.publisher_id, h.device_id, h.sightings
		FROM tracks_api.ad_hourly_v1 h JOIN tracks_api.ad_v1 a ON a.id = h.ad_id`, end)
	step("make daily counts (22 days)", `
		SELECT setseed(0.6);
		INSERT INTO tracks_api.ad_daily_v1 (day, ad_id, publisher_id, device_id, creative_id, sightings, first_seen_at, last_seen_at)
		SELECT d::date, a.id, 1 + (a.id * 13 + k * 7) % 28, 1 + (a.id + k) % 2, a.creative_id,
		       1 + floor(random() * random() * 400)::int, d + interval '1 hour', d + interval '23 hours'
		FROM generate_series($1::timestamptz - interval '22 days', $1::timestamptz, interval '1 day') d
		CROSS JOIN tracks_api.ad_v1 a
		CROSS JOIN generate_series(0, 3) k
		WHERE random() < 0.9;
		INSERT INTO tracks_api.ad_account_daily_v1 (day, ad_id, account_id, publisher_id, device_id, creative_id, sightings,
		                                            first_seen_at, last_seen_at)
		SELECT d.day, d.ad_id, a.account_id, d.publisher_id, d.device_id, d.creative_id, d.sightings, d.first_seen_at, d.last_seen_at
		FROM tracks_api.ad_daily_v1 d JOIN tracks_api.ad_v1 a ON a.id = d.ad_id;
		INSERT INTO tracks_api.scrape_coverage_v2 (hour, publisher_id, device_id, scrapes, sightings)
		SELECT h, p, dv, 240, 12000
		FROM generate_series($1::timestamptz - interval '22 days', $1::timestamptz - interval '1 hour', interval '1 hour') h
		CROSS JOIN generate_series(1, 28) p CROSS JOIN generate_series(1, 2) dv;
		INSERT INTO tracks_api.closed_hour_v1 (hour, closed_at)
		SELECT h, '2026-01-01' FROM generate_series($1::timestamptz - interval '22 days', $1::timestamptz - interval '1 hour', interval '1 hour') h;
		ANALYZE`, end.Truncate(24*time.Hour))
	var n int64
	if err := b.db.QueryRow(b.ctx, `SELECT count(*) FROM tracks_api.ad_hourly_v1 WHERE hour = $1`, end.Add(-time.Hour)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	t.Logf("(%d ad_hourly rows in the last hour)", n)
	if os.Getenv("SPY_VOLUME") == "data" {
		return // only the data, with SPY_KEEP_DB to look into it
	}

	step("read model, first run (fills every creative's days)", `SELECT spy.refresh_read_model($1)`, now)
	step("read model, next run", `SELECT spy.refresh_read_model($1)`, now.Add(5*time.Minute))
	step("Direction rebuild (daily, with noise)", `SELECT spy.direction_rebuild($1)`, now)
	slot := end.Add(-time.Hour)
	b.exec(collectorFillUsual)
	step("Direction fill, collector's query, one slot hour", `SELECT collector_fill_usual($1)`, slot)
	step("Direction fill, new, one slot hour", `SELECT spy.direction_fill_usual($1)`, slot)
	step("Direction first run (rebuild, 8 slot hours)", `SELECT spy.refresh_direction($1, TRUE)`, now)
	step("Direction five-minute run", `SELECT spy.refresh_direction($1)`, now.Add(5*time.Minute))
	step("last 24 hours (creatives and operators)", `SELECT spy.refresh_recent($1)`, now)
	step("last 24 hours, nothing new closed", `SELECT spy.refresh_recent($1)`, now.Add(time.Minute))
	step("creative range, last weekend-sized 24 hours", `SELECT count(*) FROM spy.creative_range($1, $2, NULL, NULL, $3)`,
		end.Add(-24*time.Hour), end, now)
	step("creative range, 7 days against the 7 before", `SELECT count(*) FROM spy.creative_range($1, $2, NULL, NULL, $3)`,
		end.Add(-7*24*time.Hour), end, now)
	step("operator range, 7 days", `SELECT count(*) FROM spy.operator_range($1, $2, NULL, NULL, $3)`,
		end.Add(-7*24*time.Hour), end, now)
	step("vertical range, 7 days", `SELECT count(*) FROM spy.subject_range('vertical', $1, $2, NULL, NULL, $3)`,
		end.Add(-7*24*time.Hour), end, now)
	step("publisher range, 7 days", `SELECT count(*) FROM spy.publisher_range($1, $2, $3)`,
		end.Add(-7*24*time.Hour), end, now)
}
