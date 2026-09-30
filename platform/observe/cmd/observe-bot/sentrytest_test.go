package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestSentryTest sends the test error to a stand-in Sentry and checks it
// arrives, named as the command says.
func TestSentryTest(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
	}))
	defer srv.Close()

	t.Setenv("SENTRY_DSN", "")
	if err := sentryTestCmd(); err == nil {
		t.Fatal("no DSN should be an error")
	}
	t.Setenv("SENTRY_DSN", strings.Replace(srv.URL, "http://", "http://publickey@", 1)+"/1")
	if err := sentryTestCmd(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 || !strings.Contains(strings.Join(got, ""), "sentry test from observe-bot") {
		t.Fatalf("Sentry got %q", got)
	}
}
