package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/spy/internal/testdb"
	"github.com/Raposa-Industries/adhunters/spy/numbers"
)

// now is real time: the range functions count up to the database's clock.
var now = time.Now().UTC().Truncate(time.Hour).Add(-30 * time.Minute)

type site struct {
	t  *testing.T
	db *pgxpool.Pool
	h  http.Handler
}

// newSite fills a test database with two creatives of one operator, seen
// today and over the last days, runs spy-numbers over it, and serves it.
func newSite(t *testing.T) *site {
	db := testdb.New(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	today := now.Truncate(24 * time.Hour)
	exec(`INSERT INTO tracks_api.publisher_v1 (id, name, domain) VALUES (1, 'Fox News', 'foxnews.com'), (2, 'MSN', 'msn.com')`)
	exec(`INSERT INTO spy.operator (id, name, display_name) VALUES (7, 'today55 · OP7', 'today55')`)
	exec(`INSERT INTO tracks_api.account_v1 (id, external_id) VALUES (500, 'today55-sc')`)
	exec(`INSERT INTO spy.account_operator VALUES (500, 7)`)
	exec(`INSERT INTO tracks_api.brand_v1 VALUES (1, 'SlimFast')`)
	for _, a := range []struct {
		creative, ad int
		head         string
	}{{10, 100, "Lose belly fat fast"}, {11, 110, "Doctors hate this trick"}} {
		exec(`INSERT INTO tracks_api.creative_v1 (id, creative_key, image_url, first_seen_at, last_seen_at) VALUES ($1, $2, 'https://img.example/x.jpg', $3, $4)`,
			a.creative, "ck", now.AddDate(0, 0, -20), now)
		exec(`INSERT INTO tracks_api.ad_v1 (id, creative_id, headline, account_id, brand_id, first_seen_at, last_seen_at) VALUES ($1, $2, $3, 500, 1, $4, $5)`,
			a.ad, a.creative, a.head, now.AddDate(0, 0, -20), now)
	}
	exec(`INSERT INTO spy.creative_class (creative_id, category_id, vertical_id, confidence, source, rules_hash, input_ad_id, classified_at, needs_model)
		VALUES (10, 'metabolism', 'weight-loss', 0.9, 'ad', '', 0, now(), false)`)
	for d := 0; d < 5; d++ {
		day := today.AddDate(0, 0, -d)
		for _, ad := range []int{100, 110} {
			exec(`INSERT INTO tracks_api.ad_daily_v1 (day, ad_id, publisher_id, device_id, creative_id, sightings, scrapes, first_seen_at, last_seen_at)
				SELECT $1::date, $2, 1, 2, creative_id, 10, 50, $1::timestamptz, LEAST($1::timestamptz + interval '20 hours', $3) FROM tracks_api.ad_v1 WHERE id = $2`,
				day, ad, now.Add(-time.Hour))
			exec(`INSERT INTO tracks_api.ad_account_daily_v1 (day, ad_id, account_id, brand_id, publisher_id, device_id, creative_id, sightings, first_seen_at, last_seen_at)
				SELECT $1::date, $2, 500, 1, 1, 2, creative_id, 10, $1::timestamptz, $1::timestamptz FROM tracks_api.ad_v1 WHERE id = $2`, day, ad)
		}
	}
	for h := 1; h <= 12; h++ {
		hr := now.Truncate(time.Hour).Add(-time.Duration(h) * time.Hour)
		for _, ad := range []int{100, 110} {
			exec(`INSERT INTO tracks_api.ad_hourly_v1 (hour, ad_id, publisher_id, device_id, sightings, scrapes, first_seen_at, last_seen_at)
				VALUES ($1, $2, 1, 2, 2, 10, $1, $1)`, hr, ad)
			exec(`INSERT INTO tracks_api.ad_account_brand_hourly_v1 (hour, ad_id, account_id, brand_id, publisher_id, device_id, sightings)
				VALUES ($1, $2, 500, 1, 1, 2, 2)`, hr, ad)
		}
		exec(`INSERT INTO tracks_api.scrape_coverage_v2 (hour, publisher_id, device_id, scrapes, sightings) VALUES ($1, 1, 2, 10, 4)`, hr)
		exec(`INSERT INTO tracks_api.closed_hour_v1 (hour, closed_at) VALUES ($1, $1)`, hr)
	}
	exec(`INSERT INTO tracks_api.link_v1 (id, host, path, tracker, sample_url) VALUES (1, 'slim.example', '/lp', 'redtrack', 'https://slim.example/lp')`)
	exec(`INSERT INTO tracks_api.creative_link_daily_v1 VALUES ($1::date, 10, 1, 7, $1::timestamptz, $1::timestamptz)`, today)
	exec(`INSERT INTO tracks_api.campaign_v1 (id, external_id, name, account_id) VALUES (1, 'c-1', 'Belly US', 500)`)
	exec(`INSERT INTO tracks_api.creative_campaign_daily_v1 VALUES ($1::date, 10, 1, 7, $1::timestamptz, $1::timestamptz)`, today)
	exec(`INSERT INTO tracks_api.auction_v1 (seen_at, ad_id, publisher_id, device_id, auction_id, clearing_price, bid_value, is_rtb)
		VALUES ($1, 100, 1, 2, 'a1', 0.25::real, 0.3::real, false)`, now.Add(-2*time.Hour))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := numbers.New(db, log, numbers.Config{Now: func() time.Time { return now }}, nil)
	// The read model first: the last 24 hours take each creative's
	// operator and days from it.
	if _, err := r.ReadModel(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.All(ctx, true); err != nil {
		t.Fatal(err)
	}
	s, err := New(db, slog.New(slog.NewTextHandler(tlog{t}, nil)), Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return &site{t: t, db: db, h: s.Handler()}
}

// tlog puts the server's log in the test's output.
type tlog struct{ t *testing.T }

func (l tlog) Write(b []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(b)))
	return len(b), nil
}

func (s *site) do(method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, req)
	return w
}

