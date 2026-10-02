package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/launch/internal/actions"
	"github.com/Raposa-Industries/adhunters/launch/internal/images"
	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	"github.com/Raposa-Industries/adhunters/launch/internal/network/fake"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
	"github.com/Raposa-Industries/adhunters/launch/internal/testdb"
)

const acct = "acme-sc"

func classify(err error) (int, string) {
	var r *network.Refused
	switch {
	case errors.As(err, &r):
		return http.StatusBadRequest, r.Message
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "não encontrado"
	}
	return http.StatusBadGateway, err.Error()
}

type rig struct {
	t   *testing.T
	srv *httptest.Server
	net *fake.Net
	l   *actions.Launch
	api *API
	db  *pgxpool.Pool
}

func setup(t *testing.T) *rig {
	t.Helper()
	db := testdb.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	n := fake.New("taboola", network.Account{ID: acct, Name: "Acme"})
	img := &images.Store{Dir: t.TempDir()}
	l := actions.New(store.New(db), img, log, func(err error) string { _, m := classify(err); return m }, n)
	a := New(context.Background(), l, img, log, classify)
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &rig{t: t, srv: srv, net: n, l: l, api: a, db: db}
}

// call sends a request as ana@team.test and decodes the answer into out.
func (r *rig) call(method, path string, body, out any) int {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, r.srv.URL+"/launch/api/"+path, rd)
	req.Header.Set("Cf-Access-Authenticated-User-Email", "ana@team.test")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			r.t.Fatalf("%s %s: %s", method, path, data)
		}
	}
	return res.StatusCode
}

