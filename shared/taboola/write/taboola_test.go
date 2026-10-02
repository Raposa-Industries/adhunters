package write

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/keep"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

const secret = "s3cret-never-kept"

// seen is one API request that reached the fake.
type seen struct {
	Method, Path, Auth, Type string
	Body                     []byte
}

// fake answers like Backstage. Tokens are tok-1, tok-2…; answer decides
// every other request.
type fake struct {
	srv    *httptest.Server
	tokens atomic.Int32
	expiry int // expires_in given with each token

	mu   sync.Mutex
	reqs []seen
}

func newFake(t *testing.T, answer func(w http.ResponseWriter, r *http.Request, body []byte)) *fake {
	t.Helper()
	f := &fake{expiry: 43200}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			_ = r.ParseForm()
			if r.Form.Get("client_secret") != secret || r.Form.Get("grant_type") != "client_credentials" {
				w.WriteHeader(401)
				return
			}
			n := f.tokens.Add(1)
			fmt.Fprintf(w, `{"access_token":"tok-%d","token_type":"bearer","expires_in":%d}`, n, f.expiry)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, seen{r.Method, strings.TrimPrefix(r.URL.Path, apiPrefix), r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
		f.mu.Unlock()
		answer(w, r, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) seen() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.reqs...)
}

func client(t *testing.T, base string) (*Client, string) {
	t.Helper()
	return clientWith(t, Settings{Base: base})
}

// clientWith fills the credentials, accounts and ceilings into s.
func clientWith(t *testing.T, s Settings) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	s.ClientID, s.ClientSecret = "id", secret
	s.Accounts = []string{"acme-sc", " ", "big-network"}
	s.MaxCPC, s.MaxDailyCap = 1, 100
	c, err := New(s, keep.New(dir), quiet)
	if err != nil {
		t.Fatal(err)
	}
	c.wait = func(context.Context, time.Duration) error { return nil }
	return c, dir
}

func mustNew(t *testing.T, s Settings) *Client {
	t.Helper()
	c, err := New(s, nil, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// kept reads every exchange in the keep folder.
func kept(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, "-taboola.json") {
			b, _ := os.ReadFile(p)
			out = append(out, string(b))
		}
		return nil
	})
	return out
}

var ctx = context.Background()

func campaignsAnswer(w http.ResponseWriter, r *http.Request, body []byte) {
	io.WriteString(w, `{"results":[
		{"id":"101","name":"One","status":"RUNNING","is_active":true,"branding_text":"Health","cpc":0.3,"daily_cap":20,"spending_limit":null,"bid_strategy":"FIXED","tracking_code":"s={site}"},
		{"id":"102","name":"Gone","status":"TERMINATED","is_active":false}]}`)
}

func TestTokenCachedAndRefreshedOn401(t *testing.T) {
	var reject atomic.Bool
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if reject.Swap(false) {
			w.WriteHeader(401)
			io.WriteString(w, `{"message":"token expired"}`)
			return
		}
		campaignsAnswer(w, r, body)
	})
	c, _ := client(t, f.srv.URL)
	for range 2 {
		if _, err := c.Campaigns(ctx, "acme-sc"); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.tokens.Load(); n != 1 {
		t.Fatalf("token fetched %d times for two calls", n)
	}
	reject.Store(true)
	list, err := c.Campaigns(ctx, "acme-sc")
	if err != nil {
		t.Fatal(err)
	}
	if n := f.tokens.Load(); n != 2 {
		t.Fatalf("token fetched %d times after a 401", n)
	}
	s := f.seen()
	if last := s[len(s)-1]; last.Auth != "Bearer tok-2" {
		t.Errorf("repeat sent %q", last.Auth)
	}
	if len(list) != 1 || !reflect.DeepEqual(list[0], Campaign{ID: "101", Name: "One", Status: "RUNNING", IsActive: true, BrandingText: "Health", CPC: 0.3, DailyCap: 20, BidStrategy: "FIXED", TrackingCode: "s={site}"}) {
		t.Errorf("%+v", list)
	}
}

func TestTokenNearExpiryIsRefetched(t *testing.T) {
	f := newFake(t, campaignsAnswer)
	f.expiry = 30 // under a minute: never trusted
	c, _ := client(t, f.srv.URL)
	for range 2 {
		if _, err := c.Campaigns(ctx, "acme-sc"); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.tokens.Load(); n != 2 {
		t.Fatalf("token fetched %d times", n)
	}
}

func TestGuardRefusesBeforeSending(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		t.Errorf("request reached Taboola: %s %s", r.Method, r.URL.Path)
	})
	c, _ := client(t, f.srv.URL)
	good := NewCampaign{Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10}
	item := []NewItem{{URL: "https://example.com", Title: "t", ThumbnailURL: "https://cdn/x.jpg"}}
	for name, fn := range map[string]func() error{
		"network campaigns": func() error { _, err := c.Campaigns(ctx, "big-network"); return err },
		"unlisted list":     func() error { _, err := c.Campaigns(ctx, "other-sc"); return err },
		"unlisted create":   func() error { _, err := c.CreateCampaign(ctx, "other-sc", good); return err },
		"network create":    func() error { _, err := c.CreateCampaign(ctx, "big-network", good); return err },
		"network items":     func() error { _, err := c.MassCreateItems(ctx, "big-network", "1", item); return err },
		"unlisted items":    func() error { _, err := c.MassCreateItems(ctx, "other-sc", "1", item); return err },
		"campaign path":     func() error { _, err := c.MassCreateItems(ctx, "acme-sc", "1/../../x", item); return err },
		"raw path":          func() error { _, err := c.do(ctx, call{method: "GET", path: "other-sc/campaigns/"}); return err },
		"raw network":       func() error { _, err := c.do(ctx, call{method: "POST", path: "big-network/campaigns/"}); return err },
		"no account path":   func() error { _, err := c.do(ctx, call{method: "GET", path: "resources"}); return err },
	} {
		var r *Refused
		if err := fn(); !errors.As(err, &r) || r.Message == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := f.tokens.Load(); n != 0 || len(f.seen()) != 0 {
		t.Errorf("%d tokens, %d requests reached the fake", n, len(f.seen()))
	}
}

