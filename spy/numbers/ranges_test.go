package numbers

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// closeHours marks every hour in [from, to) closed, as Tracks does whether
// or not anything was seen.
func (b *bench) closeHours(from, to time.Time) {
	b.t.Helper()
	b.exec(`INSERT INTO tracks_api.closed_hour_v1 (hour, closed_at)
		SELECT g, '2026-01-01' FROM generate_series($1::timestamptz, $2::timestamptz - interval '1 hour', interval '1 hour') g
		ON CONFLICT DO NOTHING`, from, to)
}

type rangeRow struct {
	sightings, usual         int64
	presence, presenceUsual  float64
	momentum, low, high      float64
	word, sure               string
	periods, publishers, rnk int
}

func (b *bench) rangeRow(fn string, key int, from, to time.Time) rangeRow {
	b.t.Helper()
	var r rangeRow
	var presenceUsual, momentum, low, high *float64
	var sure *string
	var rnk *int
	col := map[string]string{"creative_range": "creative_id", "operator_range": "operator_id"}[fn]
	if err := b.db.QueryRow(b.ctx, `SELECT sightings, sightings_usual, presence, presence_usual, momentum, momentum_low,
			momentum_high, momentum_word, momentum_sure, usual_periods, publishers, momentum_rank
		FROM spy.`+fn+`($1, $2, NULL, NULL, $3) WHERE `+col+` = $4`, from, to, now, key).Scan(
		&r.sightings, &r.usual, &r.presence, &presenceUsual, &momentum, &low, &high, &r.word, &sure,
		&r.periods, &r.publishers, &rnk); err != nil {
		b.t.Fatal(err)
	}
	deref := func(p *float64) float64 {
		if p == nil {
			return math.NaN()
		}
		return *p
	}
	r.presenceUsual, r.momentum, r.low, r.high = deref(presenceUsual), deref(momentum), deref(low), deref(high)
	if sure != nil {
		r.sure = *sure
	}
	if rnk != nil {
		r.rnk = *rnk
	}
	return r
}

func close3(a, b float64) bool { return math.Abs(a-b) < 0.0015 }

// The Mantel-Haenszel ratio worked by hand. Fox News desktop: 10 an hour in
// 100 checks in the usual weeks, 20 an hour now. MSN desktop: 50 an hour,
// checked only now, so it counts in presence but not in momentum.
//
//	a = 80, T1 = 400; b = 120, T0 = 1,200; T = 1,600
//	M = (80 x 1200 / 1600) / (120 x 400 / 1600) = 60 / 30 = 2
//	Var(ln M) = 400 x 1200 x 200 / 1600^2 / (60 x 30) = 0.020833 (noise 1)
//	likely range = 2 x exp(-+1.645 x 0.144338) = 1.577 to 2.536
func TestMomentumByHand(t *testing.T) {
	b := newBench(t)
	b.ad(10, 100, 500, "ck-10", "Lose belly fat")
	b.exec(`INSERT INTO spy.account_operator VALUES (500, 7)`)
	today := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	b.closeHours(today.AddDate(0, 0, -22), today.Add(12*time.Hour))
	for h := 4; h < 8; h++ {
		at := today.Add(time.Duration(h) * time.Hour)
		b.hour(at, 1, 1, 100, true, map[int]int{100: 20})
		b.hour(at, 2, 1, 100, true, map[int]int{100: 50})
		for w := 1; w <= 3; w++ {
			b.hour(at.AddDate(0, 0, -7*w), 1, 1, 100, true, map[int]int{100: 10})
		}
	}
	want := rangeRow{sightings: 280, usual: 120, presence: 35, presenceUsual: 10, momentum: 2, low: 1.577, high: 2.536,
		word: "rising", sure: "clear", periods: 3, publishers: 2, rnk: 1}
	check := func(name string, got rangeRow) {
		t.Helper()
		if got.sightings != want.sightings || got.usual != want.usual || !close3(got.presence, want.presence) ||
			!close3(got.presenceUsual, want.presenceUsual) || !close3(got.momentum, want.momentum) ||
			!close3(got.low, want.low) || !close3(got.high, want.high) || got.word != want.word ||
			got.sure != want.sure || got.periods != want.periods || got.publishers != want.publishers || got.rnk != want.rnk {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
		}
	}
	check("creative", b.rangeRow("creative_range", 10, today.Add(4*time.Hour), today.Add(8*time.Hour)))
	check("operator", b.rangeRow("operator_range", 7, today.Add(4*time.Hour), today.Add(8*time.Hour)))
	// Ends round to the nearest hour: 03:50 to 08:10 reads 04:00 to 08:00.
	check("rounded", b.rangeRow("creative_range", 10, today.Add(3*time.Hour+50*time.Minute), today.Add(8*time.Hour+10*time.Minute)))
}