func pngBytes(t *testing.T, shade uint8) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, 1200, 628))
	for x := 0; x < 1200; x += 7 {
		m.Set(x, 3, color.RGBA{shade, 10, 10, 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, m)
	return b.Bytes()
}

func (r *rig) upload(name string, data []byte) images.Info {
	r.t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	f, _ := w.CreateFormFile("image", name)
	f.Write(data)
	w.Close()
	res, err := http.Post(r.srv.URL+"/launch/api/images", w.FormDataContentType(), &b)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	var info images.Info
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil || res.StatusCode != 200 {
		r.t.Fatalf("upload %d %v", res.StatusCode, err)
	}
	return info
}

func (r *rig) waitJob(j job) job {
	r.t.Helper()
	for i := 0; i < 200 && !j.Done; i++ {
		time.Sleep(20 * time.Millisecond)
		r.call("GET", "jobs/"+j.ID, nil, &j)
	}
	if !j.Done {
		r.t.Fatal("send never finished")
	}
	return j
}

func TestNewPairInNewGroupWithPreset(t *testing.T) {
	r := setup(t)
	a := r.upload("one.png", pngBytes(t, 1))
	b := r.upload("two.png", pngBytes(t, 2))
	if a.Width != 1200 || a.SHA == b.SHA {
		t.Fatalf("%+v %+v", a, b)
	}

	var saved map[string]int64
	if code := r.call("POST", "presets", map[string]any{"level": "campaign", "name": "Memory US", "fields": map[string]any{"settings": map[string]any{"cpc": 0.3}}}, &saved); code != 200 {
		t.Fatalf("preset %d", code)
	}
	var dup map[string]string
	if code := r.call("POST", "presets", map[string]any{"level": "campaign", "name": "Memory US", "fields": map[string]any{}}, &dup); code != http.StatusConflict {
		t.Fatalf("same name twice: %d %v", code, dup)
	}
	var draft map[string]int64
	r.call("POST", "drafts", map[string]any{"account": acct, "name": "half done", "body": map[string]any{"name": "x"}}, &draft)

	pid := saved["id"]
	body := map[string]any{
		"key": "k1", "network": "taboola", "account": acct, "name": "Memory Loss US",
		"new_group": map[string]any{"name": "Memory Oct", "budget": 50, "budget_model": "MONTHLY"},
		"settings":  map[string]any{"cpc": 0.3, "daily_cap": 20, "countries": []string{"US"}},
		"ads":       []map[string]any{{"title": "Doctors Surprised By This Habit", "url": "https://lp.test", "image": a.SHA, "cta": "Learn More", "ad_id": "ah-1"}, {"title": "The Trick Seniors Use", "url": "https://lp.test", "image": b.SHA, "ad_id": "ah-2"}, {"title": "Why Memory Slips", "url": "https://lp.test", "image": a.SHA, "ad_id": "ah-3"}},
		"preset_id": pid,
		"draft_id":  draft["id"],
	}
	var j job
	if code := r.call("POST", "pairs", body, &j); code != http.StatusAccepted {
		t.Fatalf("send %d", code)
	}
	var again job
	r.call("POST", "pairs", body, &again)
	if again.ID != j.ID {
		t.Fatal("the same send key made a second send")
	}
	j = r.waitJob(j)
	res := j.Result
	if j.Error != "" || res.Result != "done" || res.Desktop == nil || res.Mobile == nil {
		t.Fatalf("%+v %+v", j, res)
	}
	if res.Desktop.Campaign.Name != "Memory Loss US · Desktop" || res.Mobile.Campaign.Device != network.Mobile || res.Desktop.Campaign.GroupID != res.Group.ID {
		t.Errorf("%+v / %+v", res.Desktop.Campaign, res.Mobile.Campaign)
	}
	if res.Desktop.Campaign.Status != "PAUSED" || res.Group.Status != "PAUSED" || len(res.Mobile.Ads) != 3 {
		t.Errorf("not all paused or ads missing: %+v", res)
	}
	if r.net.Uploaded != 2 {
		t.Errorf("uploaded %d images for 2 pictures in 2 campaigns", r.net.Uploaded)
	}

	var tree struct {
		Groups    []network.Group
		Campaigns []network.Campaign
		Pairs     []store.Pair
	}
	r.call("GET", "taboola/"+acct+"/tree", nil, &tree)
	if len(tree.Groups) != 1 || len(tree.Campaigns) != 2 || len(tree.Pairs) != 1 || tree.Pairs[0].MadeBy != "ana@team.test" || *tree.Pairs[0].PresetID != pid {
		t.Fatalf("%+v", tree)
	}
	var presets struct{ Presets []store.Preset }
	r.call("GET", "presets?account="+acct, nil, &presets)
	if len(presets.Presets) != 1 || presets.Presets[0].Used != 1 {
		t.Errorf("preset use not counted: %+v", presets)
	}
	var drafts struct{ Drafts []store.Draft }
	r.call("GET", "drafts", nil, &drafts)
	if len(drafts.Drafts) != 0 {
		t.Errorf("the draft stayed after its pair was made: %+v", drafts)
	}
	var hist struct{ History []store.Change }
	r.call("GET", "history", nil, &hist)
	kinds := map[string]int{}
	for _, c := range hist.History {
		kinds[c.Kind]++
	}
	if kinds["new_group"] != 1 || kinds["new_pair"] != 2 {
		t.Errorf("history %v", kinds)
	}

	var camp struct {
		Campaign network.Campaign
		Twin     *network.Campaign
		Ads      []network.Ad
		History  []store.Change
	}
	r.call("GET", "taboola/"+acct+"/campaigns/"+res.Desktop.Campaign.ID, nil, &camp)
	if camp.Twin == nil || camp.Twin.ID != res.Mobile.Campaign.ID || len(camp.Ads) != 3 || len(camp.History) == 0 {
		t.Errorf("%+v", camp)
	}
}

// A pair whose mobile half has its own bid, daily budget and start: the
// desktop gets the shared settings, the mobile its own, both paused, and
// each gets exactly the ads sent (the page's edited review), in order.
func TestPairMobileOwnSettingsAndExactAds(t *testing.T) {
	r := setup(t)
	g := r.net.AddGroup(acct, network.Group{Name: "01"})
	a := r.upload("one.png", pngBytes(t, 1))
	b := r.upload("two.png", pngBytes(t, 2))
	ads := []map[string]any{
		{"title": "Headline One With Image Two", "url": "https://lp.test", "image": b.SHA, "cta": "Learn More", "ad_id": "ah-1"},
		{"title": "A Headline Typed In Review", "url": "https://lp.test", "image": a.SHA, "cta": "Read More", "ad_id": "ah-2"},
	}
	var j job
	if code := r.call("POST", "pairs", map[string]any{
		"key": "own-mobile", "network": "taboola", "account": acct, "devices": "both", "group_id": g.ID,
		"settings": map[string]any{"brand": "B", "cpc": 0.3, "daily_cap": 20, "countries": []string{"US"}},
		"mobile":   map[string]any{"brand": "B", "cpc": 0.2, "daily_cap": 10, "start_date": "2030-01-05", "countries": []string{"US"}},
		"ads":      ads,
	}, &j); code != http.StatusAccepted {
		t.Fatalf("send %d", code)
	}
	res := r.waitJob(j).Result
	if res.Result != "done" || res.Desktop == nil || res.Mobile == nil {
		t.Fatalf("%+v", res)
	}
	d, m := res.Desktop.Campaign, res.Mobile.Campaign
	if d.Device != network.Desktop || d.Settings.CPC != 0.3 || d.Settings.DailyCap != 20 || d.Settings.StartDate != "" {
		t.Errorf("desktop %+v", d)
	}
	if m.Device != network.Mobile || m.Settings.CPC != 0.2 || m.Settings.DailyCap != 10 || m.Settings.StartDate != "2030-01-05" {
		t.Errorf("mobile %+v", m)
	}
	for _, made := range []*network.Made{res.Desktop, res.Mobile} {
		if made.Campaign.Status != "PAUSED" || made.Campaign.Active || len(made.Ads) != len(ads) {
			t.Fatalf("%+v", made)
		}
		for i, ad := range made.Ads {
			sha := ads[i]["image"].(string)
			if ad.Title != ads[i]["title"] || ad.CTA != ads[i]["cta"] || !strings.Contains(ad.ImageURL, sha[:12]) || ad.Active {
				t.Errorf("campaign %s ad %d: %+v, sent %v", made.Campaign.ID, i, ad, ads[i])
			}
		}
	}
}

func TestPairRefusedAndPartial(t *testing.T) {
	r := setup(t)
	var e map[string]string
	if code := r.call("POST", "pairs", map[string]any{"network": "taboola", "account": acct, "name": "x", "ads": []any{}}, &e); code != 400 || !strings.Contains(e["error"], "grupo") {
		t.Fatalf("%d %v", code, e)
	}
	g := r.net.AddGroup(acct, network.Group{Name: "Old"})
	a := r.upload("one.png", pngBytes(t, 1))
	r.net.Fail["CreateCampaign Broken · Mobile"] = &network.Refused{Message: "o Taboola recusou"}
	var j job
	r.call("POST", "pairs", map[string]any{"network": "taboola", "account": acct, "name": "Broken", "group_id": g.ID,
		"settings": map[string]any{"cpc": 0.2}, "ads": []map[string]any{{"title": "T", "url": "https://lp.test", "image": a.SHA}}}, &j)
	j = r.waitJob(j)
	if j.Result.Result != "partial" || j.Result.Desktop == nil || j.Result.Mobile != nil || len(j.Result.Problems) != 1 {
		t.Fatalf("%+v", j.Result)
	}
}

func TestMoveWaitsForCopyThenPausesOriginal(t *testing.T) {
	r := setup(t)
	from := r.net.AddGroup(acct, network.Group{Name: "From"})
	to := r.net.AddGroup(acct, network.Group{Name: "To"})
	c := r.net.AddCampaign(acct, network.Campaign{Name: "Runner", GroupID: from.ID, Status: "RUNNING", Active: true},
		network.Ad{Title: "An ad", Status: "RUNNING", Active: true})

	var out struct{ Done []actions.Done }
	if code := r.call("POST", "taboola/"+acct+"/move", map[string]any{"campaigns": []string{c.ID}, "to_group": to.ID}, &out); code != 200 {
		t.Fatalf("move %d", code)
	}
	cp := out.Done[0].Copy
	if out.Done[0].Error != "" || cp == nil || cp.GroupID != to.ID || cp.ID == c.ID || cp.Status != "PAUSED" || out.Done[0].Ads != 1 {
		t.Fatalf("%+v", out.Done)
	}
	var tree struct{ Moves []store.Move }
	r.call("GET", "taboola/"+acct+"/tree", nil, &tree)
	if len(tree.Moves) != 1 {
		t.Fatalf("moves waiting: %+v", tree.Moves)
	}

	// Nothing happens until someone starts the copy.
	if n, err := r.l.Watch(context.Background()); err != nil || n != 0 {
		t.Fatalf("watch before start: %d %v", n, err)
	}
	r.net.Start(acct, cp.ID)
	if n, err := r.l.Watch(context.Background()); err != nil || n != 1 {
		t.Fatalf("watch after start: %d %v", n, err)
	}
	orig, _ := r.net.Campaign(context.Background(), acct, c.ID)
	if orig.Active {
		t.Error("the original still runs after its copy started")
	}
	r.call("GET", "taboola/"+acct+"/tree", nil, &tree)
	if len(tree.Moves) != 0 {
		t.Errorf("move still waiting: %+v", tree.Moves)
	}
	var e map[string]string
	if code := r.call("POST", "taboola/"+acct+"/move", map[string]any{"campaigns": []string{cp.ID}, "to_group": to.ID}, &out); code != 200 || out.Done[0].Error == "" {
		t.Errorf("moving into its own group: %d %+v %v", code, out, e)
	}
}

func TestDuplicatePairPauseChange(t *testing.T) {
	r := setup(t)
	g := r.net.AddGroup(acct, network.Group{Name: "G"})
	d := r.net.AddCampaign(acct, network.Campaign{Name: "P · Desktop", GroupID: g.ID, Device: network.Desktop, Status: "RUNNING", Active: true, Settings: network.Settings{CPC: 0.2}})
	m := r.net.AddCampaign(acct, network.Campaign{Name: "P · Mobile", GroupID: g.ID, Device: network.Mobile, Status: "RUNNING", Active: true, Settings: network.Settings{CPC: 0.2}})
	if _, err := r.l.Store().AddPair(context.Background(), store.Pair{Network: "taboola", Account: acct, GroupID: g.ID, Name: "P", DesktopID: d.ID, MobileID: m.ID}); err != nil {
		t.Fatal(err)
	}
	var out struct{ Done []actions.Done }
	r.call("POST", "taboola/"+acct+"/duplicate", map[string]any{"campaigns": []string{d.ID, m.ID}}, &out)
	if len(out.Done) != 2 || out.Done[0].Copy.Name != "P · Desktop (cópia)" {
		t.Fatalf("%+v", out.Done)
	}
	pairs, _ := r.l.Store().Pairs(context.Background(), "taboola", acct)
	if len(pairs) != 2 || pairs[1].Name != "P (cópia)" && pairs[0].Name != "P (cópia)" {
		t.Errorf("copied pair not recorded: %+v", pairs)
	}

	r.call("POST", "taboola/"+acct+"/change", map[string]any{"campaigns": []string{d.ID, m.ID}, "change": map[string]any{"cpc": 0.25}}, &out)
	for _, id := range []string{d.ID, m.ID} {
		c, _ := r.net.Campaign(context.Background(), acct, id)
		if c.Settings.CPC != 0.25 {
			t.Errorf("%s cpc %v", id, c.Settings.CPC)
		}
	}
	var e map[string]string
	if code := r.call("POST", "taboola/"+acct+"/change", map[string]any{"campaigns": []string{d.ID, m.ID}, "change": map[string]any{"name": "same"}}, &e); code != 400 {
		t.Errorf("renaming two at once: %d %v", code, e)
	}

	r.net.Fail["Pause "+m.ID] = &network.Refused{Message: "não deu"}
	r.call("POST", "taboola/"+acct+"/pause", map[string]any{"campaigns": []string{d.ID, m.ID}}, &out)
	if out.Done[0].Error != "" || out.Done[1].Error != "não deu" {
		t.Fatalf("%+v", out.Done)
	}
	var hist struct{ History []store.Change }
	r.call("GET", "history?campaign="+m.ID, nil, &hist)
	if len(hist.History) == 0 || hist.History[0].Kind != "pause" || hist.History[0].Result != "failed" {
		t.Errorf("%+v", hist.History)
	}
}

func TestErrors(t *testing.T) {
	r := setup(t)
	var e map[string]string
	for _, c := range []struct {
		method, path string
		body         any
		code         int
	}{
		{"GET", "nope/" + acct + "/tree", nil, 400},
		{"GET", "taboola/" + acct + "/campaigns/404", nil, 502},
		{"POST", "presets", map[string]any{"level": "x", "name": "n", "fields": map[string]any{}}, 400},
		{"POST", "presets", map[string]any{"level": "group", "name": "n", "fields": map[string]any{}, "extra": 1}, 400},
		{"DELETE", "presets/99", nil, 404},
		{"GET", "drafts/99", nil, 404},
		{"GET", "jobs/nope", nil, 404},
		{"POST", "moves/99/cancel", nil, 400},
		{"GET", "unknown", nil, 404},
	} {
		if code := r.call(c.method, c.path, c.body, &e); code != c.code {
			t.Errorf("%s %s: %d %v, want %d", c.method, c.path, code, e, c.code)
		}
	}
	var st struct {
		User     string
		Networks []map[string]any
	}
	r.call("GET", "status", nil, &st)
	if st.User != "ana@team.test" || len(st.Networks) != 1 || st.Networks[0]["connected"] != true {
		t.Errorf("%+v", st)
	}
	var s struct{ Results []map[string]string }
	r.net.AddCampaign(acct, network.Campaign{Name: "Blood Pressure US"})
	r.call("GET", "search?q=blood", nil, &s)
	if len(s.Results) != 1 || !strings.Contains(s.Results[0]["href"], "/launch/taboola/"+acct+"/g/-/c/") {
		t.Errorf("%+v", s)
	}
}

func TestPauseAdsAskedByIntel(t *testing.T) {
	r := setup(t)
	g := r.net.AddGroup(acct, network.Group{Name: "G"})
	c := r.net.AddCampaign(acct, network.Campaign{Name: "C", GroupID: g.ID, Status: "RUNNING", Active: true},
		network.Ad{ID: "11", Title: "a", Active: true, Status: "RUNNING"},
		network.Ad{ID: "12", Title: "b", Active: true, Status: "RUNNING"},
		network.Ad{ID: "13", Title: "c", Active: true, Status: "RUNNING"})
	r.net.Fail["PauseAd 13"] = &network.Refused{Message: "não deu"}
	var out struct{ Done []actions.Done }
	r.call("POST", "taboola/"+acct+"/campaigns/"+c.ID+"/pause-ads?from=desk:step:311", map[string]any{"ads": []string{"11", "13"}}, &out)
	if len(out.Done) != 2 || out.Done[0].Error != "" || out.Done[1].Error != "não deu" {
		t.Fatalf("%+v", out.Done)
	}
	ads, _ := r.net.Ads(context.Background(), acct, c.ID)
	if ads[0].Active || !ads[1].Active || !ads[2].Active {
		t.Errorf("only ad 11 should be paused: %+v", ads)
	}
	var hist struct{ History []store.Change }
	r.call("GET", "history?campaign="+c.ID, nil, &hist)
	if len(hist.History) != 1 || hist.History[0].Result != "partial" || hist.History[0].AskedBy != "desk:step:311" || hist.History[0].Summary != "Pausou 1 anúncio de C" {
		t.Errorf("%+v", hist.History)
	}

	// A from that is not Intel's or Desk's is ignored: the person asked.
	r.call("POST", "taboola/"+acct+"/campaigns/"+c.ID+"/pause-ads?from=<b>x", map[string]any{"ads": []string{"12"}}, &out)
	r.call("GET", "history?campaign="+c.ID, nil, &hist)
	if hist.History[0].AskedBy != "ana@team.test" {
		t.Errorf("asked by %q", hist.History[0].AskedBy)
	}
}
