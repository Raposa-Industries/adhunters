package numbers

// The handbook's checks that the numbers are not misleading, run on
// simulated hours shaped like Tracks' (spy/METRICS.md, "Checks"): many ads
// of very different sizes, each on a few publishers and devices, counts
// about three times noisier than chance (budgets are paced by the hour), a
// daily cycle, and the same hours of the 3 weeks before as the usual.
//
//   - A/A: nothing changed, so almost nothing may be called rising or fading.
//   - Honest ranges: the 90% likely range holds the true ratio about 90% of
//     the time or more.
//   - Effort: checking one publisher 10 times more, almost stopping another
//     and a 6-hour outage must not move momentum.
//   - Power: of ads that truly doubled, "rising, clear" is right and the
//     top of the list is right.
//   - Additivity: a range's sightings and checks are the sum of its days'.
//
// The other two (stopped ads seen again, and whether "rising" predicts next
// week's share of voice) need weeks of real history; see METRICS.md.

import (
	"context"
	"math"
	"math/rand/v2"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type simAd struct {
	id     int
	strata [][2]int // publisher, device
	rate   float64  // sightings per check
	truth  float64  // ratio in the range
}

type simConfig struct {
	ads      int
	risers   float64 // share of ads that truly double in the range
	effort   bool    // publisher 1 checked 10x more in the range, publisher 2 almost stopped, a 6-hour outage
	seed     uint64
	checks   int     // per publisher, device and hour
	noiseMul float64 // counts come in clumps of this size: variance this many times the mean
}

// The range: last weekend in São Paulo.
var (
	simFrom = time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	simTo   = time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC)
)

func poisson(r *rand.Rand, mean float64) int {
	if mean <= 0 {
		return 0
	}
	if mean > 30 {
		return max(0, int(math.Round(mean+math.Sqrt(mean)*r.NormFloat64())))
	}
	l, k, p := math.Exp(-mean), 0, 1.0
	for {
		p *= r.Float64()
		if p < l {
			return k
		}
		k++
	}
}

