package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSentryTest sends the test error to a stand-in Sentry and checks it
// arrives, named as the command says, and that two runs make two issues.
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
	time.Sleep(1100 * time.Millisecond) // the message carries the second
	if err := sentryTestCmd(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) < 2 || !strings.Contains(strings.Join(got, ""), "sentry test from observe-bot at ") {
		t.Fatalf("Sentry got %q", got)
	}
	// One issue per fingerprint: the two runs must not share one.
	fp := regexp.MustCompile(`"fingerprint":\[[^\]]*\]`)
	a, b := fp.FindString(got[0]), fp.FindString(got[len(got)-1])
	if a == "" || a == b {
		t.Fatalf("both runs have fingerprint %s", a)
	}
}
