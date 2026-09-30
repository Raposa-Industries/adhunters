package judge_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/intel/internal/testdb"
	"github.com/Raposa-Industries/adhunters/intel/judge"
)

// 14:00 in New York.
var now = time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)

func day(back int) time.Time { return time.Date(2026, 9, 30-back, 0, 0, 0, 0, time.UTC) }

func exec(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
}

// seed builds one account whose campaign usually pays $50 a sale, and today
// spends with no sale.
func seed(t *testing.T, db *pgxpool.Pool) {
	exec(t, db, `INSERT INTO intel.tb_account VALUES ('acme-sc', 'main', 1, 'Acme', 'PARTNER', 'USD', 'US/Eastern', $1)`, now)
	exec(t, db, `INSERT INTO intel.tb_campaign (campaign_id, account, group_id, name, status, is_active, daily_cap, settings, first_seen_at, fetched_at)
		VALUES (1, 'acme-sc', 10, 'BP mobile', 'RUNNING', true, 100, '{}', $1, $1)`, now)
	for _, it := range []struct {
		id    int64
		state string
	}{{11, "APPROVED"}, {12, "APPROVED"}, {13, "REJECTED"}} {
		exec(t, db, `INSERT INTO intel.tb_item (item_id, campaign_id, account, title, status, is_active, approval_state, settings, first_seen_at, fetched_at)
			VALUES ($1, 1, 'acme-sc', 'Headline', 'RUNNING', true, $2, '{}', $3, $3)`, it.id, it.state, now)
	}
	// Ten days ago: $400 for 8 sales, so the usual cost per sale is $50.
	exec(t, db, `INSERT INTO intel.tb_campaign_day VALUES (1, $1, 'acme-sc', 200000, 50000, 800, 400, 8, $2)`, day(10), now)
	exec(t, db, `INSERT INTO intel.tb_item_day (item_id, day, campaign_id, account, impressions, visible_impressions, clicks, spent, conversions, fetched_at)
		VALUES (12, $1, 1, 'acme-sc', 200000, 50000, 800, 400, 8, $2)`, day(10), now)
	exec(t, db, `INSERT INTO intel.rt_item_day VALUES ('team', 'US/Eastern', $1, '1', '12', 790, 700, 200, 8, 800, 0, $2)`, day(10), now)
	// Yesterday: item 12 sold 4 times; Taboola counted none of them.
	exec(t, db, `INSERT INTO intel.tb_campaign_day VALUES (1, $1, 'acme-sc', 40000, 10000, 160, 80, 0, $2)`, day(1), now)
	exec(t, db, `INSERT INTO intel.tb_item_day (item_id, day, campaign_id, account, impressions, visible_impressions, clicks, spent, conversions, fetched_at)
		VALUES (12, $1, 1, 'acme-sc', 40000, 10000, 160, 80, 0, $2)`, day(1), now)
	exec(t, db, `INSERT INTO intel.rt_item_day VALUES ('team', 'US/Eastern', $1, '1', '12', 158, 150, 40, 4, 400, 0, $2)`, day(1), now)
	// Today: $300 and no sale; item 11 spent $200 of it.
	exec(t, db, `INSERT INTO intel.tb_campaign_day VALUES (1, $1, 'acme-sc', 150000, 40000, 600, 300, 0, $2)`, day(0), now)
	exec(t, db, `INSERT INTO intel.tb_item_day (item_id, day, campaign_id, account, impressions, visible_impressions, clicks, spent, conversions, fetched_at)
		VALUES (11, $1, 1, 'acme-sc', 100000, 30000, 400, 200, 0, $2), (12, $1, 1, 'acme-sc', 45000, 9000, 180, 95, 0, $2),
		       (13, $1, 1, 'acme-sc', 5000, 1000, 20, 5, 0, $2)`, day(0), now)
	exec(t, db, `INSERT INTO intel.rt_item_day VALUES ('team', 'US/Eastern', $1, '1', '11', 390, 350, 90, 0, 0, 0, $2),
		('team', 'US/Eastern', $1, '1', '12', 175, 160, 40, 0, 0, 0, $2)`, day(0), now)
	// Realtime: $20 in the last hour; 100 clicks in the hour from 12:00 New
	// York, where RedTrack counted only 10.
	exec(t, db, `INSERT INTO intel.tb_bucket VALUES (1, $1, 'acme-sc', 1000, 5, 20, 0, $2)`, now.Add(-30*time.Minute), now)
	exec(t, db, `INSERT INTO intel.tb_bucket VALUES (1, $1, 'acme-sc', 5000, 100, 50, 0, $2)`, now.Add(-2*time.Hour), now)
	exec(t, db, `INSERT INTO intel.rt_campaign_hour VALUES ('team', 'US/Eastern', $1, 12, '1', 10, 9, 3, 0, 0, 0, $2)`, day(0), now)
}

