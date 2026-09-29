package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/intel/taboola"
)

func TestProbeSavesRawAndSummarises(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /backstage/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"secret-token","expires_in":43200}`))
	})
	mux.HandleFunc("GET /backstage/api/1.0/users/current/account", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"account_id":"acme-sc","currency":"USD","time_zone_name":"US/Eastern"}`))
	})
	mux.HandleFunc("GET /backstage/api/1.0/acme-sc/campaigns", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"id":"11","status":"PAUSED"},{"id":"22","status":"RUNNING"}]}`))
	})
	mux.HandleFunc("GET /backstage/api/1.0/acme-sc/campaigns/22/items/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"id":"9","title":"Doctors stunned"}]}`))
	})
	mux.HandleFunc("GET /backstage/api/1.0/acme-sc/reports/campaign-summary/dimensions/day", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("start_date") != "2026-09-23" || r.URL.Query().Get("end_date") != "2026-09-29" {
			t.Errorf("range %s", r.URL.RawQuery)
		}
		w.Header().Set("X-RateLimit-Remaining", "99")
		w.Write([]byte(`{"timezone":"EST","results":[{"date":"2026-09-28","clicks":10,"spent":2.5},{"date":"2026-09-29","clicks":5,"spent":1.5}]}`))
	})
	// Every other read answers 404, which the probe records and passes.
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	p := &probe{c: taboola.New(srv.URL, "id", "s"), dir: dir, days: 7, items: 1,
		now: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	if err := p.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	sum, _ := os.ReadFile(filepath.Join(dir, "summary.md"))
	for _, want := range []string{
		"Account `acme-sc`", "| items-22 | 200 | 1 |", "totals: clicks 15.00, spent 4.00",
		"`X-Ratelimit-Remaining`: 99", "timezone: EST", "| campaign-summary-site_breakdown | 404 |",
	} {
		if !strings.Contains(string(sum), want) {
			t.Errorf("summary lacks %q:\n%s", want, sum)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "raw", "campaigns.json"))
	if err != nil || !strings.Contains(string(raw), `"RUNNING"`) {
		t.Fatalf("raw campaigns: %s %v", raw, err)
	}
	// The token never reaches disk.
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if b, _ := os.ReadFile(path); strings.Contains(string(b), "secret-token") {
			t.Errorf("token written to %s", path)
		}
		return nil
	})
}