func TestAccountsNamedWhenReadable(t *testing.T) {
	var fail atomic.Bool
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path != apiPrefix+allowedAccounts {
			t.Errorf("unexpected %s", r.URL.Path)
		}
		if fail.Load() {
			w.WriteHeader(403)
			io.WriteString(w, `{"message":"no permission"}`)
			return
		}
		io.WriteString(w, `{"results":[{"id":1,"account_id":"acme-sc","name":"Acme SC"},{"id":2,"account_id":"big-network","name":"Big"}]}`)
	})
	c, _ := client(t, f.srv.URL)
	got, err := c.Accounts(ctx)
	if err != nil || fmt.Sprint(got) != "[{acme-sc Acme SC}]" {
		t.Fatalf("%v %v", got, err)
	}
	fail.Store(true)
	c2, _ := client(t, f.srv.URL)
	got, err = c2.Accounts(ctx)
	if err != nil || fmt.Sprint(got) != "[{acme-sc acme-sc}]" {
		t.Fatalf("fallback: %v %v", got, err)
	}
}

func TestAvailableAndWhy(t *testing.T) {
	var nilClient *Client
	for name, tc := range map[string]struct {
		c    *Client
		want string
	}{
		"nil":          {nilClient, "falta TABOOLA_CLIENT_ID, TABOOLA_CLIENT_SECRET ou TABOOLA_ACCOUNTS"},
		"no secret":    {mustNew(t, Settings{ClientID: "id", Accounts: []string{"a-sc"}}), "falta TABOOLA_CLIENT_SECRET no servidor"},
		"only network": {mustNew(t, Settings{ClientID: "id", ClientSecret: "s", Accounts: []string{"a-network"}}), "falta TABOOLA_ACCOUNTS no servidor"},
		"on":           {mustNew(t, Settings{ClientID: "id", ClientSecret: "s", Accounts: []string{"a-sc"}}), ""},
	} {
		if got := tc.c.Why(); (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q", name, got)
		}
		if tc.c.Available() != (tc.want == "") {
			t.Errorf("%s: available %v", name, tc.c.Available())
		}
	}
}

func TestCampaignBody(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		var b map[string]any
		_ = json.Unmarshal(body, &b)
		b["id"] = "555"
		b["status"] = "PENDING_APPROVAL"
		_ = json.NewEncoder(w).Encode(b)
	})
	c, _ := client(t, f.srv.URL)
	for _, tc := range []struct {
		name  string
		in    NewCampaign
		check func(b map[string]any) string
	}{
		{"stopped, no budget, defaults", NewCampaign{Name: " Joint ", Brand: "Health Digest", CPC: 0.25, DailyCap: 20}, func(b map[string]any) string {
			_, hasLimit := b["spending_limit"]
			_, hasTC := b["tracking_code"]
			_, hasGroup := b["campaign_group_id"]
			_, hasDate := b["start_date"]
			switch {
			case hasGroup || hasDate:
				return "group or date sent unset"
			case b["is_active"] != false:
				return "is_active"
			case b["spending_limit_model"] != "NONE" || hasLimit:
				return "spending limit"
			case b["name"] != "Joint" || b["branding_text"] != "Health Digest" || b["bid_strategy"] != "FIXED" || b["cpc"] != 0.25 || b["daily_cap"] != 20.0:
				return "basics"
			case b["marketing_objective"] != "DRIVE_WEBSITE_TRAFFIC" || b["daily_ad_delivery_model"] != "STRICT":
				return "objective or delivery"
			case fmt.Sprint(b["country_targeting"]) != "map[type:INCLUDE value:[US]]" || fmt.Sprint(b["platform_targeting"]) != "map[type:INCLUDE value:[DESK PHON]]":
				return "targeting"
			case hasTC:
				return "tracking code sent empty"
			}
			return ""
		}},
		{"total budget, targeting, group, dates, smart", NewCampaign{Name: "x", Brand: "b", CPC: 1, DailyCap: 100, SpendingLimit: 3000, Countries: []string{"us", "BR"}, Platforms: []string{"phon"}, TrackingCode: "utm_source=taboola&s={site}",
			MarketingObjective: "leads_generation", BidStrategy: "SMART", StartDate: "2026-10-01", EndDate: "2026-10-01", GroupID: "4242"}, func(b map[string]any) string {
			switch {
			case b["is_active"] != false:
				return "is_active"
			case b["marketing_objective"] != "LEADS_GENERATION" || b["bid_strategy"] != "SMART":
				return "objective or bid"
			case b["start_date"] != "2026-10-01" || b["end_date"] != "2026-10-01":
				return "dates"
			case b["campaign_group_id"] != "4242":
				return "group"
			case b["spending_limit_model"] != "ENTIRE" || b["spending_limit"] != 3000.0:
				return "spending limit"
			case fmt.Sprint(b["country_targeting"]) != "map[type:INCLUDE value:[US BR]]" || fmt.Sprint(b["platform_targeting"]) != "map[type:INCLUDE value:[PHON]]":
				return "targeting"
			case b["tracking_code"] != "utm_source=taboola&s={site}":
				return "tracking code"
			}
			return ""
		}},
		{"max conversions, cities, delivery", NewCampaign{Name: "x", Brand: "b", DailyCap: 100, BidStrategy: "max_conversions", ExcludeCities: []string{" 3 ", "2132"}, AdDelivery: "optimized"}, func(b map[string]any) string {
			_, hasCPC := b["cpc"]
			switch {
			case b["bid_strategy"] != "MAX_CONVERSIONS" || hasCPC:
				return "bid"
			case fmt.Sprint(b["city_targeting"]) != "map[type:EXCLUDE value:[3 2132]]":
				return "cities"
			case b["traffic_allocation_mode"] != "OPTIMIZED":
				return "delivery"
			case b["marketing_objective"] != "ONLINE_PURCHASES":
				return "objective"
			}
			return ""
		}},
		{"target cpa", NewCampaign{Name: "x", Brand: "b", DailyCap: 100, BidStrategy: "MAX_CONVERSIONS", TargetCPA: 35}, func(b map[string]any) string {
			if b["bid_strategy"] != "TARGET_CPA" || b["target_cpa"] != 35.0 {
				return "target cpa"
			}
			return ""
		}},
	} {
		before := len(f.seen())
		cp, err := c.CreateCampaign(ctx, "acme-sc", tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		s := f.seen()
		if len(s) != before+1 || s[len(s)-1].Path != "acme-sc/campaigns/" || s[len(s)-1].Method != "POST" {
			t.Fatalf("%s: %+v", tc.name, s)
		}
		var b map[string]any
		_ = json.Unmarshal(s[len(s)-1].Body, &b)
		if what := tc.check(b); what != "" {
			t.Errorf("%s: %s wrong in %v", tc.name, what, b)
		}
		if cp.ID != "555" || cp.Status != "PENDING_APPROVAL" {
			t.Errorf("%s: %+v", tc.name, cp)
		}
	}

	before := len(f.seen())
	for name, in := range map[string]NewCampaign{
		"no name":          {Brand: "b", CPC: 0.1, DailyCap: 10},
		"no brand":         {Name: "x", CPC: 0.1, DailyCap: 10},
		"long brand":       {Name: "x", Brand: strings.Repeat("b", 26), CPC: 0.1, DailyCap: 10},
		"zero cpc":         {Name: "x", Brand: "b", DailyCap: 10},
		"cpc over":         {Name: "x", Brand: "b", CPC: 1.01, DailyCap: 10},
		"daily over total": {Name: "x", Brand: "b", CPC: 0.5, DailyCap: 50, SpendingLimit: 20},
		"cap over":         {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 100.5},
		"zero cap":         {Name: "x", Brand: "b", CPC: 0.1},
		"budget over":      {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, SpendingLimit: 3000.01},
		"budget minus":     {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, SpendingLimit: -1},
		"country":          {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, Countries: []string{"USA"}},
		"platform":         {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, Platforms: []string{"TV"}},
		"tracking code":    {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, TrackingCode: "a b"},
		"objective":        {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, MarketingObjective: "APP_INSTALLS"},
		"bid":              {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, BidStrategy: "TARGET_ROAS"},
		"cpc with maxconv": {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, BidStrategy: "MAX_CONVERSIONS"},
		"maxconv traffic":  {Name: "x", Brand: "b", DailyCap: 10, BidStrategy: "MAX_CONVERSIONS", MarketingObjective: "DRIVE_WEBSITE_TRAFFIC"},
		"cpa without":      {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, TargetCPA: 20},
		"delivery":         {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, AdDelivery: "FAST"},
		"empty city":       {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, ExcludeCities: []string{" "}},
		"city by name":     {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, ExcludeCities: []string{"Atlanta"}},
		"smart over":       {Name: "x", Brand: "b", CPC: 1.5, DailyCap: 10, BidStrategy: "SMART"},
		"bad date":         {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, StartDate: "01/10/2026"},
		"end first":        {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, StartDate: "2026-10-02", EndDate: "2026-10-01"},
		"group":            {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, GroupID: "12a"},
	} {
		var r *Refused
		if _, err := c.CreateCampaign(ctx, "acme-sc", in); !errors.As(err, &r) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.seen()) != before {
		t.Errorf("a refused campaign reached Taboola")
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUploadSendsTheImagesOwnType(t *testing.T) {
	pic := testPNG(t)
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		part, err := mr.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(part)
		if part.FormName() != "file" || part.FileName() != "a.png" || part.Header.Get("Content-Type") != "image/png" || !bytes.Equal(got, pic) {
			t.Errorf("part %q %q %q, %d bytes", part.FormName(), part.FileName(), part.Header.Get("Content-Type"), len(got))
		}
		io.WriteString(w, `{"value":"https://cdn.taboola.com/libtrc/static/thumbnails/a.png"}`)
	})
	c, dir := client(t, f.srv.URL)
	u, err := c.UploadImage(ctx, "../../a.png", pic)
	if err != nil || u != "https://cdn.taboola.com/libtrc/static/thumbnails/a.png" {
		t.Fatalf("%q %v", u, err)
	}
	if s := f.seen(); len(s) != 1 || s[0].Path != uploadPath || s[0].Method != "POST" {
		t.Errorf("%+v", s)
	}
	k := kept(t, dir)
	if len(k) != 1 || !strings.Contains(k[0], fmt.Sprintf(`"request": "(image a.png, %d bytes)"`, len(pic))) || strings.Contains(k[0], "PNG") {
		t.Errorf("kept %v", k)
	}
}

