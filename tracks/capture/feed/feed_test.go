package feed

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestLoadTargetsKeepsEnabled(t *testing.T) {
	ts, err := LoadTargets("testdata/targets.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0].NetworkName() != Taboola || ts[1].NetworkName() != NewsBreak {
		t.Fatalf("got %+v", ts)
	}
	if got := ts[0].DeviceList(); len(got) != 2 {
		t.Fatalf("default devices: %v", got)
	}
}

func TestTaboolaRequestMatchesCollector(t *testing.T) {
	ts, _ := LoadTargets("testdata/targets.yaml")
	req, err := Build(context.Background(), ts[0], "phone")
	if err != nil {
		t.Fatal(err)
	}
	u := req.HTTP.URL
	if req.HTTP.Method != "GET" || u.Host != "trc.taboola.com" || u.Path != "/foxnews-foxnews/trc/3/json" || req.Body != nil {
		t.Fatalf("got %s %s", req.HTTP.Method, u)
	}
	q, _ := url.ParseQuery(u.RawQuery)
	if q.Get("llvl") != "2" || q.Get("pubit") != "i" || q.Get("t") != "1" {
		t.Fatalf("query %v", q)
	}
	var data taboolaData
	if err := json.Unmarshal([]byte(q.Get("data")), &data); err != nil {
		t.Fatal(err)
	}
	if data.II != "/lifestyle" || !data.Uad.Mobile || data.Uad.Platform != "iOS" || len(data.R) != 3 ||
		data.R[0] != (taboolaSlot{LI: "rbox-t2m", UIP: "Mobile Feed", S: 30}) {
		t.Fatalf("data %+v", data)
	}
	if req.HTTP.Header.Get("Referer") != "https://www.foxnews.com/lifestyle" || !strings.Contains(req.HTTP.Header.Get("User-Agent"), "Mobile") {
		t.Fatalf("headers %v", req.HTTP.Header)
	}
}

func TestNewsBreakRequestAsksOneGroup(t *testing.T) {
	ts, _ := LoadTargets("testdata/targets.yaml")
	for range 20 {
		req, err := Build(context.Background(), ts[1], "desktop")
		if err != nil {
			t.Fatal(err)
		}
		if req.HTTP.Method != "POST" || req.HTTP.URL.String() != newsbreakAuctionURL {
			t.Fatalf("got %s %s", req.HTTP.Method, req.HTTP.URL)
		}
		var body struct {
			Imp []struct {
				Ext struct {
					Gpid string `json:"gpid"`
				} `json:"ext"`
			} `json:"imp"`
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Fatal(err)
		}
		groupA := len(body.Imp) == 2 && strings.HasPrefix(body.Imp[0].Ext.Gpid, "psm-")
		groupB := len(body.Imp) == 3 && strings.HasPrefix(body.Imp[0].Ext.Gpid, "nbnlv-")
		if !groupA && !groupB {
			t.Fatalf("imps %+v", body.Imp)
		}
		if len(body.User.ID) != 10 {
			t.Fatalf("user id %q", body.User.ID)
		}
	}
}
