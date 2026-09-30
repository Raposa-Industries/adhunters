package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Raposa-Industries/adhunters/create/internal/openai"
	"github.com/Raposa-Industries/adhunters/kit/keep"
	taboola "github.com/Raposa-Industries/adhunters/shared/taboola/write"
)

// backstage is a fake Taboola Backstage that records what reaches it.
type backstage struct {
	mu   sync.Mutex
	reqs []string // "METHOD path"
	body map[string][]byte
	ctyp map[string]string
	n    atomic.Int32
}

func (b *backstage) seen() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.reqs...)
}

// tbSetup is the API with a Taboola client over a fake Backstage. fail lists
// campaign ids whose mass create the fake refuses.
func tbSetup(t *testing.T, set taboola.Settings, fail ...string) (http.Handler, *backstage) {
	t.Helper()
	bs := &backstage{body: map[string][]byte{}, ctyp: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/backstage/oauth/token" {
			io.WriteString(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/backstage/api/1.0/")
		body, _ := io.ReadAll(r.Body)
		bs.mu.Lock()
		key := r.Method + " " + p
		bs.reqs = append(bs.reqs, key)
		if _, ok := bs.body[key]; !ok {
			bs.body[key] = body
		} else {
			bs.body[key] = append(append(bs.body[key], '\n'), body...)
		}
		bs.ctyp[key] = r.Header.Get("Content-Type")
		bs.mu.Unlock()
		switch {
		case p == "users/current/allowed-accounts/":
			io.WriteString(w, `{"results":[{"account_id":"acme-network","name":"Acme Network","type":"NETWORK"},{"account_id":"acme-sc","name":"Acme","type":"PARTNER"},{"account_id":"acme-2-sc","name":"Acme 2","type":"PARTNER"}]}`)
		case p == "operations/upload-image":
			fmt.Fprintf(w, `{"value":"https://cdn.taboola.com/img-%d.jpg"}`, bs.n.Add(1))
		case strings.HasSuffix(p, "/items/mass"):
			for _, id := range fail {
				if strings.Contains(p, "/"+id+"/") {
					w.WriteHeader(400)
					io.WriteString(w, `{"message":"campaign is terminated","offending_field":"campaign_id"}`)
					return
				}
			}
			var in struct {
				Collection []map[string]any `json:"collection"`
			}
			_ = json.Unmarshal(body, &in)
			var rows []map[string]any
			for _, it := range in.Collection {
				rows = append(rows, map[string]any{"id": fmt.Sprint(900 + bs.n.Add(1)), "title": it["title"], "status": "PENDING_APPROVAL", "custom_data": it["custom_data"], "is_active": false})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": rows})
		case r.Method == "GET" && strings.HasSuffix(p, "/campaigns_group/"):
			io.WriteString(w, `{"results":[{"id":"7","name":"G","status":"RUNNING","spending_limit":500,"spending_limit_model":"MONTHLY"}]}`)
		case r.Method == "GET":
			io.WriteString(w, `{"results":[{"id":"111","name":"One","status":"RUNNING","is_active":true,"branding_text":"B","cpc":0.2,"daily_cap":20,"bid_strategy":"FIXED","campaign_group_id":"7","marketing_objective":"DRIVE_WEBSITE_TRAFFIC","start_date":"2026-09-01"},{"id":"112","status":"TERMINATED"}]}`)
		default:
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			in["id"] = "333"
			_ = json.NewEncoder(w).Encode(in)
		}
	}))
	t.Cleanup(srv.Close)
	set.Base, set.ClientID, set.ClientSecret = srv.URL, "id", "secret"
	if set.Accounts == nil {
		set.Accounts = []string{"acme-sc"}
	}
	set.MaxCPC, set.MaxDailyCap = 1, 100
	tb, err := taboola.New(set, keep.New(t.TempDir()), quiet)
	if err != nil {
		t.Fatal(err)
	}
	ai := openai.New(settings("http://127.0.0.1:1", ""), nil, keep.New(t.TempDir()), quiet)
	return New(ai, quiet).WithTaboola(tb).Handler(), bs
}

