package load_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/intel/internal/testdb"
	"github.com/Raposa-Industries/adhunters/intel/load"
)

// Shapes copied from real answers (2026-09-29/30), with made-up names and
// numbers.
const (
	accounts  = `{"results":[{"id":1,"name":"Net","account_id":"acme-network","type":"NETWORK","currency":"USD","time_zone_name":"US/Eastern"},{"id":2,"name":"Acme 1","account_id":"acme-1-sc","type":"PARTNER","currency":"USD","time_zone_name":"US/Eastern"}]}`
	campaigns = `{"results":[{"id":"501","name":"[mobile] BP 01","campaign_group_id":9001,"status":"RUNNING","is_active":true,"bid_strategy":"FIXED","cpc":0.3,"daily_cap":20.0,"spending_limit":300.0,"spent":12.5,"platform_targeting":{"type":"INCLUDE","value":["PHON"],"href":null},"traffic_allocation_mode":"OPTIMIZED"}]}`
	items     = `{"results":[{"id":"7001","campaign_id":"501","title":"Sip This at Breakfast","url":"https://x.example/","thumbnail_url":"https://cdn.example/1.png","status":"RUNNING","is_active":true,"approval_state":"APPROVED","custom_data":{"custom_id":"ah-3f9c2a71b0-8e1d44c2a9"},"policy_review":{"reject_reason":null}}]}`
	campDay   = `{"timezone":"EDT","results":[{"date":"2026-09-29 00:00:00.0","campaign_name":"[mobile] BP 01","campaign":"501","clicks":148,"impressions":40000,"visible_impressions":9000,"spent":19.71,"cpa_actions_num":1,"currency":"USD"}],"recordCount":1}`
	siteDay   = `{"timezone":"EDT","results":[{"date":"2026-09-29 00:00:00.0","site":"wave-wave","site_name":"Wave","site_id":1608503,"campaign":"501","clicks":9,"impressions":2000,"visible_impressions":500,"spent":1.1,"cpa_actions_num":0,"blocking_level":"NONE"}]}`
	itemDay   = `{"timezone":"EDT","results":[{"item":"7001","custom_id":"ah-3f9c2a71b0-8e1d44c2a9","item_name":"Sip This","thumbnail_url":"t","campaign":"501","impressions":20000,"visible_impressions":4000,"clicks":75,"spent":9.9,"actions":1},{"item":null,"item_name":"Old one","campaign":"499","impressions":10,"visible_impressions":1,"clicks":1,"spent":0.2,"actions":0,"old_item_version_id":"4304222189 (Old version)"}]}`
	bucket    = `{"timezone":"EDT","results":[{"date":"2026-09-29 14:05:00.0","campaign_id":"501","campaign_name":"x","clicks":3,"visible_impressions":200,"spent":0.9,"cpa_actions_num":0}]}`
	rtItem    = `[{"sub1":"501","sub4":"7001","clicks":74,"lp_views":0,"lp_clicks":3,"conversions":1,"revenue":"49.00","cost":0},{"sub1":"","sub4":"","clicks":2,"conversions":0}]`
	rtHour    = `[{"sub1":"501","hour_of_day":14,"clicks":20,"lp_views":18,"lp_clicks":2,"conversions":0,"revenue":0,"cost":0}]`
	rtConv    = `{"items":[{"id":"c1","clickid":"k1","sub1":"501","sub4":"7001","sub8":"1608503","type":"Purchase","status":"approved","payout":49,"created_at":"2026-09-29 18:00:00"}],"total":1}`
	campNone  = `{"results":[]}`
)