func item(i int) NewItem {
	return NewItem{URL: "https://example.com/lp", Title: fmt.Sprintf("Title %d", i), ThumbnailURL: "https://cdn/x.jpg", CustomID: fmt.Sprintf("ah-%d", i)}
}

func massAnswer(w http.ResponseWriter, r *http.Request, body []byte) {
	var b struct {
		Collection []map[string]any `json:"collection"`
	}
	_ = json.Unmarshal(body, &b)
	var rows []map[string]any
	for i, it := range b.Collection {
		rows = append(rows, map[string]any{"id": fmt.Sprint(1000 + i), "title": it["title"], "status": "PENDING_APPROVAL", "custom_data": it["custom_data"], "is_active": it["is_active"]})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"results": rows})
}

func TestMassCreateBodyAndChunks(t *testing.T) {
	f := newFake(t, massAnswer)
	c, _ := client(t, f.srv.URL)
	items := make([]NewItem, 120)
	for i := range items {
		items[i] = item(i)
	}
	items[0].CTA, items[0].AI, items[0].Description = "LEARN_MORE", true, "  More here "
	items[1].CTA = "NONE"
	made, err := c.MassCreateItems(ctx, "acme-sc", "777", items)
	if err != nil || len(made) != 120 {
		t.Fatalf("%d %v", len(made), err)
	}
	if made[0] != (Item{ID: "1000", Title: "Title 0", Status: "PENDING_APPROVAL", CustomID: "ah-0", Paused: true}) {
		t.Errorf("%+v", made[0])
	}
	s := f.seen()
	if len(s) != 3 {
		t.Fatalf("%d calls", len(s))
	}
	for i, want := range []int{50, 50, 20} {
		var b struct {
			Collection []map[string]any `json:"collection"`
		}
		if err := json.Unmarshal(s[i].Body, &b); err != nil || s[i].Path != "acme-sc/campaigns/777/items/mass" || s[i].Type != "application/json" {
			t.Fatalf("call %d: %s %v", i, s[i].Path, err)
		}
		if len(b.Collection) != want {
			t.Errorf("call %d: %d items", i, len(b.Collection))
		}
		if i != 0 {
			continue
		}
		first, second := b.Collection[0], b.Collection[1]
		if fmt.Sprint(first) != "map[ai_disclosure:map[status:AI_GENERATED] cta:map[cta_type:LEARN_MORE] custom_data:map[custom_id:ah-0] description:More here is_active:false thumbnail_url:https://cdn/x.jpg title:Title 0 url:https://example.com/lp]" {
			t.Errorf("first: %v", first)
		}
		if fmt.Sprint(second) != "map[custom_data:map[custom_id:ah-1] is_active:false thumbnail_url:https://cdn/x.jpg title:Title 1 url:https://example.com/lp]" {
			t.Errorf("second: %v", second)
		}
	}
}

