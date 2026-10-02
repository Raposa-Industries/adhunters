package api

import (
	"context"
	"strconv"
	"testing"

	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
)

func TestRequestsFromDesk(t *testing.T) {
	r := setup(t)
	ctx := context.Background()
	g := r.net.AddGroup(acct, network.Group{Name: "G"})
	c1 := r.net.AddCampaign(acct, network.Campaign{Name: "C1", GroupID: g.ID, Status: "RUNNING", Active: true})
	c2 := r.net.AddCampaign(acct, network.Campaign{Name: "C2", GroupID: g.ID, Status: "RUNNING", Active: true},
		network.Ad{ID: "71", AdID: "ah-aaaaaaaaaa-bbbbbbbbbb", Active: true, Status: "RUNNING"})
	ask := func(kind, input, origin string) int64 {
		var id int64
		if err := r.db.QueryRow(ctx, `SELECT launch_api.new_request_v1($1, $2, 'ana@team.test', $3)`, kind, input, origin).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	pause := ask("pause", `{"network":"taboola","account":"`+acct+`","campaigns":["`+c1.ID+`"]}`, "desk:9")
	dup := ask("duplicate", `{"network":"taboola","account":"`+acct+`","campaigns":["`+c2.ID+`"]}`, "desk:10")

	var list struct{ Requests []store.Request }
	r.call("GET", "requests", nil, &list)
	if len(list.Requests) != 2 || list.Requests[0].State != "waiting" {
		t.Fatalf("%+v", list.Requests)
	}
	if len(r.net.Calls) != 0 {
		t.Fatalf("sent before anyone confirmed: %v", r.net.Calls)
	}

	var got struct{ Request store.Request }
	if code := r.call("POST", "requests/"+itoa(pause)+"/refuse", nil, &got); code != 200 || got.Request.State != "refused" || got.Request.ConfirmedBy != "ana@team.test" {
		t.Fatalf("%d %+v", code, got)
	}
	var e map[string]string
	if code := r.call("POST", "requests/"+itoa(pause)+"/confirm", nil, &e); code != 409 {
		t.Errorf("confirm after refuse: %d %v", code, e)
	}
	if c, _ := r.net.Campaign(ctx, acct, c1.ID); !c.Active {
		t.Error("refused request paused the campaign")
	}

	if code := r.call("POST", "requests/"+itoa(dup)+"/confirm", nil, &got); code != 200 || got.Request.State != "sent" {
		t.Fatalf("%d %+v", code, got)
	}
	r.call("GET", "requests/"+itoa(dup), nil, &got)
	if got.Request.State != "sent" || len(got.Request.Result) < 10 {
		t.Errorf("%+v %s", got.Request, got.Request.Result)
	}
	var hist struct{ History []store.Change }
	r.call("GET", "history?campaign="+c2.ID, nil, &hist)
	if len(hist.History) == 0 || hist.History[0].AskedBy != "desk:10" || hist.History[0].Who != "ana@team.test" {
		t.Errorf("%+v", hist.History)
	}
	// The copy's ads are in launch_api.item_v1 with our ad id.
	var n int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM launch_api.item_v1 WHERE ad_id = 'ah-aaaaaaaaaa-bbbbbbbbbb' AND campaign_id <> $1`, c2.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("items: %d %v", n, err)
	}

	if code := r.call("GET", "requests/99", nil, &e); code != 404 {
		t.Errorf("unknown: %d", code)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Desk sends a request after the person said yes in its conversation, so
// Launch carries it out on arrival, as that person, asked by the origin.
func TestRequestsRunOnArrival(t *testing.T) {
	r := setup(t)
	ctx := context.Background()
	g := r.net.AddGroup(acct, network.Group{Name: "G"})
	c1 := r.net.AddCampaign(acct, network.Campaign{Name: "C1", GroupID: g.ID, Status: "RUNNING", Active: true})
	c2 := r.net.AddCampaign(acct, network.Campaign{Name: "C2", GroupID: g.ID, Status: "RUNNING", Active: true})
	ask := func(kind, input, origin string) int64 {
		var id int64
		if err := r.db.QueryRow(ctx, `SELECT launch_api.new_request_v1($1, $2, 'ana@team.test', $3)`, kind, input, origin).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	pause := ask("pause", `{"network":"taboola","account":"`+acct+`","campaigns":["`+c1.ID+`"]}`, "desk:step:1")
	odd := ask("pause", `{"network":"taboola","account":"`+acct+`","campaigns":[7]}`, "desk:step:2")
	old := ask("pause", `{"network":"taboola","account":"`+acct+`","campaigns":["`+c2.ID+`"]}`, "desk:step:3")
	if _, err := r.db.Exec(ctx, `UPDATE launch.request SET made_at = now() - interval '2 hours' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}

	n, err := r.l.RunWaiting(ctx)
	if err != nil || n != 2 {
		t.Fatalf("ran %d: %v", n, err)
	}
	var got struct{ Request store.Request }
	r.call("GET", "requests/"+itoa(pause), nil, &got)
	if got.Request.State != "sent" || got.Request.ConfirmedBy != "ana@team.test" {
		t.Errorf("pause: %+v", got.Request)
	}
	if c, _ := r.net.Campaign(ctx, acct, c1.ID); c.Active {
		t.Error("the request did not pause the campaign")
	}
	var hist struct{ History []store.Change }
	r.call("GET", "history?campaign="+c1.ID, nil, &hist)
	if len(hist.History) == 0 || hist.History[0].AskedBy != "desk:step:1" || hist.History[0].Who != "ana@team.test" {
		t.Errorf("%+v", hist.History)
	}
	r.call("GET", "requests/"+itoa(odd), nil, &got)
	if got.Request.State != "failed" {
		t.Errorf("unreadable: %+v", got.Request)
	}
	r.call("GET", "requests/"+itoa(old), nil, &got)
	if got.Request.State != "refused" || got.Request.ConfirmedBy != "launch" {
		t.Errorf("old: %+v", got.Request)
	}
	if c, _ := r.net.Campaign(ctx, acct, c2.ID); !c.Active {
		t.Error("an expired request paused its campaign")
	}

	if n, err := r.l.RunWaiting(ctx); err != nil || n != 0 {
		t.Errorf("second run: %d %v", n, err)
	}
}
