package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPolicyWatchBaselineThenChanges(t *testing.T) {
	body := "Ads must not promote weapons."
	mux := http.NewServeMux()
	mux.HandleFunc("/help/en/collections/1-policy", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><main><h1>Policy</h1><a href="/help/en/articles/2-prohibited">P</a></main></body></html>`))
	})
	mux.HandleFunc("/help/en/articles/2-prohibited", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><article><h1>Prohibited</h1><p>` + body + `</p></article></body></html>`))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	t.Setenv("POLICY_URLS", srv.URL+"/help/en/collections/1-policy")

	w := newPolicyWatch(t.TempDir(), nil)
	w.crawler.HTTP, w.crawler.Pause = srv.Client(), 0
	var sent []string
	send := func(m string) error { sent = append(sent, m); return nil }
	ctx := context.Background()

	if n, err := w.once(ctx, send); err != nil || n != 0 || len(sent) != 1 || !strings.Contains(sent[0], "Watching 1") {
		t.Fatalf("first crawl: n=%d err=%v sent=%q", n, err, sent)
	}
	if n, err := w.once(ctx, send); err != nil || n != 0 || len(sent) != 1 {
		t.Fatalf("unchanged: n=%d err=%v sent=%q", n, err, sent)
	}
	body = "Ads must not promote weapons or fireworks."
	if n, err := w.once(ctx, send); err != nil || n != 1 || !strings.Contains(sent[1], "➕ Ads must not promote weapons or fireworks.") {
		t.Fatalf("changed: n=%d err=%v sent=%q", n, err, sent)
	}
	// A failed send keeps the old crawl, so the change is posted again.
	body = "Ads must not promote anything."
	fail := func(string) error { return context.DeadlineExceeded }
	if _, err := w.once(ctx, fail); err == nil {
		t.Fatal("a failed send must fail the run")
	}
	if n, err := w.once(ctx, send); err != nil || n != 1 {
		t.Fatalf("after a failed send: n=%d err=%v", n, err)
	}
}
