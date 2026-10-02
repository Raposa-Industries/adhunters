package ops

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/logx"
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

func TestMetricsCountErrorLines(t *testing.T) {
	s := New("tracks-walker", "v1")
	logx.NewTo(io.Discard, "tracks-walker", "v1", "").Error("archiving raw walk files")
	_, body := get(t, s.Handler(), "/metrics")
	if !strings.Contains(body, `adhunters_log_errors_total{msg="archiving raw walk files"}`) {
		t.Fatalf("error lines not counted on /metrics:\n%s", body)
	}
}

func TestTasks(t *testing.T) {
	s := New("tracks-loader", "v1")
	tasks := s.Tasks()
	if s.Tasks() != tasks {
		t.Fatal("Tasks registered twice")
	}
	tasks.Promise("hour_close", 75*time.Minute)
	tasks.Done("hour_close", time.Now().Add(-2*time.Second), 320000, nil)
	tasks.Done("hour_close", time.Now(), 0, errors.New("boom"))
	_, body := get(t, s.Handler(), "/metrics")
	for _, want := range []string{
		`adhunters_task_promise_seconds{task="hour_close"} 4500`,
		`adhunters_task_last_rows{task="hour_close"} 320000`,
		`adhunters_task_runs_total{result="ok",task="hour_close"} 1`,
		`adhunters_task_runs_total{result="error",task="hour_close"} 1`,
		`adhunters_task_last_success_timestamp_seconds{task="hour_close"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
}