func put(t *testing.T, db *pgxpool.Pool, id, source, account, kind string, params map[string]any, at time.Time, body string) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(body))
	zw.Close()
	if params == nil {
		params = map[string]any{}
	}
	if _, err := db.Exec(context.Background(), `INSERT INTO intel.answer (spool_id, source, login, account, kind, path, params, status, fetched_at, body, body_bytes)
		VALUES ($1, $2, 'main', $3, $4, '/x', $5, 200, $6, $7, $8)`, id, source, account, kind, params, at, buf.Bytes(), len(body)); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 29, 19, 0, 0, 0, time.UTC)
	day := map[string]any{"from": "2026-09-29", "to": "2026-09-29", "time_zone": "US/Eastern"}
	put(t, db, "a1", "taboola", "", "taboola.accounts", nil, at, accounts)
	put(t, db, "a2", "taboola", "acme-1-sc", "taboola.campaigns", nil, at, campaigns)
	put(t, db, "a3", "taboola", "acme-1-sc", "taboola.items", map[string]any{"campaign_id": "501"}, at, items)
	put(t, db, "a4", "taboola", "acme-1-sc", "taboola.campaign_day", day, at, campDay)
	put(t, db, "a5", "taboola", "acme-1-sc", "taboola.site_day", day, at, siteDay)
	put(t, db, "a6", "taboola", "acme-1-sc", "taboola.item_day", day, at, itemDay)
	put(t, db, "a7", "taboola", "acme-1-sc", "taboola.bucket", map[string]any{"time_zone": "US/Eastern"}, at, bucket)
	put(t, db, "a8", "redtrack", "", "redtrack.item_day", day, at, rtItem)
	put(t, db, "a9", "redtrack", "", "redtrack.campaign_hour", day, at, rtHour)
	put(t, db, "a10", "redtrack", "", "redtrack.conversions", map[string]any{"from": "2026-09-28", "to": "2026-09-29"}, at, rtConv)
	l := &load.Loader{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	n, err := l.Pending(ctx)
	if err != nil || n != 10 {
		t.Fatalf("loaded %d: %v", n, err)
	}
	var bad int
	db.QueryRow(ctx, `SELECT count(*) FROM intel.answer WHERE load_error IS NOT NULL`).Scan(&bad)
	if bad != 0 {
		var e string
		db.QueryRow(ctx, `SELECT kind || ': ' || load_error FROM intel.answer WHERE load_error IS NOT NULL LIMIT 1`).Scan(&e)
		t.Fatalf("%d answers failed: %s", bad, e)
	}
	checks := []struct {
		sql  string
		want string
	}{
		{`SELECT string_agg(account || ' ' || time_zone, ',' ORDER BY account) FROM intel.tb_account`, "acme-1-sc US/Eastern,acme-network US/Eastern"},
		{`SELECT group_id || ' ' || array_to_string(platforms, '+') || ' ' || daily_cap FROM intel.tb_campaign`, "9001 PHON 20"},
		{`SELECT custom_id FROM intel.tb_item`, "ah-3f9c2a71b0-8e1d44c2a9"},
		{`SELECT clicks || ' ' || spent || ' ' || conversions FROM intel.tb_campaign_day WHERE day = '2026-09-29'`, "148 19.7100 1"},
		{`SELECT site_id || ' ' || site_name FROM intel.tb_site_day`, "1608503 Wave"},
		{`SELECT string_agg(item_id || ' ' || old_version, ',' ORDER BY item_id) FROM intel.tb_item_day`, "7001 false,4304222189 true"},
		{`SELECT to_char(bucket AT TIME ZONE 'UTC', 'HH24:MI') FROM intel.tb_bucket`, "18:05"},
		{`SELECT count(*) || ' ' || max(revenue) FROM intel.rt_item_day`, "1 49.0000"},
		{`SELECT hour || ' ' || lp_views FROM intel.rt_campaign_hour`, "14 18"},
		{`SELECT click_id || ' ' || payout || ' ' || type FROM intel.rt_conversion`, "k1 49.0000 Purchase"},
		{`SELECT count(*)::text FROM intel.tb_campaign_version`, "1"},
	}
	for _, c := range checks {
		var got string
		if err := db.QueryRow(ctx, c.sql).Scan(&got); err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		if got != c.want {
			t.Errorf("%s\n got %q\nwant %q", c.sql, got, c.want)
		}
	}

	// An older answer loaded later changes nothing; a newer list without the
	// campaign marks it gone.
	put(t, db, "b1", "taboola", "acme-1-sc", "taboola.campaign_day", day, at.Add(-time.Hour),
		`{"results":[{"date":"2026-09-29 00:00:00.0","campaign":"501","clicks":1,"impressions":1,"visible_impressions":1,"spent":1,"cpa_actions_num":0}]}`)
	put(t, db, "b2", "taboola", "acme-1-sc", "taboola.campaigns", nil, at.Add(time.Hour), campNone)
	if _, err := l.Pending(ctx); err != nil {
		t.Fatal(err)
	}
	var clicks int
	var gone bool
	db.QueryRow(ctx, `SELECT clicks FROM intel.tb_campaign_day`).Scan(&clicks)
	db.QueryRow(ctx, `SELECT gone_at IS NOT NULL FROM intel.tb_campaign`).Scan(&gone)
	if clicks != 148 || !gone {
		t.Fatalf("clicks %d (want 148), gone %v", clicks, gone)
	}

	// Reloading everything ends in the same tables.
	if _, err := l.Reload(ctx, at.Add(-24*time.Hour), at.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(ctx, `SELECT clicks FROM intel.tb_campaign_day`).Scan(&clicks)
	if clicks != 148 {
		t.Fatalf("after reload clicks %d", clicks)
	}
}

func TestBadAnswerIsKept(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	put(t, db, "x", "taboola", "acme-1-sc", "taboola.campaign_day", nil, time.Now(), `{not json`)
	l := &load.Loader{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := l.Pending(ctx); err != nil {
		t.Fatal(err)
	}
	var e string
	if err := db.QueryRow(ctx, `SELECT load_error FROM intel.answer`).Scan(&e); err != nil || e == "" {
		t.Fatalf("no load error recorded: %v", err)
	}
}
