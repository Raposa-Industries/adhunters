package sweep

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/tracks/capture/feed"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
)

// toServer sends every request to the test server, whatever its host.
type toServer struct{ base *url.URL }

func (s toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = s.base.Scheme, s.base.Host
	return http.DefaultTransport.RoundTrip(r)
}

type fakeLines struct {
	rt     http.RoundTripper
	mu     sync.Mutex
	failed int
}

func (f *fakeLines) Next() (string, http.RoundTripper, bool) { return "dc-1", f.rt, true }
func (f *fakeLines) Failed(string)                           { f.mu.Lock(); f.failed++; f.mu.Unlock() }

func TestRunRecordsAnswersAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "blocked")
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, `TRC.callbacks.mute()`)
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	fl := &fakeLines{rt: toServer{base}}

	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := spool.Open(dir, "shadow", log, spool.Options{})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := feed.LoadTargets("../feed/testdata/targets.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	m := NewMetrics(prometheus.NewRegistry())
	err = Run(ctx, Config{
		Targets: targets, Lines: fl, Spool: w, Instance: "shadow", Version: "test",
		Workers: 2, Throttle: 10 * time.Millisecond, Backoff: 10 * time.Millisecond, Timeout: time.Second,
		Log: log, Metrics: m,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()

	totals, err := spool.Summarize(dir)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]spool.Totals{}
	for _, tt := range totals {
		by[tt.Network] = tt
	}
	tb, nb := by["taboola"], by["newsbreak"]
	if tb.Scrapes == 0 || tb.Errors != 0 || nb.Scrapes == 0 || nb.Errors != nb.Scrapes {
		t.Fatalf("taboola %+v newsbreak %+v", tb, nb)
	}
	if fl.failed != nb.Scrapes {
		t.Fatalf("lines failed %d times, want one per NewsBreak 403 (%d)", fl.failed, nb.Scrapes)
	}
	if m.LastWriteUnix.Load() == 0 {
		t.Fatal("last write not recorded")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "newsbreak/*/*/*/*/capture-shadow-*.ndjson.zst"))
	if len(files) == 0 || !strings.Contains(files[0], "capture-shadow-") {
		t.Fatalf("files %v", files)
	}
}
