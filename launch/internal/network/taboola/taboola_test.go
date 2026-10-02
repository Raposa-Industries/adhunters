package taboola

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Raposa-Industries/adhunters/kit/keep"
	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	"github.com/Raposa-Industries/adhunters/shared/taboola/write"
)

// backstage answers the few Backstage calls the adapter makes, and records
// every body sent.
type backstage struct {
	mu      sync.Mutex
	calls   []string
	bodies  map[string][]map[string]any
	uploads int
	next    int
}

func (b *backstage) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.URL.Path == "/backstage/oauth/token" {
		io.WriteString(w, `{"access_token":"tok","token_type":"bearer","expires_in":43200}`)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/backstage/api/1.0/")
	key := r.Method + " " + p
	b.calls = append(b.calls, key)
	var body map[string]any
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.bodies[key] = append(b.bodies[key], body)
	}
	b.next++
	switch {
	case p == "operations/upload-image":
		b.uploads++
		fmt.Fprintf(w, `{"value":"https://cdn.taboola.test/img-%d.jpg"}`, b.uploads)
	case r.Method == "POST" && p == "acme-sc/campaigns/":
		fmt.Fprintf(w, `{"id":"%d","name":%q,"status":"PAUSED","is_active":false,"campaign_group_id":%q,"platform_targeting":{"type":"INCLUDE","value":%s}}`,
			500+b.next, body["name"], body["campaign_group_id"], mustJSON(body["platform_targeting"].(map[string]any)["value"]))
	case r.Method == "POST" && p == "acme-sc/campaigns_group/":
		fmt.Fprintf(w, `{"id":"%d","name":%q,"status":"PAUSED","is_active":false,"spending_limit_model":"NONE","end_date":"9999-12-31"}`, 300+b.next, body["name"])
	case r.Method == "POST" && strings.HasSuffix(p, "/duplicate/"):
		fmt.Fprintf(w, `{"id":"%d","name":%q,"status":"PAUSED","is_active":false,"campaign_group_id":%q}`, 700+b.next, body["name"], body["campaign_group_id"])
	case r.Method == "GET" && strings.HasPrefix(p, "acme-sc/campaigns/7") && strings.HasSuffix(p, "/items/") && p != "acme-sc/campaigns/77/items/":
		io.WriteString(w, `{"results":[{"id":"31","title":"Old ad","url":"https://lp.test/a","thumbnail_url":"https://cdn.taboola.test/old.jpg","is_active":false,"status":"PENDING_APPROVAL","custom_data":{"custom_id":"ah-1"}},{"id":"32","title":"Second","url":"https://lp.test/a","thumbnail_url":"https://cdn.taboola.test/2.jpg","is_active":true,"status":"PENDING_APPROVAL"}]}`)
	case r.Method == "POST" && strings.Contains(p, "/items/3"):
		io.WriteString(w, `{"id":"32","is_active":false}`)
	case r.Method == "GET" && p == "acme-sc/campaigns/77/items/":
		io.WriteString(w, `{"results":[{"id":"1","title":"Old ad","url":"https://lp.test/a","thumbnail_url":"https://cdn.taboola.test/old.jpg","is_active":true,"status":"RUNNING","approval_state":"APPROVED","custom_data":{"custom_id":"ah-1"}}]}`)
	case r.Method == "POST" && strings.HasSuffix(p, "/items/mass"):
		coll := body["collection"].([]any)
		var out []string
		for i, it := range coll {
			m := it.(map[string]any)
			cd, _ := m["custom_data"].(map[string]any)
			out = append(out, fmt.Sprintf(`{"id":"%d","title":%q,"is_active":false,"status":"PENDING","custom_data":{"custom_id":%q}}`, 900+i, m["title"], cd["custom_id"]))
		}
		io.WriteString(w, `{"results":[`+strings.Join(out, ",")+`]}`)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"message":"not in this fake"}`)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func adapter(t *testing.T) (*Taboola, *backstage) {
	t.Helper()
	b := &backstage{bodies: map[string][]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(srv.Close)
	c, err := write.New(write.Settings{Base: srv.URL, ClientID: "id", ClientSecret: "s", Accounts: []string{"acme-sc"}, MaxCPC: 1, MaxDailyCap: 100},
		keep.New(t.TempDir()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return New(c), b
}

func TestPairSharesUploadsAndTargetsOneDevice(t *testing.T) {
	tb, b := adapter(t)
	images := map[string][]byte{"aaa": []byte("\xff\xd8one"), "bbb": []byte("\xff\xd8two")}
	up := &network.Uploads{Read: func(sha string) ([]byte, string, error) { return images[sha], sha + ".jpg", nil }}
	ads := []network.NewAd{
		{Title: "Doctors Surprised By This Morning Habit", URL: "https://lp.test/a", Image: "aaa", CTA: "Learn More", AdID: "ah-1"},
		{Title: "The Kitchen Trick Seniors Swear By", URL: "https://lp.test/a", Image: "bbb", CTA: "Read More", AdID: "ah-2", AI: true},
		{Title: "Why Your Memory Slips After 60", URL: "https://lp.test/a", Image: "aaa", CTA: "", AdID: "ah-3"},
	}
	set := network.Settings{Brand: "Health Daily", CPC: 0.3, DailyCap: 20, Countries: []string{"US"}, TrackingCode: "sub1={campaign_id}"}
	for _, dev := range []network.Device{network.Desktop, network.Mobile} {
		m, err := tb.CreateCampaign(context.Background(), "acme-sc", network.NewCampaign{Name: "Pair · " + string(dev), GroupID: "44", Device: dev, Settings: set, Ads: ads}, up)
		if err != nil {
			t.Fatal(err)
		}
		if m.Campaign.Device != dev || m.Campaign.GroupID != "44" || len(m.Ads) != 3 || m.Ads[0].Active {
			t.Fatalf("%s: %+v", dev, m)
		}
	}
	if b.uploads != 2 {
		t.Errorf("uploaded %d images for two campaigns with two pictures", b.uploads)
	}
	made := b.bodies["POST acme-sc/campaigns/"]
	if len(made) != 2 {
		t.Fatalf("campaigns made: %d", len(made))
	}
	// The team's mobile campaign is phones and tablets; desktop is desktop only.
	for i, want := range []string{"[DESK]", "[PHON TBLT]"} {
		pt := made[i]["platform_targeting"].(map[string]any)
		if v := pt["value"].([]any); fmt.Sprint(v) != want || pt["type"] != "INCLUDE" {
			t.Errorf("campaign %d targets %v %v, want %s", i, pt["type"], v, want)
		}
		if made[i]["is_active"] != false {
			t.Errorf("campaign %d sent with is_active %v", i, made[i]["is_active"])
		}
		if made[i]["campaign_group_id"] != "44" {
			t.Errorf("campaign %d group %v", i, made[i]["campaign_group_id"])
		}
	}
	var items []any
	for k, v := range b.bodies {
		if strings.HasSuffix(k, "/items/mass") {
			for _, body := range v {
				items = append(items, body["collection"].([]any)...)
			}
		}
	}
	if len(items) != 6 {
		t.Fatalf("items sent: %d", len(items))
	}
	first := items[0].(map[string]any)
	if first["is_active"] != false || first["cta"].(map[string]any)["cta_type"] != "LEARN_MORE" {
		t.Errorf("first item %v", first)
	}
	if items[1].(map[string]any)["ai_disclosure"] == nil {
		t.Error("AI ad sent without the AI label")
	}
	if _, ok := items[2].(map[string]any)["cta"]; ok {
		t.Error("an ad without a button got one")
	}
}

// A new group is made paused and with no end date: Launch leaves end_date
// out, so Taboola gives it its default, 9999-12-31 ("no end date").
func TestGroupRunsWithNoEnd(t *testing.T) {
	tb, b := adapter(t)
	g, err := tb.CreateGroup(context.Background(), "acme-sc", network.NewGroup{Name: "07", Objective: "ONLINE_PURCHASES"})
	if err != nil || g.ID == "" {
		t.Fatalf("%+v %v", g, err)
	}
	sent := b.bodies["POST acme-sc/campaigns_group/"]
	if len(sent) != 1 || sent[0]["is_active"] != false || sent[0]["spending_limit_model"] != "NONE" {
		t.Fatalf("group sent %v", sent)
	}
	if _, ok := sent[0]["end_date"]; ok {
		t.Errorf("group sent with an end date: %v", sent[0]["end_date"])
	}
}

func TestCopyIntoGroupBringsAdsPaused(t *testing.T) {
	tb, b := adapter(t)
	m, err := tb.Copy(context.Background(), "acme-sc", "77", network.CopyTo{Name: "Moved", GroupID: "55"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Campaign.GroupID != "55" || len(m.Ads) != 2 || m.Ads[0].AdID != "ah-1" || m.Ads[1].Active {
		t.Fatalf("%+v", m)
	}
	dup := b.bodies["POST acme-sc/campaigns/77/duplicate/"]
	if len(dup) != 1 || dup[0]["campaign_group_id"] != "55" || dup[0]["name"] != "Moved" || fmt.Sprint(dup[0]["duplicate_settings"]) != "map[include_items:true]" {
		t.Fatalf("duplicate sent %v", dup)
	}
	var paused []string
	for _, c := range b.calls {
		if strings.HasPrefix(c, "POST ") && strings.Contains(c, "/items/") {
			paused = append(paused, c)
		}
	}
	if len(paused) != 1 || !strings.HasSuffix(paused[0], "/items/32/") {
		t.Errorf("paused %v, want only the running ad 32", paused)
	}
}

func TestDevice(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want network.Device
	}{
		{[]string{"DESK"}, network.Desktop},
		{[]string{"PHON"}, network.Mobile},
		{[]string{"PHON", "TBLT"}, network.Mobile},
		{[]string{"DESK", "PHON"}, network.Both},
		{nil, network.Both},
	} {
		if got := device(c.in); got != c.want {
			t.Errorf("device(%v) = %s, want %s", c.in, got, c.want)
		}
	}
	if CTAType("Learn More") != "LEARN_MORE" || CTAType("Get Now") != "GET_NOW" || CTAType("") != "" {
		t.Error("CTAType")
	}
}
