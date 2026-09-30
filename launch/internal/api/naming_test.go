package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/Raposa-Industries/adhunters/launch/internal/actions"
	"github.com/Raposa-Industries/adhunters/launch/internal/network"
)

func TestTeamNames(t *testing.T) {
	r := setup(t)
	img := r.upload("one.png", pngBytes(t, 1))
	r.net.AddGroup(acct, network.Group{Name: "07"})
	r.net.AddGroup(acct, network.Group{Name: "Memory Loss US"})
	r.net.AddCampaign(acct, network.Campaign{Name: "CMP12-1-Mobile-pp-bl"})
	r.net.AddCampaign(acct, network.Campaign{Name: "cmp3-1-Desktop-pp-bl"})

	var next actions.NextNames
	if code := r.call("GET", "taboola/"+acct+"/next", nil, &next); code != 200 || next.Group != "08" || next.Campaign != 13 || next.AccountNumber != "1" || next.Mobile != "CMP13-1-Mobile-pp-bl" {
		t.Fatalf("%d %+v", code, next)
	}

	send := func(key, devices string, settings map[string]any) actions.PairResult {
		var j job
		body := map[string]any{"key": key, "network": "taboola", "account": acct, "devices": devices,
			"new_group": map[string]any{"budget_model": ""},
			"settings":  settings,
			"ads":       []map[string]any{{"title": "Doctors Surprised By This Habit", "url": "https://lp.test", "image": img.SHA, "ad_id": "ah-1"}}}
		if code := r.call("POST", "pairs", body, &j); code != http.StatusAccepted {
			t.Fatalf("send %d", code)
		}
		j = r.waitJob(j)
		if j.Error != "" || j.Result.Result != "done" {
			t.Fatalf("%+v", j)
		}
		return *j.Result
	}
	maxConv := map[string]any{"daily_cap": 500, "bid_strategy": "MAX_CONVERSIONS", "exclude_cities": []string{"Atlanta"}, "ad_delivery": "OPTIMIZED"}
	res := send("m1", "mobile", maxConv)
	if res.Desktop != nil || res.Mobile == nil || res.Mobile.Campaign.Name != "CMP13-1-Mobile-pp-bl" || res.Group.Name != "08" || res.PairID != 0 {
		t.Fatalf("%+v", res)
	}
	if got := res.Mobile.Campaign.Settings; got.BidStrategy != "MAX_CONVERSIONS" || len(got.ExcludeCities) != 1 || got.AdDelivery != "OPTIMIZED" {
		t.Errorf("settings: %+v", got)
	}
	res = send("b1", "both", maxConv)
	if res.Desktop.Campaign.Name != "CMP14-1-Desktop-pp-bl" || res.Mobile.Campaign.Name != "CMP14-1-Mobile-pp-bl" || res.Group.Name != "09" || res.PairID == 0 {
		t.Fatalf("%+v", res)
	}
}

func TestAddAdsToCampaigns(t *testing.T) {
	r := setup(t)
	img := r.upload("one.png", pngBytes(t, 3))
	g := r.net.AddGroup(acct, network.Group{Name: "01"})
	c1 := r.net.AddCampaign(acct, network.Campaign{Name: "CMP01-1-Desktop-pp-bl", GroupID: g.ID})
	c2 := r.net.AddCampaign(acct, network.Campaign{Name: "CMP01-1-Mobile-pp-bl", GroupID: g.ID})
	r.net.Fail["AddAds "+c2.ID] = &network.Refused{Message: "não deu"}
	var out struct{ Done []actions.Done }
	newAds := []map[string]any{{"title": "A", "url": "https://lp.test", "image": img.SHA, "ad_id": "ah-a"}, {"title": "B", "url": "https://lp.test", "image": img.SHA, "ad_id": "ah-b"}}
	if code := r.call("POST", "taboola/"+acct+"/add-ads", map[string]any{"campaigns": []string{c1.ID, c2.ID}, "new_ads": newAds}, &out); code != 200 {
		t.Fatalf("%d", code)
	}
	if len(out.Done) != 2 || out.Done[0].Ads != 2 || out.Done[1].Error != "não deu" {
		t.Fatalf("%+v", out.Done)
	}
	if r.net.Uploaded != 1 {
		t.Errorf("uploaded %d", r.net.Uploaded)
	}
	ads, _ := r.net.Ads(context.Background(), acct, c1.ID)
	if len(ads) != 2 || ads[0].Status != "PAUSED" {
		t.Errorf("%+v", ads)
	}
	var e map[string]string
	if code := r.call("POST", "taboola/"+acct+"/add-ads", map[string]any{"campaigns": []string{c1.ID}}, &e); code != 400 {
		t.Errorf("no ads: %d", code)
	}
}
