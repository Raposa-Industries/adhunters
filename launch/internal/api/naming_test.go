package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/launch/internal/actions"
	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
)

func TestTeamNames(t *testing.T) {
	r := setup(t)
	img := r.upload("one.png", pngBytes(t, 1))
	// Names from before still count: a group "07" and CMP<n> campaigns.
	r.net.AddGroup(acct, network.Group{Name: "07"})
	r.net.AddGroup(acct, network.Group{Name: "Memory Loss US"})
	g2 := r.net.AddGroup(acct, network.Group{Name: "GRP02"})
	r.net.AddCampaign(acct, network.Campaign{Name: "CMP12-1-Mobile-pp-bl", GroupID: g2.ID})
	r.net.AddCampaign(acct, network.Campaign{Name: "GRP02-cmp03-Desk-pp-bl", GroupID: g2.ID})
	r.net.AddCampaign(acct, network.Campaign{Name: "GRP09-CMP40-Desk-pp-bl"}) // another group's

	// Without ?group=, the names in a new group.
	var next actions.NextNames
	if code := r.call("GET", "taboola/"+acct+"/next", nil, &next); code != 200 || next.Group != "GRP08" || next.Campaign != 1 ||
		next.Desktop != "GRP08-CMP01-Desk-pp-bl" || next.Mobile != "GRP08-CMP01-Mobile-pp-bl" {
		t.Fatalf("%d %+v", code, next)
	}
	// In a group: its number and its highest CMP, old names too.
	if code := r.call("GET", "taboola/"+acct+"/next?group="+g2.ID, nil, &next); code != 200 || next.Group != "GRP08" || next.Prefix != "GRP02" ||
		next.Campaign != 13 || next.Desktop != "GRP02-CMP13-Desk-pp-bl" {
		t.Fatalf("%d %+v", code, next)
	}
	var e map[string]string
	if code := r.call("GET", "taboola/"+acct+"/next?group=999999", nil, &e); code != 400 {
		t.Errorf("unknown group: %d %v", code, e)
	}

	send := func(key, devices string, settings map[string]any, more ...any) actions.PairResult {
		var j job
		body := map[string]any{"key": key, "network": "taboola", "account": acct, "devices": devices,
			"new_group": map[string]any{"budget_model": ""},
			"settings":  settings,
			"ads":       []map[string]any{{"title": "Doctors Surprised By This Habit", "url": "https://lp.test", "image": img.SHA, "ad_id": "ah-1"}}}
		for i := 0; i+1 < len(more); i += 2 {
			body[more[i].(string)] = more[i+1]
		}
		if code := r.call("POST", "pairs", body, &j); code != http.StatusAccepted {
			t.Fatalf("send %d", code)
		}
		j = r.waitJob(j)
		if j.Error != "" || j.Result.Result != "done" {
			t.Fatalf("%+v", j)
		}
		return *j.Result
	}
	maxConv := map[string]any{"daily_cap": 500, "bid_strategy": "MAX_CONVERSIONS", "exclude_cities": []string{"3"}, "ad_delivery": "OPTIMIZED"}
	res := send("m1", "mobile", maxConv)
	if res.Desktop != nil || res.Mobile == nil || res.Mobile.Campaign.Name != "GRP08-CMP01-Mobile-pp-bl" || res.Group.Name != "GRP08" || res.PairID != 0 {
		t.Fatalf("%+v", res)
	}
	if got := res.Mobile.Campaign.Settings; got.BidStrategy != "MAX_CONVERSIONS" || len(got.ExcludeCities) != 1 || got.AdDelivery != "OPTIMIZED" {
		t.Errorf("settings: %+v", got)
	}
	if a := res.Mobile.Ads; len(a) != 1 || a[0].Name != "GRP08-CMP01-AD01-Mobile-pp-bl" {
		t.Errorf("ad names: %+v", a)
	}
	res = send("b1", "both", maxConv)
	if res.Desktop.Campaign.Name != "GRP09-CMP01-Desk-pp-bl" || res.Mobile.Campaign.Name != "GRP09-CMP01-Mobile-pp-bl" || res.Group.Name != "GRP09" || res.PairID == 0 {
		t.Fatalf("%+v", res)
	}
	// A typed group name leads its campaigns' names.
	res = send("n1", "desktop", maxConv, "new_group", map[string]any{"name": "GRP20"})
	if res.Desktop.Campaign.Name != "GRP20-CMP01-Desk-pp-bl" || res.Group.Name != "GRP20" {
		t.Fatalf("%+v", res)
	}
	// In an existing group: the group's next number. A name typed for one
	// device names that campaign; the other keeps the team's.
	res = send("b2", "both", maxConv, "new_group", nil, "group_id", g2.ID, "mobile_name", " Memory Phones ")
	if res.Desktop.Campaign.Name != "GRP02-CMP13-Desk-pp-bl" || res.Mobile.Campaign.Name != "Memory Phones" || res.GroupID != g2.ID {
		t.Fatalf("%+v", res)
	}
}

