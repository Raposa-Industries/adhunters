package errs

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

type timeoutErr struct{}

func (timeoutErr) Error() string { return "i/o timeout" }

func catch(t *testing.T) *[]*sentry.Event {
	t.Helper()
	var got []*sentry.Event
	old, was := capture, on.Load()
	capture = func(ev *sentry.Event) { got = append(got, ev) }
	on.Store(true)
	t.Cleanup(func() { capture = old; on.Store(was) })
	return &got
}

func TestErrorLinesBecomeEventsGroupedByMessage(t *testing.T) {
	got := catch(t)
	log := slog.New(Handler(slog.NewJSONHandler(io.Discard, nil))).With("service", "tracks-loader", "version", "abc")
	log.Info("fine")
	log.Warn("careful")
	err := fmt.Errorf("load file 17: %w", timeoutErr{})
	log.WithGroup("file").Error("load raw file", "err", err, "key", "raw/taboola/x.zst")

	if len(*got) != 1 {
		t.Fatalf("want 1 event (error level only), got %d", len(*got))
	}
	ev := (*got)[0]
	if ev.Message != "load raw file: load file 17: i/o timeout" {
		t.Errorf("message %q", ev.Message)
	}
	if want := []string{"tracks-loader", "load raw file"}; fmt.Sprint(ev.Fingerprint) != fmt.Sprint(want) {
		t.Errorf("fingerprint %v, want %v", ev.Fingerprint, want)
	}
	if len(ev.Exception) != 1 || ev.Exception[0].Type != "errs.timeoutErr" {
		t.Errorf("exception %+v", ev.Exception)
	}
	if ev.Contexts["log"]["file.key"] != "raw/taboola/x.zst" {
		t.Errorf("log context %v", ev.Contexts)
	}
	if _, ok := ev.Contexts["log"]["service"]; ok {
		t.Errorf("service should be a tag, not a field: %v", ev.Contexts)
	}
}

func TestOffWithoutDSN(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")
	if ok, err := Init("s", "v"); ok || err != nil {
		t.Fatalf("Init without DSN: %v %v", ok, err)
	}
	got := catch(t)
	on.Store(false)
	slog.New(Handler(slog.NewTextHandler(io.Discard, nil))).Error("boom", "err", errors.New("x"))
	if len(*got) != 0 {
		t.Fatalf("sent %d events while off", len(*got))
	}
}

// TestSendsToSentry runs the whole path against a stand-in Sentry: Init from
// the environment, an error line, and Flush.
func TestSendsToSentry(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
	}))
	defer srv.Close()
	t.Setenv("SENTRY_DSN", strings.Replace(srv.URL, "http://", "http://publickey@", 1)+"/1")
	t.Cleanup(func() { on.Store(false) })

	if ok, err := Init("tracks-loader", "abc123"); !ok || err != nil {
		t.Fatalf("Init: %v %v", ok, err)
	}
	slog.New(Handler(slog.NewJSONHandler(io.Discard, nil))).With("service", "tracks-loader").
		Error("load raw file", "err", errors.New("checksum mismatch"))
	Flush(5 * time.Second)

	select {
	case body := <-got:
		for _, want := range []string{`"release":"tracks-loader@abc123"`, "checksum mismatch", `"service":"tracks-loader"`} {
			if !strings.Contains(body, want) {
				t.Errorf("event lacks %s:\n%s", want, body)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event reached the stand-in Sentry")
	}
}