func simulate(t *testing.T, b *bench, cfg simConfig) []simAd {
	t.Helper()
	r := rand.New(rand.NewPCG(cfg.seed, 7))
	b.exec(`INSERT INTO tracks_api.publisher_v1 (id, name) VALUES (3, 'Weather')`)
	ads := make([]simAd, cfg.ads)
	for i := range ads {
		a := simAd{id: 1000 + i, rate: math.Exp(math.Log(0.01) + 1.3*r.NormFloat64()), truth: 1}
		if r.Float64() < cfg.risers {
			a.truth = 2
		}
		perm := r.Perm(6)
		for _, s := range perm[:1+r.IntN(2)] {
			a.strata = append(a.strata, [2]int{1 + s%3, 1 + s/3})
		}
		ads[i] = a
	}
	b.exec(`INSERT INTO tracks_api.creative_v1 (id, creative_key, first_seen_at, last_seen_at)
		SELECT g, 'ck-' || g, '2026-09-01', $2 FROM generate_series(1000, 999 + $1) g`, cfg.ads, now)
	b.exec(`INSERT INTO tracks_api.ad_v1 (id, creative_id, headline, first_seen_at, last_seen_at)
		SELECT g, g, 'Headline ' || g, '2026-09-01', $2 FROM generate_series(1000, 999 + $1) g`, cfg.ads, now)

	// Each publisher's checks in an hour, and its hourly traffic cycle.
	checks := func(h time.Time, pub, dev int) int {
		e := cfg.checks
		if cfg.effort && !h.Before(simFrom) && h.Before(simTo) {
			switch {
			case h.After(time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)) && h.Before(time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC)):
				return 0 // outage
			case pub == 1:
				e *= 10
			case pub == 2:
				e = max(1, e/20)
			}
		}
		return e
	}
	var hourly, cover [][]any
	for w := 0; w <= 3; w++ {
		for h := simFrom.AddDate(0, 0, -7*w); h.Before(simTo.AddDate(0, 0, -7*w)); h = h.Add(time.Hour) {
			cycle := 1 + 0.5*math.Sin(2*math.Pi*float64(h.Hour())/24)
			seen := map[[2]int]int{}
			for _, a := range ads {
				ratio := 1.0
				if w == 0 {
					ratio = a.truth
				}
				for _, s := range a.strata {
					e := checks(h, s[0], s[1])
					mean := a.rate * ratio * cycle * float64(e)
					n := int(cfg.noiseMul) * poisson(r, mean/cfg.noiseMul)
					if n > 0 {
						hourly = append(hourly, []any{h, a.id, s[0], s[1], n, h, h.Add(59 * time.Minute), true})
						seen[s] += n
					}
				}
			}
			for pub := 1; pub <= 3; pub++ {
				for dev := 1; dev <= 2; dev++ {
					if e := checks(h, pub, dev); e > 0 {
						cover = append(cover, []any{h, pub, dev, e, seen[[2]int{pub, dev}], true})
					}
				}
			}
		}
	}
	ctx := context.Background()
	if _, err := b.db.CopyFrom(ctx, pgx.Identifier{"tracks_api", "ad_hourly_v1"},
		[]string{"hour", "ad_id", "publisher_id", "device_id", "sightings", "first_seen_at", "last_seen_at", "closed"},
		pgx.CopyFromRows(hourly)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.CopyFrom(ctx, pgx.Identifier{"tracks_api", "scrape_coverage_v2"},
		[]string{"hour", "publisher_id", "device_id", "scrapes", "sightings", "closed"},
		pgx.CopyFromRows(cover)); err != nil {
		t.Fatal(err)
	}
	b.exec(`INSERT INTO tracks_api.ad_daily_v1 (day, ad_id, publisher_id, device_id, creative_id, sightings, first_seen_at, last_seen_at)
		SELECT (h.hour AT TIME ZONE 'UTC')::date, h.ad_id, h.publisher_id, h.device_id, a.creative_id, sum(h.sightings),
		       min(h.first_seen_at), max(h.last_seen_at)
		FROM tracks_api.ad_hourly_v1 h JOIN tracks_api.ad_v1 a ON a.id = h.ad_id
		GROUP BY 1, 2, 3, 4, 5`)
	b.closeHours(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	b.exec(`ANALYZE`)
	return ads
}

type simResult struct {
	truth             float64
	momentum, lo, hi  float64
	word, sure        string
	rank              int
	sightings, checks int64
}

func simRange(t *testing.T, b *bench, ads []simAd, from, to time.Time) map[int]simResult {
	t.Helper()
	truth := map[int]float64{}
	for _, a := range ads {
		truth[a.id] = a.truth
	}
	rows, err := b.db.Query(b.ctx, `SELECT creative_id, momentum, momentum_low, momentum_high, momentum_word,
			COALESCE(momentum_sure, ''), COALESCE(momentum_rank, 0), sightings, checks
		FROM spy.creative_range($1, $2, NULL, NULL, $3)`, from, to, now)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]simResult{}
	for rows.Next() {
		var id int
		var m, lo, hi *float64
		var res simResult
		if err := rows.Scan(&id, &m, &lo, &hi, &res.word, &res.sure, &res.rank, &res.sightings, &res.checks); err != nil {
			t.Fatal(err)
		}
		res.momentum, res.lo, res.hi = math.NaN(), math.NaN(), math.NaN()
		if m != nil {
			res.momentum, res.lo, res.hi = *m, *lo, *hi
		}
		res.truth = truth[id]
		out[id] = res
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

type tally struct {
	rows, withValue, labelled, clear, covered int
	median                                    float64
}

func count(res map[int]simResult, truth float64) tally {
	var t tally
	var ms []float64
	for _, r := range res {
		if r.truth != truth {
			continue
		}
		t.rows++
		if math.IsNaN(r.momentum) {
			continue
		}
		t.withValue++
		ms = append(ms, r.momentum)
		if r.word == "rising" || r.word == "fading" {
			t.labelled++
			if r.sure == "clear" {
				t.clear++
			}
		}
		if r.lo <= truth && truth <= r.hi {
			t.covered++
		}
	}
	sort.Float64s(ms)
	if len(ms) > 0 {
		t.median = ms[len(ms)/2]
	}
	return t
}

func pct(a, b int) float64 { return 100 * float64(a) / float64(max(b, 1)) }

func TestCheckAA(t *testing.T) {
	if testing.Short() {
		t.Skip("simulation")
	}
	b := newBench(t)
	ads := simulate(t, b, simConfig{ads: 600, seed: 1, checks: 60, noiseMul: 3})
	res := simRange(t, b, ads, simFrom, simTo)
	c := count(res, 1)
	t.Logf("A/A: %d ads, %d with a value, %.1f%% labelled, %.1f%% clear, %.1f%% of likely ranges hold 1, median %.3f",
		c.rows, c.withValue, pct(c.labelled, c.withValue), pct(c.clear, c.withValue), pct(c.covered, c.withValue), c.median)
	if pct(c.labelled, c.withValue) > 1 {
		t.Errorf("%.1f%% of unchanged ads called rising or fading, want at most 1%%", pct(c.labelled, c.withValue))
	}
	if pct(c.covered, c.withValue) < 87 {
		t.Errorf("likely ranges held the truth %.1f%% of the time, want about 90%%", pct(c.covered, c.withValue))
	}
	if c.withValue < c.rows/2 {
		t.Errorf("only %d of %d ads have a value", c.withValue, c.rows)
	}
}

func TestCheckEffort(t *testing.T) {
	if testing.Short() {
		t.Skip("simulation")
	}
	b := newBench(t)
	ads := simulate(t, b, simConfig{ads: 600, seed: 2, checks: 60, noiseMul: 3, effort: true})
	res := simRange(t, b, ads, simFrom, simTo)
	c := count(res, 1)
	t.Logf("effort: %d with a value, %.1f%% labelled, %.1f%% ranges hold 1, median %.3f",
		c.withValue, pct(c.labelled, c.withValue), pct(c.covered, c.withValue), c.median)
	if pct(c.labelled, c.withValue) > 1 {
		t.Errorf("%.1f%% of unchanged ads called rising or fading when checks moved", pct(c.labelled, c.withValue))
	}
	if math.Abs(c.median-1) > 0.05 {
		t.Errorf("median momentum %.3f of unchanged ads when checks moved, want about 1", c.median)
	}
}

func TestCheckPower(t *testing.T) {
	if testing.Short() {
		t.Skip("simulation")
	}
	b := newBench(t)
	ads := simulate(t, b, simConfig{ads: 600, seed: 3, checks: 60, noiseMul: 3, risers: 0.1})
	res := simRange(t, b, ads, simFrom, simTo)
	up, flat := count(res, 2), count(res, 1)
	var clearRight, clearAll, top, topRight int
	for _, r := range res {
		if r.word == "rising" && r.sure == "clear" {
			clearAll++
			if r.truth == 2 {
				clearRight++
			}
		}
		if r.rank > 0 && r.rank <= 20 {
			top++
			if r.truth == 2 {
				topRight++
			}
		}
	}
	t.Logf("power: %d of %d risers labelled, %d of %d 'rising, clear' right, top 20: %d of %d right, %.1f%% unchanged labelled, %.1f%% risers' ranges hold 2",
		up.labelled, up.rows, clearRight, clearAll, topRight, top, pct(flat.labelled, flat.withValue), pct(up.covered, up.withValue))
	if pct(clearRight, clearAll) < 90 {
		t.Errorf("'rising, clear' right %d of %d times, want 90%% or more", clearRight, clearAll)
	}
	if top < 20 || topRight < 18 {
		t.Errorf("top 20: %d of %d truly rising, want 18 or more", topRight, top)
	}
	if pct(up.labelled, up.rows) < 30 {
		t.Errorf("only %d of %d ads that doubled were called rising", up.labelled, up.rows)
	}
}

func TestCheckAdditivity(t *testing.T) {
	if testing.Short() {
		t.Skip("simulation")
	}
	b := newBench(t)
	ads := simulate(t, b, simConfig{ads: 100, seed: 4, checks: 60, noiseMul: 3})
	whole := simRange(t, b, ads, simFrom, simTo)
	parts := []map[int]simResult{
		simRange(t, b, ads, simFrom, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)),
		simRange(t, b, ads, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 18, 13, 0, 0, 0, time.UTC)),
		simRange(t, b, ads, time.Date(2026, 10, 18, 13, 0, 0, 0, time.UTC), simTo),
	}
	var checks int64
	for _, p := range parts {
		for _, r := range p {
			checks += r.checks
			break
		}
	}
	for id, w := range whole {
		var s int64
		for _, p := range parts {
			s += p[id].sightings
		}
		if s != w.sightings {
			t.Errorf("creative %d: %d sightings in the range, %d in its parts", id, w.sightings, s)
		}
		if w.checks != checks {
			t.Fatalf("%d checks in the range, %d in its parts", w.checks, checks)
		}
	}
}