func TestNames(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"GRP01-CMP01-Desk-pp-bl", "GRP01-CMP01-AD03-Desk-pp-bl"},
		{"GRP01-CMP01-Mobile-pp-bl", "GRP01-CMP01-AD03-Mobile-pp-bl"},
		{"CMP04-1-Desktop-pp-bl", "CMP04-1-AD03-Desktop-pp-bl"},
		{"Memory Phones", "Memory Phones-AD03"},
	} {
		if got := actions.AdName(c.in, 3); got != c.want {
			t.Errorf("AdName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for in, want := range map[string]string{"7": "GRP07", "GRP3": "GRP03", " grp12 ": "GRP12", "Memory  Loss": "Memory Loss"} {
		if got := actions.GroupPrefix(in); got != want {
			t.Errorf("GroupPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddAdsToCampaigns(t *testing.T) {
	r := setup(t)
	img := r.upload("one.png", pngBytes(t, 3))
	g := r.net.AddGroup(acct, network.Group{Name: "GRP01"})
	c1 := r.net.AddCampaign(acct, network.Campaign{Name: "GRP01-CMP01-Desk-pp-bl", GroupID: g.ID})
	c2 := r.net.AddCampaign(acct, network.Campaign{Name: "GRP01-CMP01-Mobile-pp-bl", GroupID: g.ID})
	r.net.Fail["AddAds "+c2.ID] = &network.Refused{Message: "não deu"}
	var out struct{ Done []actions.Done }
	fp := img.SHA[:10]
	// Two ticked cells of Novos anúncios' matrix: each its own headline,
	// description and the one button.
	newAds := []map[string]any{
		{"title": "A", "description": "Desc A", "url": "https://lp.test", "image": img.SHA, "cta": "Get Offer", "ad_id": "ah-" + fp + "-aaaaaaaaaa"},
		{"title": "B", "url": "https://lp.test", "image": img.SHA, "cta": "Get Offer", "ad_id": "ah-" + fp + "-bbbbbbbbbb"}}
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
	if len(ads) != 2 || ads[0].Status != "PAUSED" || ads[0].Description != "Desc A" || ads[1].Description != "" || ads[0].CTA != "Get Offer" || ads[1].Title != "B" {
		t.Errorf("%+v", ads)
	}
	// Taboola's items have no name: the ads' names are in History.
	var hist struct{ History []store.Change }
	r.call("GET", "history?campaign="+c1.ID, nil, &hist)
	if len(hist.History) == 0 || !strings.Contains(hist.History[0].Summary, "(GRP01-CMP01-AD01-Desk-pp-bl a AD02)") {
		t.Errorf("history %+v", hist.History)
	}
	// "no Launch: 2 anúncios" under the picture in the library.
	var used struct{ Used map[string]int }
	if code := r.call("GET", "used?creatives="+fp+",0000000000,bad", nil, &used); code != 200 || used.Used[fp] != 2 || len(used.Used) != 1 {
		t.Errorf("used %d %+v", code, used)
	}
	var e map[string]string
	if code := r.call("POST", "taboola/"+acct+"/add-ads", map[string]any{"campaigns": []string{c1.ID}}, &e); code != 400 {
		t.Errorf("no ads: %d", code)
	}
}
