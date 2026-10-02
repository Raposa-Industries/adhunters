package e2e

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ceiling is the daily cap the box runs with (the owner, 2026-10-02: $500 a
// day and no spending limit). A total typed by hand is at most 30 of it.
const ceiling = 500.0

// checkRules checks every request Launch sent to Taboola, run as the box is
// (TABOOLA_CREATE_ACTIVE=1):
//   - only a new group, campaign or ad is switched on; nothing else sends an
//     is_active other than false;
//   - only reads, the image upload, and POSTs that make or pause things;
//     nothing deleted, nothing PUT;
//   - every campaign made has a daily cap above 0 and at most $500, a total
//     limit that is the team's (none, or at most 30 daily caps), and the
//     page's tracking code.
func checkRules(t *testing.T, reqs []tbRequest, tm team) {
	t.Helper()
	if len(reqs) == 0 {
		t.Fatal("Launch sent nothing to Taboola")
	}
	for _, r := range reqs {
		where := r.Method + " " + r.Path
		parts := strings.Split(strings.TrimSuffix(r.Path, "/"), "/")
		made := r.Method == "POST" && (len(parts) == 2 && (parts[1] == "campaigns" || parts[1] == "campaigns_group") ||
			len(parts) == 5 && parts[3] == "items" && parts[4] == "mass")
		if on := turnsOn(r.Body, "body"); len(on) > 0 && !made {
			t.Errorf("%s turns something on: %s", where, strings.Join(on, ", "))
		}
		switch r.Method {
		case "GET":
			continue
		case "POST":
		default:
			t.Errorf("%s: Launch only reads and makes paused things", where)
			continue
		}
		if r.Path == "operations/upload-image" {
			continue
		}
		b, ok := r.Body.(map[string]any)
		if !ok {
			t.Errorf("%s: body is not a JSON object: %.200s", where, r.Raw)
			continue
		}
		switch {
		case len(parts) == 2 && parts[1] == "campaigns":
			checkCampaign(t, where, b, tm)
		case len(parts) == 2 && parts[1] == "campaigns_group":
			if b["is_active"] != true {
				t.Errorf("%s: a group made without is_active true: %v", where, b["is_active"])
			}
		case len(parts) == 5 && parts[3] == "items" && parts[4] == "mass":
			coll, _ := b["collection"].([]any)
			if len(coll) == 0 {
				t.Errorf("%s: no ads", where)
			}
			for i, x := range coll {
				it, _ := x.(map[string]any)
				if it["is_active"] != true {
					t.Errorf("%s: ad %d made without is_active true", where, i)
				}
				if u, _ := it["url"].(string); strings.ContainsAny(u, "{}") {
					t.Errorf("%s: ad %d link carries macros: %s", where, i, u)
				}
			}
		case len(parts) == 3 && parts[1] == "campaigns", len(parts) == 5 && parts[3] == "items":
			// A change on a campaign or ad: only pausing, or settling a new
			// campaign under the ceilings.
			for k, v := range b {
				switch k {
				case "is_active":
					if v != false {
						t.Errorf("%s: turns on an existing campaign or ad", where)
					}
				case "spending_limit_model":
				case "daily_cap", "spending_limit":
					if f, _ := v.(float64); !(f > 0) || k == "daily_cap" && f > ceiling || f > 30*ceiling {
						t.Errorf("%s: %s %v is over $%.0f", where, k, v, ceiling)
					}
				default:
					t.Errorf("%s: changes %s", where, k)
				}
			}
		default:
			t.Errorf("%s: not a write Launch should make", where)
		}
	}
}

func checkCampaign(t *testing.T, where string, b map[string]any, tm team) {
	t.Helper()
	name := fmt.Sprint(b["name"])
	if b["is_active"] != true {
		t.Errorf("%s %s: made without is_active true", where, name)
	}
	cap, _ := b["daily_cap"].(float64)
	limit, _ := b["spending_limit"].(float64)
	if !(cap > 0) || cap > ceiling {
		t.Errorf("%s %s: daily cap %v, want above 0 and at most $%.0f", where, name, b["daily_cap"], ceiling)
	}
	switch {
	case tm.SpendingLimit == 0 && (b["spending_limit_model"] != "NONE" || b["spending_limit"] != nil):
		t.Errorf("%s %s: spending limit %v (%v), want none (the team's preset)", where, name, b["spending_limit"], b["spending_limit_model"])
	case tm.SpendingLimit > 0 && (!(limit > 0) || limit > 30*ceiling || b["spending_limit_model"] != "ENTIRE"):
		t.Errorf("%s %s: spending limit %v (%v), want a total (ENTIRE) above 0 and at most $%.0f", where, name, b["spending_limit"], b["spending_limit_model"], 30*ceiling)
	}
	if b["tracking_code"] != tm.TrackingCode {
		t.Errorf("%s %s: tracking code %q, want the page's default %q", where, name, b["tracking_code"], tm.TrackingCode)
	}
	if b["bid_strategy"] != tm.BidStrategy || b["marketing_objective"] != tm.Objective {
		t.Errorf("%s %s: bid %v objective %v, want %s and %s", where, name, b["bid_strategy"], b["marketing_objective"], tm.BidStrategy, tm.Objective)
	}
	ct, _ := b["city_targeting"].(map[string]any)
	if ct["type"] != "EXCLUDE" || fmt.Sprint(ct["value"]) != fmt.Sprint(anySlice(tm.ExcludeCities)) {
		t.Errorf("%s %s: cities %v, want %v excluded", where, name, ct, tm.ExcludeCities)
	}
}

func anySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// sentTrackingCode is the tracking code on the campaigns Launch made; they
// all must carry the same one.
func sentTrackingCode(t *testing.T, reqs []tbRequest) string {
	t.Helper()
	code := ""
	for _, r := range reqs {
		b, _ := r.Body.(map[string]any)
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/campaigns/") {
			c, _ := b["tracking_code"].(string)
			if code != "" && c != code {
				t.Errorf("campaigns carry different tracking codes: %q and %q", code, c)
			}
			code = c
		}
	}
	if code == "" {
		t.Fatal("no campaign carried a tracking code")
	}
	return code
}

