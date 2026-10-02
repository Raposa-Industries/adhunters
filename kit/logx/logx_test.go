package logx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestLinesCarryServiceAndVersion(t *testing.T) {
	var buf bytes.Buffer
	NewTo(&buf, "tracks-loader", "v1.2.3", "debug").Debug("hello", "file", "capture-a-1403")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not JSON: %v: %s", err, buf.String())
	}
	for k, want := range map[string]string{"service": "tracks-loader", "version": "v1.2.3", "msg": "hello", "file": "capture-a-1403"} {
		if line[k] != want {
			t.Errorf("%s = %v, want %s", k, line[k], want)
		}
	}
}

func TestDefaultLevelIsInfo(t *testing.T) {
	var buf bytes.Buffer
	NewTo(&buf, "s", "v", "").Debug("hidden")
	if buf.Len() != 0 {
		t.Fatalf("debug line written at default level: %s", buf.String())
	}
}

func TestErrorLinesAreCounted(t *testing.T) {
	log := NewTo(io.Discard, "spy-numbers", "v1", "")
	before := testutil.ToFloat64(Errors.WithLabelValues("numbers job failed"))
	log.Info("numbers job done")
	log.Warn("numbers job slow")
	log.With("job", "direction").Error("numbers job failed", "err", errors.New("numeric field overflow"))
	log.WithGroup("g").Error("numbers job failed")
	if got := testutil.ToFloat64(Errors.WithLabelValues("numbers job failed")) - before; got != 2 {
		t.Fatalf("counted %v error lines, want 2", got)
	}
	log.Error("sentry test at 2026-10-01T03:51:23Z")
	if got := testutil.ToFloat64(Errors.WithLabelValues("sentry test at N-N-NTN:N:NZ")); got != 1 {
		t.Fatalf("a message with numbers counted %v under its masked label, want 1", got)
	}
}
