package load

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/tracks/bridge"
	"github.com/Raposa-Industries/adhunters/tracks/internal/collectortest"
	"github.com/Raposa-Industries/adhunters/tracks/internal/rawtest"
)

// counts reads Tracks' counts of day d by natural keys, so rows from two
// sources compare whatever their ids.
func (b *bench) counts(d time.Time) map[string]int64 {
	b.t.Helper()
	out := map[string]int64{}
	add := func(prefix, q string) {
		rows, err := b.db.Query(b.ctx, q, d)
		if err != nil {
			b.t.Fatalf("%s: %v", prefix, err)
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var n int64
			if err := rows.Scan(&k, &n); err != nil {
				b.t.Fatal(err)
			}
			out[prefix+" "+k] += n
		}
	}
	add("hourly", `SELECT a.hour || c.creative_key || x.headline || p.name || a.device_id || ' s' || a.scrapes || ' f' || a.feed_position_sum, a.sightings
		FROM tracks.ad_hourly a JOIN tracks.ad x ON x.id = a.ad_id JOIN tracks.creative c ON c.id = x.creative_id
		JOIN tracks.publisher p ON p.id = a.publisher_id WHERE a.hour >= $1 AND a.hour < $1 + interval '1 day'`)
	add("account", `SELECT a.hour || c.creative_key || x.headline || COALESCE(ac.external_id, '-') || COALESCE(br.name, '-'), a.sightings
		FROM tracks.ad_account_brand_hourly a JOIN tracks.ad x ON x.id = a.ad_id JOIN tracks.creative c ON c.id = x.creative_id
		LEFT JOIN tracks.account ac ON ac.id = a.account_id LEFT JOIN tracks.brand br ON br.id = a.brand_id
		WHERE a.hour >= $1 AND a.hour < $1 + interval '1 day'`)
	add("answered", `SELECT h.hour || p.name || h.device_id, h.answered
		FROM tracks.publisher_hourly h JOIN tracks.publisher p ON p.id = h.publisher_id WHERE h.hour >= $1 AND h.hour < $1 + interval '1 day'`)
	add("daily", `SELECT c.creative_key || x.headline || p.name || a.device_id || ' s' || a.scrapes, a.sightings
		FROM tracks.ad_daily a JOIN tracks.ad x ON x.id = a.ad_id JOIN tracks.creative c ON c.id = x.creative_id
		JOIN tracks.publisher p ON p.id = a.publisher_id WHERE a.day = $1::date`)
	add("placement", `SELECT x.headline || pl.name, a.sightings FROM tracks.placement_daily a JOIN tracks.ad x ON x.id = a.ad_id
		JOIN tracks.placement pl ON pl.id = a.placement_id WHERE a.day = $1::date`)
	add("campaign", `SELECT c.external_id || ' s' || a.scrapes, a.sightings FROM tracks.campaign_daily a
		JOIN tracks.campaign c ON c.id = a.campaign_id WHERE a.day = $1::date`)
	add("creative campaign", `SELECT c.creative_key || x.external_id, 1 FROM tracks.creative_campaign_daily a
		JOIN tracks.creative c ON c.id = a.creative_id JOIN tracks.campaign x ON x.id = a.campaign_id WHERE a.day = $1::date`)
	add("creative link", `SELECT c.creative_key || l.link_key, 1 FROM tracks.creative_link_daily a
		JOIN tracks.creative c ON c.id = a.creative_id JOIN tracks.link l ON l.id = a.link_id WHERE a.day = $1::date`)
	return out
}

// The collector's history, as import-old copies it, equals what Tracks
// counts itself from the same scrapes: the bridge writes them into a copy of
// the collector's database, and import-old brings its counts back.
func TestImportOldMatchesTracksOwnCounts(t *testing.T) {
	b := newBench(t)
	old := collectortest.New(t)
	h14 := day.Add(14 * time.Hour)
	next := day.AddDate(0, 0, 1)
	b.capture(h14.Add(3*time.Minute),
		taboolaRec(t, h14.Add(3*time.Minute), "desktop"),
		taboolaRec(t, h14.Add(3*time.Minute+time.Second), "phone"),
		newsbreakRec(t, h14.Add(3*time.Minute+2*time.Second)),
		failedRec(h14.Add(3*time.Minute+3*time.Second)))
	b.capture(h14.Add(70*time.Minute), taboolaRec(t, h14.Add(70*time.Minute), "desktop"))
	// The first day Tracks scrapes alone.
	b.capture(next.Add(time.Hour), taboolaRec(t, next.Add(time.Hour), "phone"))

	b.now = next.Add(3 * time.Hour)
	b.loadAll()
	if _, err := b.loader.CloseReady(b.ctx); err != nil {
		t.Fatal(err)
	}
	own := b.counts(day)
	ownNext := b.counts(next)
	if own["hourly "] != 0 || len(own) < 10 {
		t.Fatalf("too few of Tracks' own counts to compare: %v", own)
	}

	br := bridge.New(b.db, old, b.store, rawtest.Quiet(), bridge.Config{From: day, Version: "test"}, bridge.NewMetrics(prometheus.NewRegistry()))
	for {
		ok, err := br.Next(b.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
	}

	if _, err := ImportOld(b.ctx, b.db, old, ImportConfig{Before: next.Add(time.Hour)}); err == nil {
		t.Fatal("import-old took a switch-over that is not a UTC midnight")
	}
	for run := 1; run <= 2; run++ { // running it again changes nothing
		res, err := ImportOld(b.ctx, b.db, old, ImportConfig{Before: next})
		if err != nil {
			t.Fatal(err)
		}
		if res.Days != 1 || res.Hours != 2 || res.Sightings != 7 {
			t.Fatalf("run %d imported %+v, want 1 day, 2 hours, 7 sightings", run, res)
		}
		got := b.counts(day)
		for k, v := range own {
			if got[k] != v {
				t.Errorf("run %d: %s is %d after the import, Tracks counted %d", run, k, got[k], v)
			}
		}
		for k, v := range got {
			if _, ok := own[k]; !ok {
				t.Errorf("run %d: the import added %s = %d", run, k, v)
			}
		}
		gotNext := b.counts(next)
		if len(gotNext) != len(ownNext) {
			t.Fatalf("run %d: the day after the switch-over changed: %v, was %v", run, gotNext, ownNext)
		}
	}
	if got := b.int(`SELECT count(*) FROM tracks.hour_state WHERE imported_at IS NOT NULL AND NOT dirty`); got != 2 {
		t.Fatalf("%d hours marked imported, want 2", got)
	}
	st, err := ReadStatus(b.ctx, b.db, true)
	if err != nil || len(st.UnbalancedHours) != 0 {
		t.Fatalf("the books do not balance after the import: %+v %v", st, err)
	}

	// The collector's hours are final: no replay, no late file.
	if _, err := Replay(b.ctx, b.db, day, next, ""); err == nil {
		t.Fatal("a replay of imported hours was allowed")
	}
	b.capture(h14.Add(30*time.Minute), taboolaRec(t, h14.Add(30*time.Minute), "phone"))
	_, err = b.loader.LoadNext(b.ctx)
	var perm errPermanent
	if !errors.As(errors.Unwrap(err), &perm) && !errors.As(err, &perm) {
		t.Fatalf("a late file for an imported hour loaded: %v", err)
	}
	if got := b.int(`SELECT count(*) FROM tracks.raw_file WHERE quarantined_at IS NOT NULL`); got != 1 {
		t.Fatal("the late file for an imported hour was not set aside")
	}
}