func TestMassCreateChecksEveryItemFirst(t *testing.T) {
	f := newFake(t, massAnswer)
	c, _ := client(t, f.srv.URL)
	for name, mod := range map[string]func(*NewItem){
		"empty title": func(it *NewItem) { it.Title = " " },
		"macro":       func(it *NewItem) { it.URL = "https://example.com/?c={campaign_id}" },
		"not a link":  func(it *NewItem) { it.URL = "example.com" },
		"custom id":   func(it *NewItem) { it.CustomID = strings.Repeat("x", 31) },
		"cta":         func(it *NewItem) { it.CTA = "learn more" },
		"no image":    func(it *NewItem) { it.ThumbnailURL = "" },
	} {
		items := []NewItem{item(0), item(1)}
		mod(&items[1])
		var r *Refused
		if _, err := c.MassCreateItems(ctx, "acme-sc", "777", items); !errors.As(err, &r) || !strings.HasPrefix(r.Message, "anúncio 2: ") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.seen()) != 0 {
		t.Errorf("refused items reached Taboola")
	}
}

func TestMassCreateKeepsWhatWasMadeBeforeAFailure(t *testing.T) {
	var calls atomic.Int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if calls.Add(1) == 2 {
			w.WriteHeader(400)
			io.WriteString(w, `{"http_status":400,"message":"Title is too\nlong","offending_field":"title"}`)
			return
		}
		massAnswer(w, r, body)
	})
	c, _ := client(t, f.srv.URL)
	items := make([]NewItem, 120)
	for i := range items {
		items[i] = item(i)
	}
	made, err := c.MassCreateItems(ctx, "acme-sc", "777", items)
	var e *Error
	if !errors.As(err, &e) || e.Status != 400 || e.Message != "lote 2 de 3: a Taboola recusou (HTTP 400): Title is too long (campo title)" {
		t.Fatalf("%v", err)
	}
	if len(made) != 50 || calls.Load() != 2 {
		t.Errorf("%d made, %d calls", len(made), calls.Load())
	}
}

func TestRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status []int // answers before a 200
		create bool
		calls  int
		ok     bool
	}{
		{"read repeats a 5xx", []int{503, 502}, false, 3, true},
		{"read gives up after 3 repeats", []int{500, 500, 500, 500}, false, 4, false},
		{"create repeats a 429", []int{429}, true, 2, true},
		{"create never repeats a 5xx", []int{500}, true, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
				if i := int(n.Add(1)) - 1; i < len(tc.status) {
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(tc.status[i])
					io.WriteString(w, `{"message":"busy"}`)
					return
				}
				if r.Method == "POST" {
					io.WriteString(w, `{"id":"9","status":"RUNNING","is_active":false}`)
					return
				}
				campaignsAnswer(w, r, body)
			})
			c, _ := client(t, f.srv.URL)
			var waits []time.Duration
			c.wait = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
			var err error
			if tc.create {
				_, err = c.CreateCampaign(ctx, "acme-sc", NewCampaign{Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10})
			} else {
				_, err = c.Campaigns(ctx, "acme-sc")
			}
			if (err == nil) != tc.ok || int(n.Load()) != tc.calls {
				t.Fatalf("err %v after %d calls", err, n.Load())
			}
			for _, d := range waits {
				if d != 7*time.Second {
					t.Errorf("waited %v, not Retry-After", d)
				}
			}
			var e *Error
			if tc.create && !tc.ok && (!errors.As(err, &e) || !strings.Contains(e.Message, "confira no Taboola antes de repetir")) {
				t.Errorf("create 5xx message: %v", err)
			}
		})
	}
}

func TestEveryExchangeKeptWithoutSecrets(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/items/mass"):
			massAnswer(w, r, body)
		case r.URL.Path == apiPrefix+uploadPath:
			io.WriteString(w, `{"value":"https://cdn/x.png"}`)
		case r.Method == "POST":
			io.WriteString(w, `{"id":"9","is_active":false}`)
		case strings.HasSuffix(r.URL.Path, "allowed-accounts/"):
			w.WriteHeader(500)
			io.WriteString(w, "oops, not json")
		default:
			campaignsAnswer(w, r, body)
		}
	})
	c, dir := client(t, f.srv.URL)
	c.wait = func(context.Context, time.Duration) error { return nil }
	if _, err := c.Accounts(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Campaigns(ctx, "acme-sc"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateCampaign(ctx, "acme-sc", NewCampaign{Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UploadImage(ctx, "a.png", testPNG(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MassCreateItems(ctx, "acme-sc", "9", []NewItem{item(0)}); err != nil {
		t.Fatal(err)
	}
	k := kept(t, dir)
	if len(k) != len(f.seen()) {
		t.Fatalf("%d kept for %d requests", len(k), len(f.seen()))
	}
	all := strings.Join(k, "\n")
	for _, bad := range []string{"tok-", secret, "Bearer", "client_secret"} {
		if strings.Contains(all, bad) {
			t.Errorf("keep folder holds %q", bad)
		}
	}
	for _, want := range []string{`"path": "acme-sc/campaigns/"`, `"status": 200`, `"body": "oops, not json"`, `"collection": [`, `"branding_text": "b"`} {
		if !strings.Contains(all, want) {
			t.Errorf("keep folder lacks %s", want)
		}
	}
}

func TestKeepFailureIsAnError(t *testing.T) {
	f := newFake(t, campaignsAnswer)
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o644)
	c, err := New(Settings{Base: f.srv.URL, ClientID: "id", ClientSecret: secret, Accounts: []string{"acme-sc"}, MaxCPC: 1, MaxDailyCap: 1},
		keep.New(filepath.Join(file, "under-a-file")), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Campaigns(ctx, "acme-sc"); !errors.Is(err, ErrKeep) {
		t.Fatalf("%v", err)
	}
}

func TestItemsTaboolaLeftActiveArePaused(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/items/mass"):
			// Taboola ignored is_active false on two of three.
			io.WriteString(w, `{"results":[{"id":"1","is_active":true},{"id":"2","is_active":false},{"id":"3"}]}`)
		case strings.HasSuffix(r.URL.Path, "/items/3/"):
			w.WriteHeader(400)
			io.WriteString(w, `{"message":"item is locked"}`)
		default:
			var b map[string]any
			_ = json.Unmarshal(body, &b)
			b["status"] = "PENDING_APPROVAL"
			_ = json.NewEncoder(w).Encode(b)
		}
	})
	c, _ := client(t, f.srv.URL)
	made, err := c.MassCreateItems(ctx, "acme-sc", "777", []NewItem{item(0), item(1), item(2)})
	var e *Error
	if !errors.As(err, &e) || e.Message != "anúncio 3 criado mas não pausado: a Taboola recusou (HTTP 400): item is locked" {
		t.Fatalf("%v", err)
	}
	if len(made) != 3 || !made[0].Paused || !made[1].Paused || made[2].Paused {
		t.Errorf("%+v", made)
	}
	var pauses []string
	for _, s := range f.seen() {
		if strings.Contains(s.Path, "/items/") && !strings.HasSuffix(s.Path, "/mass") {
			if string(s.Body) != `{"is_active":false}` || s.Method != "POST" {
				t.Errorf("pause %s %s", s.Method, s.Body)
			}
			pauses = append(pauses, s.Path)
		}
	}
	if fmt.Sprint(pauses) != "[acme-sc/campaigns/777/items/1/ acme-sc/campaigns/777/items/3/]" {
		t.Errorf("paused %v", pauses)
	}
}

func TestDuplicateCampaign(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		io.WriteString(w, `{"id":"888","name":"copy","is_active":false,"status":"PENDING_APPROVAL"}`)
	})
	c, _ := client(t, f.srv.URL)
	cp, err := c.DuplicateCampaign(ctx, "acme-sc", "101", NewCampaign{Name: "copy", CPC: 0.2, TrackingCode: "s={site}"})
	if err != nil || cp.ID != "888" || cp.IsActive {
		t.Fatalf("%+v %v", cp, err)
	}
	s := f.seen()
	if len(s) != 1 || s[0].Path != "acme-sc/campaigns/101/duplicate/" {
		t.Fatalf("%+v", s)
	}
	var b map[string]any
	_ = json.Unmarshal(s[0].Body, &b)
	if fmt.Sprint(b) != "map[cpc:0.2 duplicate_settings:map[include_items:false] is_active:false name:copy tracking_code:s={site}]" {
		t.Errorf("%v", b)
	}
	var r *Refused
	if _, err := c.DuplicateCampaign(ctx, "acme-sc", "101", NewCampaign{Name: "copy", CPC: 5}); !errors.As(err, &r) {
		t.Errorf("cpc over the ceiling in a copy: %v", err)
	}
	if _, err := c.DuplicateCampaign(ctx, "acme-sc", "10x", NewCampaign{Name: "copy"}); !errors.As(err, &r) {
		t.Errorf("bad source id: %v", err)
	}
	if len(f.seen()) != 1 {
		t.Errorf("a refused copy reached Taboola")
	}
	// A copy into another group, with its ads, is how a campaign moves.
	if _, err := c.DuplicateCampaign(ctx, "acme-sc", "101", NewCampaign{Name: "moved", GroupID: "7", WithAds: true}); err != nil {
		t.Fatal(err)
	}
	s = f.seen()
	b = nil
	_ = json.Unmarshal(s[len(s)-1].Body, &b)
	if b["campaign_group_id"] != "7" || b["is_active"] != false || fmt.Sprint(b["duplicate_settings"]) != "map[include_items:true]" {
		t.Errorf("copy into group 7 sent %v", b)
	}
}

