package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	"github.com/Raposa-Industries/adhunters/launch/internal/actions"
	"github.com/Raposa-Industries/adhunters/launch/internal/api"
	"github.com/Raposa-Industries/adhunters/launch/internal/images"
	"github.com/Raposa-Industries/adhunters/launch/internal/library"
	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	"github.com/Raposa-Industries/adhunters/launch/internal/network/fake"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
	"github.com/Raposa-Industries/adhunters/launch/internal/testdb"
	"github.com/Raposa-Industries/adhunters/shared/taboola/write"
)

// demo is launch-web over a fake Taboola with a few groups and campaigns.
func demo(t *testing.T) (http.Handler, *fake.Net, *actions.Launch) {
	t.Helper()
	db := testdb.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	n := fake.New("taboola", network.Account{ID: "acme-sc", Name: "Acme Health"}, network.Account{ID: "acme-two-sc", Name: "Acme Two"})
	mem := n.AddGroup("acme-sc", network.Group{Name: "Memory Loss US", Status: "RUNNING", Budget: 300, BudgetModel: "MONTHLY"})
	bp := n.AddGroup("acme-sc", network.Group{Name: "Blood Pressure US", Status: "PAUSED", Budget: 150, BudgetModel: "MONTHLY"})
	set := network.Settings{Brand: "Health Daily", CPC: 0.32, DailyCap: 25, Countries: []string{"US"}, TrackingCode: "sub1={campaign_id}&sub4={campaign_item_id}", Objective: "DRIVE_WEBSITE_TRAFFIC"}
	ad := network.Ad{Title: "Doctors Surprised By This Morning Habit", ImageURL: "", CTA: "LEARN_MORE", Status: "RUNNING", Approval: "APPROVED", Active: true, AdID: "ah-4f2a91c0de-77b1c2a9e0"}
	d := n.AddCampaign("acme-sc", network.Campaign{Name: "Memory Loss US · Desktop", GroupID: mem.ID, Device: network.Desktop, Status: "RUNNING", Active: true, Settings: set}, ad,
		network.Ad{Title: "The 10-Second Trick For Sharper Memory", CTA: "READ_MORE", Status: "RUNNING", Approval: "APPROVED", Active: true, AdID: "ah-4f2a91c0de-0c9d2e71aa"})
	m := n.AddCampaign("acme-sc", network.Campaign{Name: "Memory Loss US · Mobile", GroupID: mem.ID, Device: network.Mobile, Status: "RUNNING", Active: true, Settings: set}, ad)
	n.AddCampaign("acme-sc", network.Campaign{Name: "Memory old test", GroupID: mem.ID, Device: network.Both, Status: "PAUSED", Settings: set})
	n.AddCampaign("acme-sc", network.Campaign{Name: "BP Seniors · Desktop", GroupID: bp.ID, Device: network.Desktop, Status: "PENDING_APPROVAL", Settings: set})
	st := store.New(db)
	if _, err := st.AddPair(context.Background(), store.Pair{Network: "taboola", Account: "acme-sc", GroupID: mem.ID, Name: "Memory Loss US", DesktopID: d.ID, MobileID: m.ID, MadeBy: "ana@team.test"}); err != nil {
		t.Fatal(err)
	}
	img := &images.Store{Dir: t.TempDir()}
	l := actions.New(st, img, log, func(err error) string { _, m := classify(err); return m }, n)
	a := api.New(context.Background(), l, img, log, classify)
	a.Limits = map[string]any{"max_cpc": 1, "max_daily_cap": 100, "only_own": true}
	a.Library = library.New(demoLibrary(t).URL)
	return handler(a), n, l
}

// TestDemo serves the pages over a fake Taboola until stopped, to look at
// them in a browser: LAUNCH_DEMO=127.0.0.1:8094 go test ./cmd/launch-web -run TestDemo
func TestDemo(t *testing.T) {
	addr := os.Getenv("LAUNCH_DEMO")
	if addr == "" {
		t.Skip("LAUNCH_DEMO not set")
	}
	h, _, _ := demo(t)
	srv := &http.Server{Addr: addr, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Cf-Access-Authenticated-User-Email", "ana@team.test")
		h.ServeHTTP(w, r)
	})}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	go func() { <-stop; _ = srv.Close() }()
	t.Logf("launch demo on http://%s/launch/", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}

func TestRoutes(t *testing.T) {
	h, _, _ := demo(t)
	for _, c := range []struct {
		method, path, origin, has string
		code                      int
	}{
		{"GET", "/", "", "", 302},
		{"GET", "/launch/", "", "<main", 200},
		{"GET", "/launch/taboola/acme-sc", "", "<main", 200},
		{"GET", "/launch/_frame/frame.js", "", "mountFrame", 200},
		{"GET", "/launch/_ads/pairing.js", "", "mixedN", 200},
		{"GET", "/launch/_ads/realize-base.xlsx", "", "", 200},
		{"GET", "/launch/api/accounts/taboola", "", "Acme Health", 200},
		{"GET", "/launch/api/library/set?id=1", "", "Memory morning habit", 200},
		{"POST", "/launch/api/taboola/acme-sc/pause", "https://evil.test", "outro site", 403},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{"campaigns":["1"]}`))
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
			req.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.has) {
			t.Errorf("%s %s: %d %.120s", c.method, c.path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: no CSP", c.path)
		}
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
	}{
		{&network.Refused{Message: "x"}, 400},
		{&write.Refused{Message: "x"}, 400},
		{write.ErrNotConfigured, 503},
		{context.DeadlineExceeded, 504},
		{store.ErrNotFound, 404},
		{&write.Error{Status: 400, Message: "bad"}, 502},
		{errors.New("boom"), 500},
	} {
		if code, msg := classify(c.err); code != c.code || msg == "" {
			t.Errorf("%v: %d %q", c.err, code, msg)
		}
	}
}