// New, too little data, steady and fading.
func TestMomentumWords(t *testing.T) {
	b := newBench(t)
	today := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	b.closeHours(today.AddDate(0, 0, -22), today.Add(12*time.Hour))
	b.ad(10, 100, 0, "ck-10", "Steady")
	b.ad(11, 110, 0, "ck-11", "Fading")
	b.ad(12, 120, 0, "ck-12", "Small")
	b.ad(13, 130, 0, "ck-13", "New")
	b.exec(`UPDATE tracks_api.creative_v1 SET first_seen_at = $1 WHERE id = 13`, today.Add(-2*time.Hour))
	for h := 0; h < 8; h++ {
		at := today.Add(time.Duration(h) * time.Hour)
		b.hour(at, 1, 1, 1000, true, map[int]int{100: 300, 110: 100, 120: 1, 130: 40})
		for w := 1; w <= 3; w++ {
			b.hour(at.AddDate(0, 0, -7*w), 1, 1, 1000, true, map[int]int{100: 300, 110: 300, 120: 1})
		}
	}
	for key, want := range map[int]string{10: "steady", 11: "fading", 12: "too_little", 13: "new"} {
		got := b.rangeRow("creative_range", key, today, today.Add(8*time.Hour))
		if got.word != want {
			t.Errorf("creative %d is %s, want %s (%+v)", key, got.word, want, got)
		}
		if want == "new" || want == "too_little" {
			if !math.IsNaN(got.momentum) {
				t.Errorf("creative %d shows momentum %v with %s", key, got.momentum, want)
			}
		}
	}
	if got := b.rangeRow("creative_range", 11, today, today.Add(8*time.Hour)); !close3(got.momentum, 1.0/3) || got.sure != "clear" {
		t.Errorf("fading: %+v", got)
	}
}

// A week or more is compared with the whole weeks just before it, and its
// whole days come from the daily counts.
func TestRangeOfDays(t *testing.T) {
	b := newBench(t)
	b.ad(10, 100, 0, "ck-10", "Lose belly fat")
	b.closeHours(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	// 50 a day from 3 to 9 Oct, 100 a day from 10 to 16 Oct, 240 checks a day.
	for d := 3; d <= 16; d++ {
		day := time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC)
		s := 50
		if d >= 10 {
			s = 100
		}
		b.day(day, 100, 1, 1, s)
		b.exec(`INSERT INTO tracks_api.scrape_coverage_v2 (hour, publisher_id, device_id, scrapes, sightings)
			SELECT g, 1, 1, 10, 0 FROM generate_series($1::timestamptz, $1::timestamptz + interval '23 hours', interval '1 hour') g`, day)
	}
	got := b.rangeRow("creative_range", 10, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC))
	if got.sightings != 700 || got.usual != 350 || !close3(got.momentum, 2) || got.periods != 1 || got.word != "rising" {
		t.Errorf("a week against the week before: %+v", got)
	}
	var kind string
	var from time.Time
	if err := b.db.QueryRow(b.ctx, `SELECT usual_kind, usual_from FROM spy.range_info($1, $2, NULL, NULL, $3)`,
		time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), now).Scan(&kind, &from); err != nil {
		t.Fatal(err)
	}
	if kind != "before" || !from.Equal(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("usual %s from %v, want before from 3 Oct", kind, from)
	}
}