func TestGroups(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == "GET" {
			io.WriteString(w, `{"results":[{"id":"7","name":"G","status":"RUNNING","spending_limit":500,"spending_limit_model":"MONTHLY"},{"id":"8","name":"old","status":"TERMINATED"}]}`)
			return
		}
		var b map[string]any
		_ = json.Unmarshal(body, &b)
		b["id"] = "9"
		_ = json.NewEncoder(w).Encode(b)
	})
	c, _ := client(t, f.srv.URL)
	list, err := c.Groups(ctx, "acme-sc")
	if err != nil || fmt.Sprint(list) != "[{7 G RUNNING 500 MONTHLY }]" {
		t.Fatalf("%v %v", list, err)
	}
	g, err := c.CreateGroup(ctx, "acme-sc", NewGroup{Name: "G2", SpendingLimit: 300, Model: "entire"})
	if err != nil || g.ID != "9" {
		t.Fatalf("%+v %v", g, err)
	}
	s := f.seen()
	if last := s[len(s)-1]; last.Path != "acme-sc/campaigns_group/" ||
		string(last.Body) != `{"is_active":false,"marketing_objective":"DRIVE_WEBSITE_TRAFFIC","name":"G2","spending_limit":300,"spending_limit_model":"ENTIRE"}` {
		t.Errorf("%s %s", last.Path, last.Body)
	}
	if _, err := c.CreateGroup(ctx, "acme-sc", NewGroup{Name: "G3", Model: "none"}); err != nil {
		t.Fatal(err)
	}
	if last := f.seen()[len(f.seen())-1]; string(last.Body) != `{"is_active":false,"marketing_objective":"DRIVE_WEBSITE_TRAFFIC","name":"G3","spending_limit_model":"NONE"}` {
		t.Errorf("no budget: %s", last.Body)
	}
	before := len(f.seen())
	for name, n := range map[string]NewGroup{
		"no name":   {SpendingLimit: 10, Model: "MONTHLY"},
		"no budget": {Name: "g", Model: "MONTHLY"},
		"too much":  {Name: "g", SpendingLimit: 3001, Model: "MONTHLY"},
		"model":     {Name: "g", SpendingLimit: 10, Model: "WEEKLY"},
		"none, sum": {Name: "g", SpendingLimit: 10, Model: "NONE"},
		"daily":     {Name: "g", SpendingLimit: 10, Model: "DAILY"},
	} {
		var r *Refused
		if _, err := c.CreateGroup(ctx, "acme-sc", n); !errors.As(err, &r) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.seen()) != before {
		t.Errorf("a refused group reached Taboola")
	}
}

// ownFake is a Backstage account with the owner's campaign 101 and group 7
// in it, and whatever this client creates (ids from 500).
func ownFake(t *testing.T) *fake {
	var next atomic.Int32
	next.Store(500)
	return newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		p := strings.TrimPrefix(r.URL.Path, apiPrefix)
		switch {
		case r.Method == "GET" && p == "acme-sc/campaigns/":
			io.WriteString(w, `{"results":[{"id":"101","name":"Owner's","status":"RUNNING"},{"id":"501","name":"AH-TEST mine","status":"PAUSED"}]}`)
		case r.Method == "GET" && p == "acme-sc/campaigns_group/":
			io.WriteString(w, `{"results":[{"id":"7","name":"Owner's group"},{"id":"502","name":"AH-TEST g"}]}`)
		case strings.HasSuffix(p, "/items/mass"):
			massAnswer(w, r, body)
		default:
			fmt.Fprintf(w, `{"id":"%d","is_active":false}`, next.Add(1))
		}
	})
}

