package engine

import (
	"context"
	"testing"

	"github.com/Raposa-Industries/adhunters/raposa/internal/testdb"
)

// New ads that run now and whose landing page no visit has read whole
// are queued as quick investigations, newest first, up to the queue depth.
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
	}
	for i, a := range ads {
		mustExec(t, pool, `INSERT INTO tracks_api.creative_v1 (id, creative_key, first_seen_at) VALUES ($1, $2, now() - make_interval(days => $3))`,
			a.creative, "k"+itoa(a.creative), a.newDays)
		mustExec(t, pool, `INSERT INTO tracks_api.ad_v1 (id, creative_id, last_seen_at) VALUES ($1, $2, now() - make_interval(mins => $3))`,
			100+i, a.creative, a.seenMin)
		mustExec(t, pool, `INSERT INTO tracks_api.network_ad_v1 (network_id, ad_id) VALUES ($1, $2)`, a.network, 100+i)
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