func TestRangePlan(t *testing.T) {
	b := newBench(t)
	type plan struct {
		prec, usual      string
		from, to         time.Time
		dayFrom, dayTo   time.Time
		periods          int
		firstUsualFromAt time.Time
	}
	read := func(from, to time.Time) plan {
		t.Helper()
		var p plan
		if err := b.db.QueryRow(b.ctx, `SELECT n.prec, n.usual_kind, n.from_at, n.to_at, n.day_from::timestamptz, n.day_to::timestamptz,
				(SELECT count(*) FROM spy.range_plan($1, $2, NULL, NULL, $3) u WHERE u.period = 'usual'),
				(SELECT min(u.from_at) FROM spy.range_plan($1, $2, NULL, NULL, $3) u WHERE u.period = 'usual')
			FROM spy.range_plan($1, $2, NULL, NULL, $3) n WHERE n.period = 'now'`, from, to, now).Scan(
			&p.prec, &p.usual, &p.from, &p.to, &p.dayFrom, &p.dayTo, &p.periods, &p.firstUsualFromAt); err != nil {
			t.Fatal(err)
		}
		p.from, p.to, p.dayFrom, p.dayTo, p.firstUsualFromAt = p.from.UTC(), p.to.UTC(), p.dayFrom.UTC(), p.dayTo.UTC(), p.firstUsualFromAt.UTC()
		return p
	}
	utc := func(m time.Month, d, h int) time.Time { return time.Date(2026, m, d, h, 0, 0, 0, time.UTC) }
	// Last weekend in São Paulo: to the hour, every hour from the hourly
	// counts (under 3 days), against the same hours of the 3 weekends before.
	sp := time.FixedZone("BRT", -3*3600)
	got := read(time.Date(2026, 10, 17, 0, 0, 0, 0, sp), time.Date(2026, 10, 19, 0, 0, 0, 0, sp))
	if want := (plan{"hour", "weeks", utc(10, 17, 3), utc(10, 19, 3), utc(10, 18, 0), utc(10, 18, 0), 3, utc(9, 26, 3)}); got != want {
		t.Errorf("weekend:\n got %+v\nwant %+v", got, want)
	}
	// Four days: the whole days inside from the daily counts.
	got = read(utc(10, 10, 5), utc(10, 14, 5))
	if want := (plan{"hour", "weeks", utc(10, 10, 5), utc(10, 14, 5), utc(10, 11, 0), utc(10, 14, 0), 3, utc(9, 19, 5)}); got != want {
		t.Errorf("four days:\n got %+v\nwant %+v", got, want)
	}
	// Two weeks: the two weeks before.
	got = read(utc(10, 1, 0), utc(10, 15, 0))
	if got.usual != "before" || got.periods != 1 || !got.firstUsualFromAt.Equal(utc(9, 17, 0)) {
		t.Errorf("two weeks: %+v", got)
	}
	// Older than 35 days: whole UTC days, ends rounded to the nearest midnight.
	got = read(time.Date(2026, 8, 1, 5, 0, 0, 0, time.UTC), time.Date(2026, 8, 3, 20, 0, 0, 0, time.UTC))
	if want := (plan{"day", "weeks", utc(8, 1, 0), utc(8, 4, 0), utc(8, 1, 0), utc(8, 4, 0), 3, utc(7, 11, 0)}); got != want {
		t.Errorf("August:\n got %+v\nwant %+v", got, want)
	}
	// A range reaching into the future stops where closed hours stop (here,
	// with nothing closed, at the current hour).
	got = read(utc(10, 20, 10), utc(10, 25, 0))
	if !got.to.Equal(utc(10, 20, 12)) {
		t.Errorf("future end read as %v", got.to)
	}
	if _, err := b.db.Exec(b.ctx, `SELECT * FROM spy.range_plan($1, $1, NULL, NULL, $2)`, now, now); err == nil {
		t.Error("an empty range was accepted")
	}
	if _, err := b.db.Exec(b.ctx, `SELECT * FROM spy.range_plan($1, $2, NULL, NULL, $3)`, utc(10, 20, 12), utc(10, 21, 0), now); err == nil {
		t.Error("a range with no closed hour was accepted")
	}
}