func TestOnlyOwn(t *testing.T) {
	f := ownFake(t)
	state := filepath.Join(t.TempDir(), "sub", "taboola-state.json")
	c, _ := clientWith(t, Settings{Base: f.srv.URL, OnlyOwn: true, NamePrefix: "AH-TEST", StateFile: state})

	// Refused before any request.
	for name, fn := range map[string]func() error{
		"campaign name": func() error {
			_, err := c.CreateCampaign(ctx, "acme-sc", NewCampaign{Name: "Real one", Brand: "b", CPC: 0.1, DailyCap: 10})
			return err
		},
		"owner's group": func() error {
			_, err := c.CreateCampaign(ctx, "acme-sc", NewCampaign{Name: "AH-TEST x", Brand: "b", CPC: 0.1, DailyCap: 10, GroupID: "7"})
			return err
		},
		"group name": func() error {
			_, err := c.CreateGroup(ctx, "acme-sc", NewGroup{Name: "Budget", SpendingLimit: 10, Model: "MONTHLY"})
			return err
		},
		"copy owner's": func() error {
			_, err := c.DuplicateCampaign(ctx, "acme-sc", "101", NewCampaign{Name: "AH-TEST copy"})
			return err
		},
		"copy name": func() error {
			_, err := c.DuplicateCampaign(ctx, "acme-sc", "501", NewCampaign{Name: "copy"})
			return err
		},
		"ads in owner's": func() error {
			_, err := c.MassCreateItems(ctx, "acme-sc", "101", []NewItem{item(0)})
			return err
		},
		"network": func() error { _, err := c.Campaigns(ctx, "big-network"); return err },
		"raw path": func() error {
			_, err := c.do(ctx, call{method: "POST", path: "acme-sc/campaigns/101/items/1/", body: []byte("{}")})
			return err
		},
		"raw other": func() error { _, err := c.do(ctx, call{method: "GET", path: "acme-sc/reports/x"}); return err },
	} {
		var r *Refused
		if err := fn(); !errors.As(err, &r) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := len(f.seen()); n != 0 {
		t.Fatalf("%d refused requests reached Taboola: %+v", n, f.seen())
	}

	// Nothing is ours yet: the owner's campaigns and groups are not shown.
	if list, err := c.Campaigns(ctx, "acme-sc"); err != nil || len(list) != 0 {
		t.Fatalf("%v %v", list, err)
	}
	g, err := c.CreateGroup(ctx, "acme-sc", NewGroup{Name: "AH-TEST g", SpendingLimit: 10, Model: "MONTHLY"})
	if err != nil || g.ID != "501" {
		t.Fatalf("%+v %v", g, err)
	}
	cp, err := c.CreateCampaign(ctx, "acme-sc", NewCampaign{Name: "AH-TEST mine", Brand: "b", CPC: 0.1, DailyCap: 10, GroupID: g.ID})
	if err != nil || cp.ID != "502" {
		t.Fatalf("%+v %v", cp, err)
	}
	// The fake lists 501 as a campaign and 502 as a group: ids are kept by
	// kind, so neither shows.
	if list, _ := c.Campaigns(ctx, "acme-sc"); len(list) != 0 {
		t.Errorf("campaigns %v", list)
	}
	if list, _ := c.Groups(ctx, "acme-sc"); len(list) != 0 {
		t.Errorf("groups %v", list)
	}
	if _, err := c.MassCreateItems(ctx, "acme-sc", cp.ID, []NewItem{item(0)}); err != nil {
		t.Errorf("ads in our own campaign: %v", err)
	}
	cp2, err := c.DuplicateCampaign(ctx, "acme-sc", cp.ID, NewCampaign{Name: "AH-TEST copy"})
	if err != nil {
		t.Fatalf("copy of our own: %v", err)
	}

	// The state survives a restart and only it is listed.
	b, _ := os.ReadFile(state)
	var st ownState
	if err := json.Unmarshal(b, &st); err != nil || fmt.Sprint(st.Campaigns) != fmt.Sprintf("map[502:acme-sc %s:acme-sc]", cp2.ID) || fmt.Sprint(st.Groups) != "map[501:acme-sc]" {
		t.Fatalf("state %s %v", b, err)
	}
	st.Campaigns["501"] = "acme-sc"
	st.Groups["502"] = "acme-sc"
	b, _ = json.Marshal(st)
	_ = os.WriteFile(state, b, 0o640)
	c2, _ := clientWith(t, Settings{Base: f.srv.URL, OnlyOwn: true, NamePrefix: "AH-TEST", StateFile: state})
	if list, err := c2.Campaigns(ctx, "acme-sc"); err != nil || len(list) != 1 || list[0].ID != "501" {
		t.Errorf("campaigns after restart %v %v", list, err)
	}
	if list, err := c2.Groups(ctx, "acme-sc"); err != nil || len(list) != 1 || list[0].ID != "502" {
		t.Errorf("groups after restart %v %v", list, err)
	}
	if on, prefix := c2.OnlyOwn(); !on || prefix != "AH-TEST" {
		t.Errorf("OnlyOwn %v %q", on, prefix)
	}
}

func TestOnlyOwnNeedsState(t *testing.T) {
	if _, err := New(Settings{OnlyOwn: true, NamePrefix: "AH"}, nil, quiet); err == nil {
		t.Errorf("only-own without a state file accepted")
	}
	// The prefix is optional: without one, any name goes, and the state
	// file still keeps the client to what it made.
	c, err := New(Settings{OnlyOwn: true, StateFile: filepath.Join(t.TempDir(), "s.json")}, nil, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.checkOwnName("Tinnitus launch", "da campanha"); err != nil {
		t.Errorf("no prefix set, name refused: %v", err)
	}
	if err := c.checkOwnCampaign("acme-sc", "123"); err == nil {
		t.Errorf("a campaign not made here was allowed")
	}
	bad := filepath.Join(t.TempDir(), "state.json")
	_ = os.WriteFile(bad, []byte("{"), 0o640)
	if _, err := New(Settings{OnlyOwn: true, NamePrefix: "AH", StateFile: bad}, nil, quiet); err == nil {
		t.Errorf("broken state file accepted")
	}
}

func TestAdsPauseAndChange(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		p := strings.TrimPrefix(r.URL.Path, apiPrefix)
		switch {
		case r.Method == "GET" && p == "acme-sc/campaigns/101/items/":
			io.WriteString(w, `{"results":[
				{"id":"1","title":"T","url":"https://x.test/a","thumbnail_url":"https://cdn.test/1.jpg","cta":{"cta_type":"READ_MORE"},"custom_data":{"custom_id":"ah-1-2"},"ai_disclosure":{"status":"AI_GENERATED"},"status":"PAUSED","approval_state":"APPROVED","is_active":false},
				{"id":"2","status":"TERMINATED"}]}`)
		case r.Method == "POST" && p == "acme-sc/campaigns/101/":
			var b map[string]any
			_ = json.Unmarshal(body, &b)
			b["id"] = "101"
			if _, ok := b["is_active"]; !ok {
				b["is_active"] = false
			}
			_ = json.NewEncoder(w).Encode(b)
		default:
			io.WriteString(w, `{"id":"9","is_active":false}`)
		}
	})
	c, _ := client(t, f.srv.URL)
	ads, err := c.Ads(ctx, "acme-sc", "101")
	if err != nil || len(ads) != 1 {
		t.Fatalf("%v %v", ads, err)
	}
	if a := ads[0]; a.CTA != "READ_MORE" || a.CustomID != "ah-1-2" || !a.AI || a.Approval != "APPROVED" {
		t.Errorf("%+v", a)
	}
	if it := ads[0].NewItem(); it.ThumbnailURL != "https://cdn.test/1.jpg" || it.Check() != nil {
		t.Errorf("%+v", it)
	}
	if err := c.PauseCampaign(ctx, "acme-sc", "101"); err != nil {
		t.Fatal(err)
	}
	if err := c.PauseAd(ctx, "acme-sc", "101", "x"); err == nil {
		t.Error("a bad item id was sent")
	}
	cp, err := c.ChangeCampaign(ctx, "acme-sc", "101", Change{CPC: 0.35, DailyCap: 20})
	if err != nil || cp.CPC != 0.35 {
		t.Fatalf("%+v %v", cp, err)
	}
	s := f.seen()
	if last := string(s[len(s)-1].Body); last != `{"cpc":0.35,"daily_cap":20}` {
		t.Errorf("change sent %s", last)
	}
	before := len(f.seen())
	for name, ch := range map[string]Change{
		"nothing":     {},
		"cpc":         {CPC: 2},
		"cap":         {DailyCap: 500},
		"cap > total": {DailyCap: 50, SpendingLimit: 40},
	} {
		var r *Refused
		if _, err := c.ChangeCampaign(ctx, "acme-sc", "101", ch); !errors.As(err, &r) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.seen()) != before {
		t.Error("a refused change reached Taboola")
	}
}