func TestTaboolaStatusOff(t *testing.T) {
	ai := openai.New(settings("http://127.0.0.1:1", ""), nil, keep.New(t.TempDir()), quiet)
	h := New(ai, quiet).Handler() // no Taboola client at all
	rec, out := do(h, httptest.NewRequest("GET", "/api/taboola/status", nil))
	if rec.Code != 200 || out["connected"] != false || !strings.Contains(fmt.Sprint(out["reason"]), "TABOOLA_CLIENT_ID") ||
		fmt.Sprint(out["accounts"]) != "[]" || out["only_own"] != false {
		t.Fatalf("%d %v", rec.Code, out)
	}
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/api/taboola/campaigns?account=acme-sc", nil),
		httptest.NewRequest("GET", "/api/taboola/groups?account=acme-sc", nil),
		httptest.NewRequest("POST", "/api/taboola/campaigns", strings.NewReader(`{}`)),
		httptest.NewRequest("POST", "/api/taboola/groups", strings.NewReader(`{}`)),
		httptest.NewRequest("POST", "/api/taboola/ads", nil),
	} {
		if rec, out := do(h, req); rec.Code != 503 || !strings.Contains(fmt.Sprint(out["error"]), "Taboola não conectado") {
			t.Errorf("%s %s: %d %v", req.Method, req.URL, rec.Code, out)
		}
	}
	if rec, _ := do(h, httptest.NewRequest("GET", "/api/taboola/ads", nil)); rec.Code != 405 {
		t.Errorf("GET ads: %d", rec.Code)
	}
}

func TestTaboolaStatusOn(t *testing.T) {
	h, _ := tbSetup(t, taboola.Settings{})
	rec, out := do(h, httptest.NewRequest("GET", "/api/taboola/status", nil))
	if rec.Code != 200 || out["connected"] != true || fmt.Sprint(out["accounts"]) != "[map[id:acme-sc name:Acme]]" ||
		out["max_cpc"] != 1.0 || out["max_daily_cap"] != 100.0 || out["only_own"] != false {
		t.Fatalf("%d %v", rec.Code, out)
	}
	if _, ok := out["reason"]; ok {
		t.Errorf("reason while connected: %v", out)
	}
	if _, ok := out["name_prefix"]; ok {
		t.Errorf("name prefix without only-own: %v", out)
	}

	h, _ = tbSetup(t, taboola.Settings{OnlyOwn: true, NamePrefix: "AH-TEST", StateFile: t.TempDir() + "/state.json"})
	if rec, out := do(h, httptest.NewRequest("GET", "/api/taboola/status", nil)); out["only_own"] != true || out["name_prefix"] != "AH-TEST" {
		t.Fatalf("%d %v", rec.Code, out)
	}
}

