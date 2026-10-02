package engine

import (
	"context"
	"testing"

	"github.com/Raposa-Industries/adhunters/raposa/internal/testdb"
)

// New ads that run now, whose landing page tracks-walker could not get and
// no visit has read whole, are queued as quick investigations, newest
// first, up to the queue depth.
func TestQueueQuick(t *testing.T) {
	pool := testdb.New(t)
	testdb.Tracks(t, pool)
	ctx := context.Background()
	s := NewStore(pool)

	// creative: network, minutes since an ad of it was seen, days since
	// Tracks first saw it.
	ads := []struct {
		creative int
		network  int
		seenMin  int
		newDays  int
	}{
		{5, 1, 2, 1},   // queued
		{6, 2, 2, 1},   // NewsBreak: not by default
		{7, 1, 180, 1}, // not running
		{8, 1, 2, 30},  // not new
		{9, 1, 2, 1},   // has a usable landing page
		{10, 1, 2, 1},  // investigated 2 hours ago
		{11, 1, 2, 3},  // stopped 2 hours ago: queued
		{12, 1, 2, 2},  // has only a bot check page: queued
		{13, 1, 2, 1},  // the walker read its landing page
		{14, 1, 2, 1},  // the walker never tried it
		{15, 1, 2, 1},  // the walker failed only once
		{16, 1, 2, 1},  // failed twice, then read on another ad
	}
	// The network is the publisher's: Taboola ads have no network_ad row.
	mustExec(t, pool, `INSERT INTO tracks_api.publisher_v1 (id, network_id, name) VALUES (501, 1, 'A Taboola site'), (502, 2, 'A NewsBreak feed')`)
	for i, a := range ads {
		mustExec(t, pool, `INSERT INTO tracks_api.creative_v1 (id, creative_key, first_seen_at) VALUES ($1, $2, now() - make_interval(days => $3))`,
			a.creative, "k"+itoa(a.creative), a.newDays)
		mustExec(t, pool, `INSERT INTO tracks_api.ad_v1 (id, creative_id, last_seen_at) VALUES ($1, $2, now() - make_interval(mins => $3))`,
			100+i, a.creative, a.seenMin)
		mustExec(t, pool, `INSERT INTO tracks_api.ad_hourly_v1 (hour, ad_id, publisher_id, device_id, sightings, first_seen_at, last_seen_at, closed)
			SELECT date_trunc('hour', t), $1, $2, 1, 3, t, t, false FROM (SELECT now() - make_interval(mins => $3) AS t) x`,
			100+i, 500+a.network, a.seenMin)
	}
	// What tracks-walker got: a usable page, a bot check, and walks that
	// got nothing.
	mustExec(t, pool, `INSERT INTO tracks_api.page_version_v1 (hash, title, word_count) VALUES
		(md5('good')::uuid, 'Seven habits', 300), (md5('bot')::uuid, 'Just a moment...', 300)`)
	walk := func(ad int, creative int, version string) {
		var hash any
		if version != "" {
			hash = version
		}
		mustExec(t, pool, `INSERT INTO tracks_api.walk_page_v1 (at, ad_id, creative_id, step, status, version_hash)
			VALUES (now(), $1, $2, 0, CASE WHEN $3::uuid IS NULL THEN NULL ELSE 200 END, $3::uuid)`, ad, creative, hash)
	}
	hash := func(name string) string {
		var h string
		if err := pool.QueryRow(ctx, `SELECT md5($1)::uuid::text`, name).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	good, bot := hash("good"), hash("bot")
	for i, a := range ads {
		switch a.creative {
		case 13:
			walk(100+i, a.creative, "")
			walk(100+i, a.creative, good)
		case 14:
		case 15:
			walk(100+i, a.creative, "")
		case 16:
			walk(100+i, a.creative, "")
			walk(100+i, a.creative, bot)
			mustExec(t, pool, `INSERT INTO tracks_api.ad_v1 (id, creative_id, last_seen_at) VALUES (900, 16, now() - interval '3 days')`)
			walk(900, a.creative, good)
		default:
			walk(100+i, a.creative, "")
			walk(100+i, a.creative, bot)
		}
	}
	mustExec(t, pool, `INSERT INTO raposa.page (content_hash, page_key, text_digest, url, host, path, title, word_count)
		VALUES (md5('a')::uuid, 'a.com/', 'd1', 'https://a.com/', 'a.com', '/', 'Seven habits', 300),
		       (md5('b')::uuid, 'b.com/', 'd2', 'https://b.com/', 'b.com', '/', 'Just a moment...', 300)`)
	mustExec(t, pool, `INSERT INTO raposa.investigation (id, creative_id, mode, status, requested_at) VALUES
		(1001, 9, 'quick', 'completed', now() - interval '3 days'),
		(1002, 10, 'deep', 'completed', now() - interval '2 hours'),
		(1003, 11, 'deep', 'stopped', now() - interval '2 hours'),
		(1004, 12, 'quick', 'completed', now() - interval '3 days')`)
	mustExec(t, pool, `INSERT INTO raposa.visit (investigation_id, purpose, rung, outcome, landed_page_id)
		SELECT 1001, 'ladder', 2, 'white', id FROM raposa.page WHERE host = 'a.com'
		UNION ALL SELECT 1004, 'ladder', 2, 'white', id FROM raposa.page WHERE host = 'b.com'`)

	mustExec(t, pool, `UPDATE raposa.setting SET value = '0' WHERE key = 'quick_queue_depth'`)
	if n, err := s.QueueQuick(ctx); err != nil || n != 0 {
		t.Fatalf("with depth 0: queued %d, %v", n, err)
	}
	mustExec(t, pool, `UPDATE raposa.setting SET value = '30' WHERE key = 'quick_queue_depth'`)
	n, err := s.QueueQuick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("queued %d, want 3", n)
	}
	rows, err := pool.Query(ctx, `SELECT creative_id FROM raposa.investigation WHERE origin = 'auto' AND mode = 'quick' AND status = 'waiting' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []int32
	for rows.Next() {
		var c int32
		_ = rows.Scan(&c)
		got = append(got, c)
	}
	rows.Close()
	if len(got) != 3 || got[0] != 5 || got[1] != 12 || got[2] != 11 {
		t.Fatalf("queued creatives %v, want [5 12 11] (newest first)", got)
	}
	// One failed walk is enough once the setting says so.
	mustExec(t, pool, `UPDATE raposa.setting SET value = '1' WHERE key = 'quick_walk_failures'`)
	if n, err := s.QueueQuick(ctx); err != nil || n != 1 {
		t.Fatalf("with one failure enough: queued %d, %v", n, err)
	}
	// Queued ones are waiting now, so nothing more.
	if n, err := s.QueueQuick(ctx); err != nil || n != 0 {
		t.Fatalf("second top-up queued %d, %v", n, err)
	}

	// NewsBreak once it is named.
	mustExec(t, pool, `UPDATE raposa.setting SET value = 'taboola, newsbreak' WHERE key = 'quick_networks'`)
	if n, err := s.QueueQuick(ctx); err != nil || n != 1 {
		t.Fatalf("with NewsBreak: queued %d, %v", n, err)
	}
}