func TestSettingsFromEnv(t *testing.T) {
	env := map[string]string{"TABOOLA_ACCOUNTS": " a-sc, ,b-sc", "TABOOLA_ONLY_OWN": "1", "TABOOLA_MAX_CPC": "0.5", "TABOOLA_CREATE_ACTIVE": "1"}
	s, err := SettingsFromEnv(func(k string) string { return env[k] }, "/x/state.json")
	if err != nil || fmt.Sprint(s.Accounts) != "[a-sc b-sc]" || !s.OnlyOwn || !s.CreateActive || s.MaxCPC != 0.5 || s.MaxDailyCap != 20 || s.MaxSpendLimit != 20 || s.StateFile != "/x/state.json" || s.Base != DefaultBase {
		t.Fatalf("%+v %v", s, err)
	}
	// 0 is no spending limit; the other ceilings must stay above 0.
	env = map[string]string{"TABOOLA_MAX_SPEND_LIMIT": "0", "TABOOLA_MAX_DAILY_CAP": "500"}
	if s, err = SettingsFromEnv(func(k string) string { return env[k] }, ""); err != nil || s.MaxSpendLimit != 0 || s.MaxDailyCap != 500 {
		t.Fatalf("no spending limit: %+v %v", s, err)
	}
	for k, v := range map[string]string{"TABOOLA_ACCOUNTS": "z-network", "TABOOLA_ONLY_OWN": "yes", "TABOOLA_CREATE_ACTIVE": "on", "TABOOLA_MAX_DAILY_CAP": "0", "TABOOLA_MAX_CPC": "0", "TABOOLA_MAX_SPEND_LIMIT": "-1"} {
		if _, err := SettingsFromEnv(func(key string) string {
			if key == k {
				return v
			}
			return ""
		}, ""); err == nil {
			t.Errorf("%s=%s accepted", k, v)
		}
	}
}

// A campaign Taboola makes is checked: one that comes back running (or
// without saying) is paused at once, and a copy above the ceilings is
// brought down to them.
func TestNewCampaignSettled(t *testing.T) {
	answers := map[string]string{
		"acme-sc/campaigns/":               `{"id":"31","is_active":true,"daily_cap":10}`,
		"acme-sc/campaigns/101/duplicate/": `{"id":"32","daily_cap":900,"cpc":4,"bid_strategy":"FIXED"}`,
		"acme-sc/campaigns/102/duplicate/": `{"id":"33","is_active":false,"daily_cap":900,"bid_strategy":"MAX_CONVERSIONS"}`,
	}
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		p := strings.TrimPrefix(r.URL.Path, apiPrefix)
		if a, ok := answers[p]; ok {
			io.WriteString(w, a)
			return
		}
		var b map[string]any
		_ = json.Unmarshal(body, &b)
		b["id"] = strings.Split(strings.TrimPrefix(p, "acme-sc/campaigns/"), "/")[0]
		_ = json.NewEncoder(w).Encode(b)
	})
	c, _ := client(t, f.srv.URL)
	cp, err := c.CreateCampaign(ctx, "acme-sc", NewCampaign{Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10})
	if err != nil || cp.IsActive {
		t.Fatalf("%+v %v", cp, err)
	}
	cp, err = c.DuplicateCampaign(ctx, "acme-sc", "101", NewCampaign{Name: "copy"})
	if err != nil || cp.IsActive || cp.DailyCap != c.s.MaxDailyCap || cp.CPC != c.s.MaxCPC {
		t.Fatalf("%+v %v", cp, err)
	}
	if _, err := c.DuplicateCampaign(ctx, "acme-sc", "102", NewCampaign{Name: "copy"}); err != nil {
		t.Fatal(err)
	}
	var fixes []string
	for _, x := range f.seen() {
		if !strings.HasSuffix(x.Path, "/duplicate/") && x.Path != "acme-sc/campaigns/" {
			fixes = append(fixes, x.Path+" "+string(x.Body))
		}
	}
	want := []string{
		`acme-sc/campaigns/31/ {"is_active":false}`,
		fmt.Sprintf(`acme-sc/campaigns/32/ {"cpc":%v,"daily_cap":%v,"is_active":false}`, c.s.MaxCPC, c.s.MaxDailyCap),
		fmt.Sprintf(`acme-sc/campaigns/33/ {"daily_cap":%v,"is_active":false}`, c.s.MaxDailyCap),
	}
	if strings.Join(fixes, "\n") != strings.Join(want, "\n") {
		t.Errorf("fixes:\n%s\nwant:\n%s", strings.Join(fixes, "\n"), strings.Join(want, "\n"))
	}
}