// Lifespan against the vertical's Kaplan-Meier curve, and launch hit rate.
// 21 health creatives of operator 7, all first seen on 1 Oct: 10 ended on
// 3 Oct (2 days), 11 still running on 20 Oct (19 days).
func TestLifespanAndHitRate(t *testing.T) {
	b := newBench(t)
	b.exec(`INSERT INTO tracks_api.account_v1 (id, external_id) VALUES (500, 'today55-sc')`)
	b.exec(`INSERT INTO spy.account_operator VALUES (500, 7)`)
	first := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	b.closeHours(first.AddDate(0, 0, -22), today.Add(12*time.Hour))
	for c := 1; c <= 21; c++ {
		b.ad(c, c, 500, fmt.Sprintf("ck-%d", c), "Headline")
		b.exec(`UPDATE tracks_api.creative_v1 SET first_seen_at = $2 WHERE id = $1`, c, first)
		b.exec(`INSERT INTO spy.creative_vertical (creative_id, vertical, shown_vertical, confidence, unsure)
			VALUES ($1, 'health', 'Health', 0.9, false)`, c)
		for d := 0; d < 4; d++ {
			if c <= 10 && d > 2 {
				break
			}
			b.day(first.AddDate(0, 0, d), c, 1, 1, 5)
		}
		if c > 10 {
			b.day(today, c, 1, 1, 5)
		}
	}
	b.exec(`UPDATE tracks_api.creative_v1 SET last_seen_at = $1 WHERE id <= 10`, first.AddDate(0, 0, 2).Add(20*time.Hour))
	b.hour(today.Add(9*time.Hour), 1, 1, 100, true, map[int]int{21: 5})
	if _, err := b.r.ReadModel(b.ctx); err != nil {
		t.Fatal(err)
	}

	var days int
	var pct float64
	var curve string
	var running bool
	if err := b.db.QueryRow(b.ctx, `SELECT lifespan_days, lifespan_pct, lifespan_curve, running
		FROM spy.creative_range($1, $2, NULL, NULL, $3) WHERE creative_id = 21`,
		today, today.Add(12*time.Hour), now).Scan(&days, &pct, &curve, &running); err != nil {
		t.Fatal(err)
	}
	// Of 21, 10 ended at 2 days: 10 of 21 ended younger than 19 days.
	if days != 19 || !close3(pct, 47.6) || curve != "health" || !running {
		t.Errorf("lifespan %d days, %v%% ended younger (curve %s, running %v); want 19, 47.6, health, true", days, pct, curve, running)
	}

	var launches, hits, misses, testing int
	var rate, low, high float64
	if err := b.db.QueryRow(b.ctx, `SELECT launches, hits, misses, testing, hit_rate_pct, hit_rate_low_pct, hit_rate_high_pct
		FROM spy.operator_range($1, $2, NULL, NULL, $3) WHERE operator_id = 7`,
		first, first.AddDate(0, 0, 4), now).Scan(&launches, &hits, &misses, &testing, &rate, &low, &high); err != nil {
		t.Fatal(err)
	}
	// Wilson 90%: 11 of 21 is 52.4%, likely 35.2% to 69.0%.
	if launches != 21 || hits != 11 || misses != 10 || testing != 0 || !close3(rate, 52.4) ||
		math.Abs(low-35.2) > 0.1 || math.Abs(high-69.0) > 0.1 {
		t.Errorf("launches %d, hits %d, misses %d, testing %d, rate %v (%v to %v)", launches, hits, misses, testing, rate, low, high)
	}

	var verticals int
	if err := b.db.QueryRow(b.ctx, `SELECT count(*) FROM spy.subject_range('vertical', $1, $2, NULL, NULL, $3) WHERE key = 'health'`,
		first, first.AddDate(0, 0, 4), now).Scan(&verticals); err != nil || verticals != 1 {
		t.Errorf("vertical range: %d rows, err %v", verticals, err)
	}
}