func TestRound(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	seed(t, db)
	s, err := judge.LoadSettings(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	d, err := judge.Gather(ctx, db, now)
	if err != nil {
		t.Fatal(err)
	}
	// 30 days: $780 over 12 RedTrack sales.
	if u := d.Usual()["acme-sc"]; u != 65 {
		t.Fatalf("usual cost per sale %.2f, want $65", u)
	}
	if _, err := judge.Results(ctx, db, d, s); err != nil {
		t.Fatal(err)
	}
	var spent float64
	var sales int64
	var source string
	if err := db.QueryRow(ctx, `SELECT spent::float8, sales, sales_source FROM intel.campaign_result WHERE campaign_id = 1 AND time_window = '30d'`).
		Scan(&spent, &sales, &source); err != nil {
		t.Fatal(err)
	}
	if spent != 780 || sales != 12 || source != "tracker" {
		t.Fatalf("30d: spent %v sales %d from %s, want 780, 12, tracker", spent, sales, source)
	}

	// Item 11 spent $200 today with no sale: clearly worse. Item 12 sold 12
	// times at about $48 a sale, better than its campaign, but 12 sales can't
	// tell it apart yet.
	words := map[int64]string{}
	rows, _ := db.Query(ctx, `SELECT item_id, word || '/' || sureness FROM intel.ad_result WHERE time_window = '30d'`)
	for rows.Next() {
		var id int64
		var w string
		rows.Scan(&id, &w)
		words[id] = w
	}
	if words[11] != "worse/clear" || words[12] != "unclear/" {
		t.Errorf("words %v", words)
	}

	var tell float64
	if err := db.QueryRow(ctx, `SELECT spend_to_tell::float8 FROM intel.ad_result WHERE item_id = 12 AND time_window = '30d'`).Scan(&tell); err != nil || tell <= 0 {
		t.Errorf("no spend to tell for an unclear ad: %v %v", tell, err)
	}

	found, err := judge.Detect(ctx, db, d, s)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, f := range found {
		kinds[f.Kind] = true
	}
	for _, k := range []string{"runaway", "tracking_gap", "postback_gap", "item_rejected"} {
		if !kinds[k] {
			t.Errorf("no %s alert; found %+v", k, found)
		}
	}
	sent := &fakeSender{}
	if _, err := judge.Record(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil)), found, now, sent, "https://app.example"); err != nil {
		t.Fatal(err)
	}
	if len(sent.msgs) != len(found) {
		t.Fatalf("sent %d, want %d", len(sent.msgs), len(found))
	}
	// A second round keeps them open and sends nothing again.
	if _, err := judge.Record(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil)), found, now.Add(time.Minute), sent, ""); err != nil {
		t.Fatal(err)
	}
	if len(sent.msgs) != len(found) {
		t.Fatalf("resent: %d messages", len(sent.msgs))
	}
	// When a condition stops holding, its alert closes.
	if _, err := judge.Record(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil)), found[1:], now.Add(2*time.Minute), sent, ""); err != nil {
		t.Fatal(err)
	}
	var open int
	db.QueryRow(ctx, `SELECT count(*) FROM intel.alert WHERE closed_at IS NULL`).Scan(&open)
	if open != len(found)-1 {
		t.Fatalf("%d open, want %d", open, len(found)-1)
	}

	list, err := judge.Suggest(ctx, db, d, s, found)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]judge.Suggestion{}
	for _, sg := range list {
		got[sg.Kind] = sg
	}
	pa, ok := got["pause-ads"]
	if !ok || len(pa.Items) != 1 || pa.Items[0] != 11 {
		t.Fatalf("pause-ads: %+v", list)
	}
	if cap, ok := got["set-daily-cap"]; !ok || cap.Values["daily_cap"] != 50.0 {
		t.Fatalf("set-daily-cap: %+v", list)
	}
	if _, err := judge.Keep(ctx, db, list, now); err != nil {
		t.Fatal(err)
	}
	var url string
	if err := db.QueryRow(ctx, `SELECT launch_url FROM intel.suggestion WHERE kind = 'pause-ads'`).Scan(&url); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "/launch/taboola/acme-sc/g/10/c/1?") || !strings.Contains(url, "ads=11") || !strings.Contains(url, "do=pause-ads") {
		t.Fatalf("launch url %q", url)
	}

	// "Not now": the same suggestion does not come back within a day.
	exec(t, db, `UPDATE intel.suggestion SET state = 'dismissed', answered_at = $1 WHERE kind = 'pause-ads'`, now)
	if _, err := judge.Keep(ctx, db, list, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(ctx, `SELECT count(*) FROM intel.suggestion WHERE kind = 'pause-ads' AND state = 'open'`).Scan(&n)
	if n != 0 {
		t.Fatal("a dismissed suggestion came back within a day")
	}

	// Paused in Launch: Taboola's settings show it, and the cap one is done.
	exec(t, db, `UPDATE intel.tb_campaign SET daily_cap = 50`)
	if _, err := judge.Keep(ctx, db, list, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var state string
	db.QueryRow(ctx, `SELECT state FROM intel.suggestion WHERE kind = 'set-daily-cap'`).Scan(&state)
	if state != "done" {
		t.Fatalf("set-daily-cap is %s, want done", state)
	}
}

type fakeSender struct{ msgs []string }

func (f *fakeSender) Send(_ context.Context, html string, _ bool) error {
	f.msgs = append(f.msgs, html)
	return nil
}

func TestNoSaleOdds(t *testing.T) {
	// The replan's table: a normal ad at $289 a sale goes $500 without one
	// about 18% of the time.
	if p := judge.NoSaleOdds(500, 289); p < 0.17 || p > 0.19 {
		t.Fatalf("odds %.3f", p)
	}
}

func TestLaunchPathNoGroup(t *testing.T) {
	p := judge.LaunchPath(judge.Suggestion{Account: "acme-sc", Campaign: 5, Kind: "pause-campaign"}, 7)
	if !strings.HasPrefix(p, "/launch/taboola/acme-sc/g/-/c/5?") {
		t.Fatalf("got %s", p)
	}
}

func TestLaunchPathNeedsAccount(t *testing.T) {
	if p := judge.LaunchPath(judge.Suggestion{Account: "redtrack:team", Kind: "pause-ads"}, 1); p != "" {
		t.Fatalf("a RedTrack-only campaign got a Launch link: %s", p)
	}
}