// Nothing sent may turn a campaign or an ad on, however the body was built.
func TestNeverTurnsOn(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) { io.WriteString(w, `{"is_active":false}`) })
	c, _ := client(t, f.srv.URL)
	var r *Refused
	for _, body := range []any{
		obj{"is_active": true},
		obj{"collection": []any{obj{"title": "a", "is_active": false}, obj{"title": "b", "is_active": true}}},
		obj{"is_active": "true"},
	} {
		if _, err := c.sendJSON(ctx, http.MethodPost, "acme-sc/campaigns/101/", body, false); !errors.As(err, &r) {
			t.Errorf("%v: %v", body, err)
		}
	}
	if len(f.seen()) != 0 {
		t.Fatalf("a body that turns something on reached Taboola: %+v", f.seen())
	}
	if _, err := c.sendJSON(ctx, http.MethodPost, "acme-sc/campaigns/101/", obj{"is_active": false, "cpc": 0.3}, false); err != nil {
		t.Fatal(err)
	}
}

// The owner's rule (2026-10-01): a campaign never spends more than $20 in
// all. Every new campaign gets a total limit at or under the ceiling, a copy
// without one (or above it) is given one, and no change raises it.
func TestSpendCeiling(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		p := strings.TrimPrefix(r.URL.Path, apiPrefix)
		if strings.HasSuffix(p, "/duplicate/") {
			io.WriteString(w, `{"id":"41","is_active":false,"daily_cap":500,"spending_limit":0,"bid_strategy":"MAX_CONVERSIONS"}`)
			return
		}
		var b map[string]any
		_ = json.Unmarshal(body, &b)
		b["id"] = "40"
		_ = json.NewEncoder(w).Encode(b)
	})
	c, _ := clientWith(t, Settings{Base: f.srv.URL, MaxSpendLimit: 20})
	c.s.MaxDailyCap = 20
	var r *Refused
	for name, in := range map[string]NewCampaign{
		"total over":    {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10, SpendingLimit: 21},
		"daily over":    {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 25},
		"daily > total": {Name: "x", Brand: "b", CPC: 0.1, DailyCap: 20, SpendingLimit: 10},
	} {
		if _, err := c.CreateCampaign(ctx, "acme-sc", in); !errors.As(err, &r) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.seen()) != 0 {
		t.Fatalf("a campaign over the ceiling reached Taboola")
	}
	cp, err := c.CreateCampaign(ctx, "acme-sc", NewCampaign{Name: "x", Brand: "b", BidStrategy: "MAX_CONVERSIONS", DailyCap: 20})
	if err != nil || cp.SpendingLimit != 20 {
		t.Fatalf("no total asked: %+v %v", cp, err)
	}
	var b map[string]any
	_ = json.Unmarshal(f.seen()[0].Body, &b)
	if b["spending_limit_model"] != "ENTIRE" || b["spending_limit"] != 20.0 {
		t.Errorf("new campaign body %v", b)
	}
	cp, err = c.DuplicateCampaign(ctx, "acme-sc", "101", NewCampaign{Name: "copy"})
	if err != nil || cp.SpendingLimit != 20 || cp.DailyCap != 20 {
		t.Fatalf("copy: %+v %v", cp, err)
	}
	s := f.seen()
	b = nil
	_ = json.Unmarshal(s[len(s)-1].Body, &b)
	if s[len(s)-1].Path != "acme-sc/campaigns/41/" || fmt.Sprint(b) != "map[daily_cap:20 is_active:false spending_limit:20 spending_limit_model:ENTIRE]" {
		t.Errorf("copy fix %s %v", s[len(s)-1].Path, b)
	}
	if _, err := c.ChangeCampaign(ctx, "acme-sc", "101", Change{SpendingLimit: 50}); !errors.As(err, &r) {
		t.Errorf("change above the ceiling: %v", err)
	}
	if _, err := c.ChangeCampaign(ctx, "acme-sc", "101", Change{DailyCap: 21}); !errors.As(err, &r) {
		t.Errorf("daily change above the ceiling: %v", err)
	}
}

// With CreateActive a new campaign, group and ads go up running and are
// left so; a copy still arrives paused, and any other call that would turn
// something on is still refused.
func TestCreateActive(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		p := strings.TrimPrefix(r.URL.Path, apiPrefix)
		if strings.HasSuffix(p, "/items/mass") {
			massAnswer(w, r, body)
			return
		}
		var b map[string]any
		_ = json.Unmarshal(body, &b)
		switch p {
		case "acme-sc/campaigns/":
			b["id"] = "41"
		case "acme-sc/campaigns_group/":
			b["id"] = "g1"
		case "acme-sc/campaigns/101/duplicate/":
			b = map[string]any{"id": "42", "is_active": true}
		default:
			b["id"] = strings.Split(strings.TrimPrefix(p, "acme-sc/campaigns/"), "/")[0]
		}
		_ = json.NewEncoder(w).Encode(b)
	})
	c, _ := clientWith(t, Settings{Base: f.srv.URL, CreateActive: true})
	cp, err := c.CreateCampaign(ctx, "acme-sc", NewCampaign{Name: "x", Brand: "b", CPC: 0.1, DailyCap: 10})
	if err != nil || !cp.IsActive {
		t.Fatalf("%+v %v", cp, err)
	}
	if _, err := c.CreateGroup(ctx, "acme-sc", NewGroup{Name: "g", Model: "NONE"}); err != nil {
		t.Fatal(err)
	}
	made, err := c.MassCreateItems(ctx, "acme-sc", "41", []NewItem{item(0)})
	if err != nil || len(made) != 1 || made[0].Paused {
		t.Fatalf("%+v %v", made, err)
	}
	cp, err = c.DuplicateCampaign(ctx, "acme-sc", "101", NewCampaign{Name: "copy"})
	if err != nil || cp.IsActive {
		t.Fatalf("copy: %+v %v", cp, err)
	}
	var got []string
	for _, x := range f.seen() {
		var b map[string]any
		_ = json.Unmarshal(x.Body, &b)
		if coll, ok := b["collection"].([]any); ok {
			b = coll[0].(map[string]any)
		}
		got = append(got, fmt.Sprintf("%s %v", x.Path, b["is_active"]))
	}
	want := []string{"acme-sc/campaigns/ true", "acme-sc/campaigns_group/ true", "acme-sc/campaigns/41/items/mass true",
		"acme-sc/campaigns/101/duplicate/ false", "acme-sc/campaigns/42/ false"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sent:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var r *Refused
	if _, err := c.sendJSON(ctx, http.MethodPost, "acme-sc/campaigns/101/", obj{"is_active": true}, false); !errors.As(err, &r) {
		t.Errorf("turning on an existing campaign: %v", err)
	}
	if _, err := c.ChangeCampaign(ctx, "acme-sc", "101", Change{DailyCap: 5}); err != nil {
		t.Fatal(err)
	}
}