func (s *site) get(path string) map[string]any {
	s.t.Helper()
	w := s.do("GET", path, "")
	if w.Code != http.StatusOK {
		s.t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		s.t.Fatalf("GET %s: %v", path, err)
	}
	return out
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func TestAPI(t *testing.T) {
	s := newSite(t)

	ads := s.get("/spy/api/ads")
	items := list(ads["items"])
	if len(items) != 2 || ads["total"] != float64(2) {
		t.Fatalf("ads: %v", ads)
	}
	first := items[0].(map[string]any)
	if first["operator_name"] != "today55" || first["brand"] != "SlimFast" || len(list(first["series"])) != 14 {
		t.Errorf("first ad: %v", first)
	}
	if got := list(s.get("/spy/api/ads?vertical=weight-loss")["items"]); len(got) != 1 || got[0].(map[string]any)["vertical_name"] != "Weight Loss" {
		t.Errorf("weight-loss filter: %v", got)
	}
	if got := list(s.get("/spy/api/ads?q=doctors")["items"]); len(got) != 1 {
		t.Errorf("q filter: %v", got)
	}
	if got := list(s.get("/spy/api/ads?status=running")["items"]); len(got) != 2 {
		t.Errorf("running: %v", got)
	}
	day := now.Format("2006-01-02")
	if got := list(s.get("/spy/api/ads?from=" + now.AddDate(0, 0, -4).Format("2006-01-02") + "&to=" + day)["items"]); len(got) != 2 {
		t.Errorf("a range: %v", got)
	}

	ad := s.get("/spy/api/ads/10")
	if c := ad["creative"].(map[string]any); c["headline"] != "Lose belly fat fast" || c["vertical_name"] != "Weight Loss" {
		t.Errorf("ad head: %v", c)
	}
	for _, k := range []string{"ads", "publishers", "hours", "links", "campaigns", "prices", "series"} {
		if len(list(ad[k])) == 0 {
			t.Errorf("ad %s is empty", k)
		}
	}
	if r := ad["raposa"].(map[string]any); r["available"] != false {
		t.Errorf("raposa without its views: %v", r)
	}
	if p := list(ad["prices"])[0].(map[string]any); p["network"] != "Taboola" || p["auctions"] != float64(1) {
		t.Errorf("prices: %v", p)
	}

	ops := s.get("/spy/api/operators")
	if got := list(ops["items"]); len(got) != 1 || got[0].(map[string]any)["name"] != "today55" {
		t.Errorf("operators: %v", ops)
	}
	op := s.get("/spy/api/operators/7")
	if len(list(op["creatives"])) != 2 || len(list(op["accounts"])) != 1 || len(list(op["brands"])) != 1 {
		t.Errorf("operator: %v", op)
	}
	if got := list(s.get("/spy/api/publishers")["items"]); len(got) != 2 {
		t.Errorf("publishers: %v", got)
	}
	s.get("/spy/api/publishers/1")
	if got := list(s.get("/spy/api/pulse")["verticals"]); len(got) == 0 {
		t.Errorf("pulse has no verticals")
	}
	se := s.get("/spy/api/search?q=belly")
	if len(list(se["ads"])) != 1 {
		t.Errorf("search: %v", se)
	}
	if len(list(s.get("/spy/api/verticals")["categories"])) == 0 {
		t.Errorf("no verticals")
	}
	if len(list(s.get("/spy/api/facets")["publishers"])) != 1 {
		t.Errorf("facets")
	}
	if w := s.do("GET", "/spy/api/events?kind=creative", ""); w.Code != http.StatusOK {
		t.Errorf("events: %d %s", w.Code, w.Body.String())
	}

	for path, code := range map[string]int{
		"/spy/api/ads/999":             http.StatusNotFound,
		"/spy/api/ads?sort=nope":       http.StatusBadRequest,
		"/spy/api/ads?from=yesterday":  http.StatusBadRequest,
		"/spy/api/ads?from=2020-01-01": http.StatusBadRequest,
		"/spy/api/nothing":             http.StatusNotFound,
		"/spy/api/operators?network=x": http.StatusBadRequest,
	} {
		if w := s.do("GET", path, ""); w.Code != code {
			t.Errorf("%s: %d, want %d (%s)", path, w.Code, code, w.Body.String())
		}
	}
	// Only a JSON body from the same origin may ask for an investigation.
	if w := s.do("POST", "/spy/api/ads/10/investigate", "mode=deep", "Content-Type", "application/x-www-form-urlencoded"); w.Code != http.StatusBadRequest {
		t.Errorf("form post: %d", w.Code)
	}
	if w := s.do("POST", "/spy/api/ads/10/investigate", `{"mode":"deep"}`, "Content-Type", "application/json", "Origin", "https://evil.example"); w.Code != http.StatusBadRequest {
		t.Errorf("other origin: %d", w.Code)
	}
}

func TestPages(t *testing.T) {
	s := newSite(t)
	for path, want := range map[string]string{
		"/spy/":                 "ads.js",
		"/spy/ads/10":           "ad.js",
		"/spy/operators/":       "operators.js",
		"/spy/operators/7":      "operator.js",
		"/spy/publishers":       "publishers.js",
		"/spy/publishers/1":     "publisher.js",
		"/spy/pulse/":           "pulse.js",
		"/spy/assets/spy.js":    "mountFrame",
		"/spy/assets/spy.css":   ".sp-card",
		"/spy/_frame/frame.js":  "export function mountFrame",
		"/spy/_frame/frame.css": "--accent",
	} {
		w := s.do("GET", path, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d, missing %q", path, w.Code, want)
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: no CSP", path)
		}
	}
	if ct := s.do("GET", "/spy/assets/ads.js", "").Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("js type %q", ct)
	}
	for _, path := range []string{"/spy/assets/ads.html", "/spy/ads/x", "/spy/nothing"} {
		if w := s.do("GET", path, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}
	if w := s.do("GET", "/spy", ""); w.Code != http.StatusMovedPermanently {
		t.Errorf("/spy: %d", w.Code)
	}
}
