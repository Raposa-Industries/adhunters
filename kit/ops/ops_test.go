package ops

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b)
}

func TestHealthz(t *testing.T) {
	s := New("tracks-loader", "v1")
	s.AddCheck("db", func(context.Context) error { return nil })
	if code, body := get(t, s.Handler(), "/healthz"); code != 200 || !strings.Contains(body, `"healthy":true`) {
		t.Fatalf("healthy: %d %s", code, body)
	}
	s.AddCheck("spool", func(context.Context) error { return errors.New("disk full") })
	if code, body := get(t, s.Handler(), "/healthz"); code != 503 || !strings.Contains(body, "disk full") {
		t.Fatalf("unhealthy: %d %s", code, body)
	}
}

func TestMetricsCarryBuildInfo(t *testing.T) {
	_, body := get(t, New("scout-web", "v2").Handler(), "/metrics")
	if !strings.Contains(body, `adhunters_build_info{service="scout-web",version="v2"} 1`) {
		t.Fatalf("build info missing:\n%s", body)
	}
}