// seedCampaign is one campaign's day as Intel would have collected it.
type seedCampaign struct {
	ID, Item       string
	Spent, Revenue float64
	Sales          int
}

// seedIntel writes today's numbers the way intel-collect leaves them:
// Taboola's per campaign and per item, and RedTrack's per sub slots. The sub
// slots are filled from the tracking code as Taboola fills its macros on a
// click, so Intel's join (sub1 = campaign, sub4 = item) only finds them when
// Launch's code puts the ids there.
func seedIntel(t *testing.T, db *pgxpool.Pool, account, group string, camps []seedCampaign, trackingCode string) {
	t.Helper()
	ctx := context.Background()
	q, err := url.ParseQuery(trackingCode)
	if err != nil {
		t.Fatalf("tracking code %q: %v", trackingCode, err)
	}
	const tz = "America/New_York"
	day := time.Now().In(mustZone(t, tz)).Format(time.DateOnly)
	if _, err := db.Exec(ctx, `INSERT INTO intel.tb_account (account, login, name, type, currency, time_zone, fetched_at)
		VALUES ($1, 'default', 'ZoltaGroup 1', 'PARTNER', 'USD', $2, now())`, account, tz); err != nil {
		t.Fatal(err)
	}
	for _, c := range camps {
		macros := map[string]string{"{campaign_id}": c.ID, "{campaign_item_id}": c.Item, "{site}": "msn-us", "{site_id}": "1137489"}
		sub := func(k string) string {
			v := q.Get(k)
			if m, ok := macros[v]; ok {
				return m
			}
			return v
		}
		const clicks = 120
		for _, st := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO intel.tb_campaign (campaign_id, account, group_id, name, status, is_active, settings, first_seen_at, fetched_at)
			  VALUES ($1::bigint, $2, NULLIF($3, '')::bigint, 'e2e', 'RUNNING', true, '{}', now(), now())`, []any{c.ID, account, group}},
			{`INSERT INTO intel.tb_item (item_id, campaign_id, account, title, settings, first_seen_at, fetched_at)
			  VALUES ($1::bigint, $2::bigint, $3, 'e2e', '{}', now(), now())`, []any{c.Item, c.ID, account}},
			{`INSERT INTO intel.tb_campaign_day (campaign_id, day, account, impressions, visible_impressions, clicks, spent, conversions, fetched_at)
			  VALUES ($1::bigint, $2::date, $3, 10000, 9000, $4, $5, 0, now())`, []any{c.ID, day, account, clicks, c.Spent}},
			{`INSERT INTO intel.tb_item_day (item_id, day, campaign_id, account, impressions, visible_impressions, clicks, spent, conversions, fetched_at)
			  VALUES ($1::bigint, $2::date, $3::bigint, $4, 10000, 9000, $5, $6, 0, now())`, []any{c.Item, day, c.ID, account, clicks, c.Spent}},
		} {
			if _, err := db.Exec(ctx, st.sql, st.args...); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(ctx, `INSERT INTO intel.rt_item_day (login, time_zone, day, sub1, sub4, clicks, lp_views, lp_clicks, conversions, revenue, cost, fetched_at)
			VALUES ('default', $1, $2::date, $3, $4, 110, 100, 40, $5, $6, 0, now())`, tz, day, sub("sub1"), sub("sub4"), c.Sales, c.Revenue); err != nil {
			t.Fatal(err)
		}
	}
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