func TestTaboolaCampaigns(t *testing.T) {
	h, bs := tbSetup(t, taboola.Settings{Accounts: []string{"acme-sc", "big-network"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/taboola/campaigns?account=acme-sc", nil))
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != 200 || err != nil || len(list) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	c := list[0]
	if c["id"] != "111" || c["is_active"] != true || c["cpc"] != 0.2 || c["campaign_group_id"] != "7" ||
		c["marketing_objective"] != "DRIVE_WEBSITE_TRAFFIC" || c["start_date"] != "2026-09-01" || c["end_date"] != "" || c["tracking_code"] != "" {
		t.Errorf("%v", c)
	}
	before := len(bs.seen())
	for _, acct := range []string{"big-network", "other-sc", ""} {
		if rec, out := do(h, httptest.NewRequest("GET", "/api/taboola/campaigns?account="+acct, nil)); rec.Code != 400 || out["error"] == nil {
			t.Errorf("%q: %d %v", acct, rec.Code, out)
		}
	}
	if len(bs.seen()) != before {
		t.Errorf("a refused account reached Taboola")
	}
}

func TestTaboolaCreateCampaignAndCopy(t *testing.T) {
	h, bs := tbSetup(t, taboola.Settings{})
	rec, out := do(h, httptest.NewRequest("POST", "/api/taboola/campaigns", strings.NewReader(
		`{"account":"acme-sc","name":"New","brand":"Health","cpc":0.3,"daily_cap":25,"spending_limit":0,"countries":["US"],"platforms":["PHON"],"tracking_code":"s={site}","marketing_objective":"ONLINE_PURCHASES","bid_strategy":"SMART","start_date":"2026-10-01","group_id":"7"}`)))
	if rec.Code != 200 || out["id"] != "333" || out["is_active"] != false {
		t.Fatalf("%d %v", rec.Code, out)
	}
	var sent map[string]any
	_ = json.Unmarshal(bs.body["POST acme-sc/campaigns/"], &sent)
	if sent["is_active"] != false || sent["bid_strategy"] != "SMART" || sent["marketing_objective"] != "ONLINE_PURCHASES" ||
		sent["campaign_group_id"] != "7" || sent["start_date"] != "2026-10-01" || sent["spending_limit_model"] != "NONE" {
		t.Errorf("%v", sent)
	}

	rec, out = do(h, httptest.NewRequest("POST", "/api/taboola/campaigns", strings.NewReader(`{"account":"acme-sc","name":"Copy","copy_from":"111","cpc":0.5}`)))
	if rec.Code != 200 || out["id"] != "333" {
		t.Fatalf("copy: %d %v", rec.Code, out)
	}
	if b := string(bs.body["POST acme-sc/campaigns/111/duplicate/"]); b != `{"cpc":0.5,"duplicate_settings":{"include_items":false},"is_active":false,"name":"Copy"}` {
		t.Errorf("copy body %s", b)
	}

	before := len(bs.seen())
	for name, body := range map[string]string{
		"not json": `{`,
		"cpc over": `{"account":"acme-sc","name":"x","brand":"b","cpc":2,"daily_cap":10}`,
		"no brand": `{"account":"acme-sc","name":"x","cpc":0.1,"daily_cap":10}`,
		"account":  `{"account":"x-network","name":"x","brand":"b","cpc":0.1,"daily_cap":10}`,
		"dates":    `{"account":"acme-sc","name":"x","brand":"b","cpc":0.1,"daily_cap":10,"start_date":"2026-10-02","end_date":"2026-10-01"}`,
		"copy id":  `{"account":"acme-sc","name":"x","copy_from":"abc"}`,
	} {
		if rec, out := do(h, httptest.NewRequest("POST", "/api/taboola/campaigns", strings.NewReader(body))); rec.Code != 400 || out["error"] == nil {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	if len(bs.seen()) != before {
		t.Errorf("a refused campaign reached Taboola: %v", bs.seen()[before:])
	}
}

func TestTaboolaGroups(t *testing.T) {
	h, bs := tbSetup(t, taboola.Settings{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/taboola/groups?account=acme-sc", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `[{"id":"7","name":"G","status":"RUNNING","spending_limit":500,"spending_limit_model":"MONTHLY","marketing_objective":""}]` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec, out := do(h, httptest.NewRequest("POST", "/api/taboola/groups", strings.NewReader(`{"account":"acme-sc","name":"G2","spending_limit":200,"spending_limit_model":"MONTHLY"}`)))
	if rec.Code != 200 || out["id"] != "333" {
		t.Fatalf("%d %v", rec.Code, out)
	}
	if b := string(bs.body["POST acme-sc/campaigns_group/"]); !strings.Contains(b, `"is_active":false`) || !strings.Contains(b, `"spending_limit_model":"MONTHLY"`) {
		t.Errorf("%s", b)
	}
	if rec, _ := do(h, httptest.NewRequest("POST", "/api/taboola/groups", strings.NewReader(`{"account":"acme-sc","name":"G3","spending_limit_model":"MONTHLY"}`))); rec.Code != 400 {
		t.Errorf("no budget: %d", rec.Code)
	}
}

func TestTaboolaWorkbook(t *testing.T) {
	h, _ := tbSetup(t, taboola.Settings{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/taboola/workbook", nil))
	want := `{"network":"acme-network","accounts":[{"id":"acme-sc","name":"Acme"},{"id":"acme-2-sc","name":"Acme 2"}],"groups":["G"]}`
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// adsForm builds a multipart ads request.
func adsForm(t *testing.T, fields map[string]string, images ...[]byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	for i, img := range images {
		part, _ := w.CreateFormFile("image", fmt.Sprintf("pic-%d.jpg", i))
		_, _ = part.Write(img)
	}
	_ = w.Close()
	req := httptest.NewRequest("POST", "/api/taboola/ads", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func TestTaboolaAdsEndToEnd(t *testing.T) {
	h, bs := tbSetup(t, taboola.Settings{}, "222")
	pic := testJPEG(t)
	req := adsForm(t, map[string]string{
		"account":   "acme-sc",
		"campaigns": `["111", 222, "111"]`,
		"ads": `[
			{"image":0,"title":"First headline","description":"More","cta":"learn_more","url":"https://example.com/a","custom_id":"ah-1","ai":true},
			{"image":1,"title":"Second headline","url":"https://example.com/a","custom_id":"ah-2"},
			{"image":0,"title":"Third headline","cta":"NONE","url":"https://example.com/a"}]`,
	}, pic, pic, pic) // the third image is used by no ad
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out adsReply
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ImagesUploaded != 2 || len(out.Results) != 2 {
		t.Fatalf("%+v", out)
	}
	ok, bad := out.Results[0], out.Results[1]
	if ok.CampaignID != "111" || ok.Error != "" || len(ok.Created) != 3 || ok.Created[0].CustomID != "ah-1" || !ok.Created[0].Paused {
		t.Errorf("111: %+v", ok)
	}
	if bad.CampaignID != "222" || len(bad.Created) != 0 || bad.Error != "a Taboola recusou (HTTP 400): campaign is terminated (campo campaign_id)" {
		t.Errorf("222: %+v", bad)
	}
	if !strings.Contains(rec.Body.String(), `"created":[]`) {
		t.Errorf("failed campaign's created is not an empty list: %s", rec.Body)
	}

	seen := bs.seen()
	if fmt.Sprint(seen) != "[POST operations/upload-image POST operations/upload-image POST acme-sc/campaigns/111/items/mass POST acme-sc/campaigns/222/items/mass]" {
		t.Errorf("%v", seen)
	}
	if ct := bs.ctyp["POST operations/upload-image"]; !strings.HasPrefix(ct, "multipart/form-data") {
		t.Errorf("upload content type %q", ct)
	}
	if !bytes.Contains(bs.body["POST operations/upload-image"], []byte("Content-Type: image/jpeg")) {
		t.Errorf("image part not labelled image/jpeg")
	}
	var mass struct {
		Collection []map[string]any `json:"collection"`
	}
	_ = json.Unmarshal(bs.body["POST acme-sc/campaigns/111/items/mass"], &mass)
	if len(mass.Collection) != 3 {
		t.Fatalf("%v", mass)
	}
	first, second, third := mass.Collection[0], mass.Collection[1], mass.Collection[2]
	if first["thumbnail_url"] != "https://cdn.taboola.com/img-1.jpg" || second["thumbnail_url"] != "https://cdn.taboola.com/img-2.jpg" || third["thumbnail_url"] != first["thumbnail_url"] {
		t.Errorf("thumbnails %v %v %v", first["thumbnail_url"], second["thumbnail_url"], third["thumbnail_url"])
	}
	if fmt.Sprint(first["cta"]) != "map[cta_type:LEARN_MORE]" || fmt.Sprint(first["ai_disclosure"]) != "map[status:AI_GENERATED]" || first["description"] != "More" || first["is_active"] != false {
		t.Errorf("first %v", first)
	}
	for _, k := range []string{"cta", "ai_disclosure", "description"} {
		if _, has := third[k]; has {
			t.Errorf("third has %s: %v", k, third)
		}
	}
	if _, has := third["custom_data"]; has {
		t.Errorf("third has custom_data without a custom id")
	}
}

func TestTaboolaAdsBadInput(t *testing.T) {
	h, bs := tbSetup(t, taboola.Settings{Accounts: []string{"acme-sc", "big-network"}})
	pic := testJPEG(t)
	ad := `[{"image":0,"title":"T","url":"https://example.com"}]`
	many := make([]string, 11)
	for i := range many {
		many[i] = fmt.Sprint(100 + i)
	}
	tooMany, _ := json.Marshal(many)
	big := append(append([]byte{}, pic...), make([]byte, 5<<20)...)
	for name, req := range map[string]*http.Request{
		"network":       adsForm(t, map[string]string{"account": "big-network", "campaigns": `["1"]`, "ads": ad}, pic),
		"unlisted":      adsForm(t, map[string]string{"account": "x-sc", "campaigns": `["1"]`, "ads": ad}, pic),
		"no campaigns":  adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `[]`, "ads": ad}, pic),
		"bad campaign":  adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `["1/../2"]`, "ads": ad}, pic),
		"11 campaigns":  adsForm(t, map[string]string{"account": "acme-sc", "campaigns": string(tooMany), "ads": ad}, pic),
		"no ads":        adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `["1"]`, "ads": `[]`}, pic),
		"ads not json":  adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `["1"]`, "ads": `{`}, pic),
		"no image":      adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `["1"]`, "ads": ad}),
		"bad index":     adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `["1"]`, "ads": `[{"image":1,"title":"T","url":"https://example.com"}]`}, pic),
		"missing index": adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `["1"]`, "ads": `[{"title":"T","url":"https://example.com"}]`}, pic),
		"macro":         adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `["1"]`, "ads": `[{"image":0,"title":"T","url":"https://example.com/?c={campaign_id}"}]`}, pic),
		"long title":    adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `["1"]`, "ads": `[{"image":0,"title":"` + strings.Repeat("t", 101) + `","url":"https://example.com"}]`}, pic),
		"not image":     adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `["1"]`, "ads": ad}, []byte("plain text, not a picture")),
		"too big":       adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `["1"]`, "ads": ad}, big),
		"not multipart": httptest.NewRequest("POST", "/api/taboola/ads", strings.NewReader(`{}`)),
	} {
		rec, out := do(h, req)
		if rec.Code != 400 || out["error"] == nil || out["error"] == "" {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	if n := len(bs.seen()); n != 0 {
		t.Errorf("%d requests reached Taboola: %v", n, bs.seen())
	}
}

func TestTaboolaAdsOnlyOwnRefusesOthersCampaigns(t *testing.T) {
	h, bs := tbSetup(t, taboola.Settings{OnlyOwn: true, NamePrefix: "AH-TEST", StateFile: t.TempDir() + "/state.json"})
	rec, out := do(h, adsForm(t, map[string]string{"account": "acme-sc", "campaigns": `["111"]`,
		"ads": `[{"image":0,"title":"T","url":"https://example.com"}]`}, testJPEG(t)))
	if rec.Code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "conta de testes") || len(bs.seen()) != 0 {
		t.Fatalf("%d %v %v", rec.Code, out, bs.seen())
	}
	// The owner's campaign 111 is not listed.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/taboola/campaigns?account=acme-sc", nil))
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("listed %s", rec.Body)
	}
	if rec, out := do(h, httptest.NewRequest("POST", "/api/taboola/campaigns", strings.NewReader(`{"account":"acme-sc","name":"Copy","copy_from":"111"}`))); rec.Code != 400 {
		t.Errorf("copy of the owner's: %d %v", rec.Code, out)
	}
}
