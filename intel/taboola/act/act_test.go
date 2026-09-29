package act

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fake answers like Backstage and counts the requests that reach it.
func fake(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	var next atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			w.Write([]byte(`{"access_token":"tok","expires_in":43200}`))
			return
		}
		hits.Add(1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body == nil {
			body = map[string]any{}
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/items/mass"):
			w.Write([]byte(`{"results":[{"id":"501"},{"id":"502"}]}`))
			return
		case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/campaigns/") || strings.HasSuffix(r.URL.Path, "/duplicate/") || strings.HasSuffix(r.URL.Path, "/items/")):
			body["id"] = float64(100 + next.Add(1))
			if _, ok := body["spending_limit"]; !ok {
				body["spending_limit"] = 20.0
			}
		}
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func guard(t *testing.T) Guard {
	return Guard{Account: "acme-1-sc", NamePrefix: "AH-TEST", MaxCPC: 0.5, MaxMoney: 20, StateFile: filepath.Join(t.TempDir(), "state.json")}
}

func campaign() Obj {
	return Obj{"name": "AH-TEST one", "is_active": false, "bid_strategy": "FIXED", "cpc": 0.1,
		"spending_limit_model": "ENTIRE", "spending_limit": 20.0, "marketing_objective": "DRIVE_WEBSITE_TRAFFIC", "branding_text": "Test"}
}

func TestGuardRefusesBeforeSending(t *testing.T) {
	srv, hits := fake(t)
	c, err := New(srv.URL, "id", "s", guard(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bad := []func() error{
		func() error {
			b := campaign()
			b["name"] = "Real campaign"
			_, err := c.CreateCampaign(ctx, b)
			return err
		},
		func() error { b := campaign(); b["is_active"] = true; _, err := c.CreateCampaign(ctx, b); return err },
		func() error { b := campaign(); delete(b, "is_active"); _, err := c.CreateCampaign(ctx, b); return err },
		func() error {
			b := campaign()
			b["bid_strategy"] = "MAX_CONVERSIONS"
			_, err := c.CreateCampaign(ctx, b)
			return err
		},
		func() error { b := campaign(); b["cpc"] = 0.9; _, err := c.CreateCampaign(ctx, b); return err },
		func() error {
			b := campaign()
			b["spending_limit_model"] = "MONTHLY"
			_, err := c.CreateCampaign(ctx, b)
			return err
		},
		func() error {
			b := campaign()
			b["spending_limit"] = 50.0
			_, err := c.CreateCampaign(ctx, b)
			return err
		},
		func() error { _, err := c.UpdateCampaign(ctx, "999", Obj{"is_active": false}); return err },
		func() error { _, err := c.DeleteCampaign(ctx, "999"); return err },
		func() error { _, err := c.PatchCampaign(ctx, "999", Obj{}); return err },
		func() error { _, err := c.CreateItem(ctx, "999", Obj{"url": "https://x"}); return err },
		func() error { _, err := c.Get(ctx, "other-sc/campaigns"); return err },
	}
	for i, f := range bad {
		if err := f(); !errors.Is(err, ErrRefused) {
			t.Errorf("case %d: got %v, want ErrRefused", i, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d refused requests reached the server", hits.Load())
	}
	if _, err := New(srv.URL, "id", "s", Guard{Account: "acme-network", NamePrefix: "AH-TEST", MaxCPC: 1, MaxMoney: 1, StateFile: "x"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("network account: %v", err)
	}
}

func TestOnlyOurObjectsAndMoneyCeiling(t *testing.T) {
	srv, _ := fake(t)
	g := guard(t)
	c, _ := New(srv.URL, "id", "s", g)
	ctx := context.Background()
	var saved []Exchange
	c.Record = func(e Exchange) { saved = append(saved, e) }

	a, err := c.CreateCampaign(ctx, campaign())
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.CreateCampaign(ctx, campaign())
	if err != nil {
		t.Fatal(err)
	}
	ida, idb := str(a["id"]), str(b["id"])
	item, err := c.CreateItem(ctx, ida, Obj{"url": "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.MassCreateItems(ctx, ida, []Obj{{"url": "u", "title": "t", "thumbnail_url": "i"}}); err != nil {
		t.Fatal(err)
	}
	// An item under the wrong campaign is not ours there.
	if _, err := c.UpdateItem(ctx, idb, str(item["id"]), Obj{"is_active": false}); !errors.Is(err, ErrRefused) {
		t.Fatalf("item under other campaign: %v", err)
	}
	if _, err := c.UpdateItem(ctx, ida, "502", Obj{"is_active": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateCampaign(ctx, ida, Obj{"is_active": true}); err != nil {
		t.Fatal(err)
	}
	// A second $20 campaign would take the total to $40.
	if _, err := c.UpdateCampaign(ctx, idb, Obj{"is_active": true}); !errors.Is(err, ErrRefused) {
		t.Fatalf("second campaign on: %v", err)
	}
	if _, err := c.DuplicateCampaign(ctx, ida, nil); !errors.Is(err, ErrRefused) {
		t.Fatalf("copy without a name: %v", err)
	}
	if _, err := c.DuplicateCampaign(ctx, ida, Obj{"name": "AH-TEST copy"}); err != nil {
		t.Fatal(err)
	}

	// A new client with the same state file still knows what is ours and on.
	c2, _ := New(srv.URL, "id", "s", g)
	st := c2.State()
	if len(st.Campaigns) != 3 || len(st.Items) != 3 || st.Activated[ida] != 20 {
		t.Fatalf("state %+v", st)
	}
	if _, err := c2.UpdateCampaign(ctx, idb, Obj{"is_active": true}); !errors.Is(err, ErrRefused) {
		t.Fatalf("ceiling forgotten after restart: %v", err)
	}
	if _, err := c2.DeleteCampaign(ctx, idb); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.UpdateCampaign(ctx, idb, Obj{"is_active": false}); !errors.Is(err, ErrRefused) {
		t.Fatalf("deleted campaign still editable: %v", err)
	}
	for _, e := range saved {
		if strings.Contains(string(e.RequestBody)+string(e.Body), "tok") && e.Path != "" && strings.Contains(string(e.Body), `"tok"`) {
			t.Fatalf("token in exchange %s", e.Path)
		}
	}
	if len(saved) == 0 || saved[0].Method != http.MethodPost {
		t.Fatalf("exchanges %v", saved)
	}
}

func TestUploadImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			w.Write([]byte(`{"access_token":"tok","expires_in":43200}`))
			return
		}
		f, fh, err := r.FormFile("file")
		if err != nil || r.URL.Path != apiPrefix+uploadAPI {
			t.Fatalf("upload: %v %s", err, r.URL.Path)
		}
		if ct := fh.Header.Get("Content-Type"); ct != "image/png" {
			t.Errorf("part type %q, want image/png", ct)
		}
		b, _ := io.ReadAll(f)
		if !strings.HasPrefix(string(b), "\x89PNG") {
			t.Errorf("body %q", b)
		}
		w.Write([]byte(`{"name":"image_url","value":"http://cdn/x.png"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "id", "s", guard(t))
	u, err := c.UploadImage(context.Background(), "x.png", []byte("\x89PNG\r\n\x1a\n..."))
	if err != nil || u != "http://cdn/x.png" {
		t.Fatalf("%s %v", u, err)
	}
}

func TestDeleteLeftoverItemOnlyOurCampaigns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request may leave: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "id", "s", guard(t))
	if _, err := c.DeleteLeftoverItem(context.Background(), "999", "1"); !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v, want ErrRefused", err)
	}
}

func TestDeleteCampaignGroupOnlyEmptyAutoGenGroups(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, apiPrefix+"acme-1-sc/")
		switch {
		case r.URL.Path == tokenPath:
			w.Write([]byte(`{"access_token":"tok","expires_in":43200}`))
		case r.Method == http.MethodDelete:
			deleted = append(deleted, p)
			w.Write([]byte(`{}`))
		case p == "campaigns/":
			w.Write([]byte(`{"results":[{"id":"7","campaign_group_id":"30"},{"id":"8","campaign_group_id":"40"}]}`))
		case p == "campaigns_group/10/":
			w.Write([]byte(`{"id":"10","name":"Group 1"}`))
		case p == "campaigns_group/30/":
			w.Write([]byte(`{"id":"30","name":"AutoGen - AH-TEST still used"}`))
		case p == "campaigns_group/40/":
			w.Write([]byte(`{"id":"40","name":"AutoGen - AH-TEST ours, running"}`))
		case p == "campaigns_group/50/":
			w.Write([]byte(`{"id":"50","name":"AutoGen - AH-TEST T1"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "id", "s", guard(t))
	ctx := context.Background()
	for _, g := range []string{"10", "30", "40"} {
		if _, err := c.DeleteCampaignGroup(ctx, g); !errors.Is(err, ErrRefused) {
			t.Errorf("group %s: got %v, want ErrRefused", g, err)
		}
	}
	if _, err := c.DeleteCampaignGroup(ctx, "50"); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "campaigns_group/50" {
		t.Fatalf("deleted %v, want only group 50", deleted)
	}
	if !c.State().Deleted["group:50"] {
		t.Fatal("state lacks the deleted group")
	}
}
