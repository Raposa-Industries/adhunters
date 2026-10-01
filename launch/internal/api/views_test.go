package api

import (
	"context"
	"testing"

	"github.com/Raposa-Industries/adhunters/launch/internal/network"
)

func TestNumbersFromIntel(t *testing.T) {
	r := setup(t)
	var res struct {
		Available bool
		Campaigns map[string]struct{ Spent, Revenue float64 }
	}
	if code := r.call("GET", "numbers?window=7d", nil, &res); code != 200 || res.Available {
		t.Fatalf("without Intel: %d %+v", code, res)
	}
	if code := r.call("GET", "numbers?window=week", nil, nil); code != 400 {
		t.Fatalf("bad window: %d", code)
	}
	ctx := context.Background()
	for _, q := range []string{
		`CREATE SCHEMA intel_api`,
		`CREATE VIEW intel_api.campaign_result_v1 AS SELECT * FROM (VALUES
			(11::bigint, '7d', 'acme-sc', 1000::bigint, 30::bigint, 12.5::numeric, 2::bigint, 80::numeric, 67.5::numeric, now()),
			(12::bigint, '7d', 'other-sc', 5::bigint, 1::bigint, 1::numeric, 0::bigint, 0::numeric, -1::numeric, now()),
			(11::bigint, 'today', 'acme-sc', 1::bigint, 1::bigint, 1::numeric, 0::bigint, 0::numeric, -1::numeric, now()))
			AS t(campaign_id, time_window, account, impressions, clicks, spent, sales, revenue, profit, refreshed_at)`,
		`CREATE VIEW intel_api.ad_result_v1 AS SELECT * FROM (VALUES
			(901::bigint, '7d', 11::bigint, 'acme-sc', 600::bigint, 20::bigint, 8::numeric, 2::bigint, 80::numeric, 72::numeric, 'better', 'clear', now()))
			AS t(item_id, time_window, campaign_id, account, impressions, clicks, spent, sales, revenue, profit, word, sureness, refreshed_at)`,
	} {
		if _, err := r.db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var got struct {
		Available bool
		Campaigns map[string]struct {
			Impressions, Clicks int64
			Spent, Revenue      float64
		}
		Ads map[string]struct {
			Word string
		}
		RefreshedAt *string `json:"refreshed_at"`
	}
	if code := r.call("GET", "numbers?window=7d&accounts=acme-sc", nil, &got); code != 200 || !got.Available {
		t.Fatalf("%d %+v", code, got)
	}
	if len(got.Campaigns) != 1 || got.Campaigns["11"].Spent != 12.5 || got.Campaigns["11"].Clicks != 30 || got.Ads["901"].Word != "better" || got.RefreshedAt == nil {
		t.Errorf("%+v", got)
	}
	r.call("GET", "numbers?window=7d", nil, &got)
	if len(got.Campaigns) != 2 {
		t.Errorf("every account: %+v", got.Campaigns)
	}
}

func TestAdsOfCampaignsAndKeptLists(t *testing.T) {
	r := setup(t)
	c1 := r.net.AddCampaign(acct, network.Campaign{Name: "a", Status: "RUNNING"}, network.Ad{Title: "One"}, network.Ad{Title: "Two"})
	c2 := r.net.AddCampaign(acct, network.Campaign{Name: "b", Status: "PAUSED"}, network.Ad{Title: "Three"})
	var res struct {
		Ads    map[string][]network.Ad
		Errors map[string]string
	}
	if code := r.call("GET", "taboola/"+acct+"/ads?campaigns="+c1.ID+","+c2.ID+",404", nil, &res); code != 200 {
		t.Fatal(code)
	}
	if len(res.Ads[c1.ID]) != 2 || len(res.Ads[c2.ID]) != 1 || res.Errors["404"] == "" {
		t.Fatalf("%+v", res)
	}
	if code := r.call("GET", "taboola/"+acct+"/ads", nil, nil); code != 400 {
		t.Fatalf("no campaigns: %d", code)
	}

	// The tree is kept for a while, and dropped by any write through Launch.
	var tree struct{ Campaigns []network.Campaign }
	r.call("GET", "taboola/"+acct+"/tree", nil, &tree)
	r.net.AddCampaign(acct, network.Campaign{Name: "made in Realize"})
	r.call("GET", "taboola/"+acct+"/tree", nil, &tree)
	if len(tree.Campaigns) != 2 {
		t.Fatalf("kept tree: %d campaigns", len(tree.Campaigns))
	}
	r.call("POST", "taboola/"+acct+"/pause", map[string]any{"campaigns": []string{c1.ID}}, nil)
	r.call("GET", "taboola/"+acct+"/tree", nil, &tree)
	if len(tree.Campaigns) != 3 {
		t.Fatalf("after a write: %d campaigns", len(tree.Campaigns))
	}
}
