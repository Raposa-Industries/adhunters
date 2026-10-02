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
	oldSends := sends
	sends = &limiter{}
	t.Cleanup(func() { capture = old; on.Store(was); sends = oldSends })
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
	if want := []string{"tracks-loader", "load raw file", "load file N: i/o timeout"}; fmt.Sprint(ev.Fingerprint) != fmt.Sprint(want) {
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

// The real messages behind Sentry issues GO-C and GO-8 (2 Oct 2026): one log
// message each, failing in different ways. Each way must be its own issue,
// and the same way for another file, time or address must not be.
func TestShapeSplitsKindsNotOccurrences(t *testing.T) {
	same := [][2]string{
		{"walk/2026/10/01/21/capture-adhunters-worker-2155.ndjson.zst: archive: key holds different bytes",
			"walk/2026/10/02/03/capture-adhunters-worker-0312.ndjson.zst: archive: key holds different bytes"},
		{"ERROR: the range 2026-09-17 17:00:00+00 to 2026-09-18 17:00:00+00 has no closed day yet (SQLSTATE P0001)",
			"ERROR: the range 2026-09-30 17:00:00+00 to 2026-10-01 17:00:00+00 has no closed day yet (SQLSTATE P0001)"},
		{"claim: failed to connect to `user=raposa database=adhunters`: 10.20.1.20:5432 (10.20.1.20): tls error: read tcp 10.20.1.10:51142->10.20.1.20:5432: read: connection reset by peer",
			"claim: failed to connect to `user=raposa database=adhunters`: 10.20.1.20:5432 (10.20.1.20): tls error: read tcp 10.20.1.10:51230->10.20.1.20:5432: read: connection reset by peer"},
		{"files: put files/11b13f050114cc270ad1d9322a7fba6e: The specified bucket does not exist.",
			"files: put files/9f0e4c2b7a1d4e8f9a0b1c2d3e4f5a6b: The specified bucket does not exist."},
		{`Get "https://de.sentry.io/api/0/projects/a/b/issues/?limit=100": context deadline exceeded`,
			`Get "https://de.sentry.io/api/0/projects/a/b/issues/?limit=50&cursor=x": context deadline exceeded`},
		{"loading: failed to connect:\n\t127.0.0.1:5432 (localhost): server error\n\t[::1]:5432 (localhost): server error",
			"loading: failed to connect:\n\t127.0.0.1:5433 (localhost): server error\n\t[::1]:5433 (localhost): server error"},
	}
	for _, p := range same {
		if a, b := shape(p[0]), shape(p[1]); a != b {
			t.Errorf("one failure, two shapes:\n%q\n%q", a, b)
		}
	}
	different := [][2]string{
		{"ERROR: numeric field overflow (SQLSTATE 22003)",
			"ERROR: canceling statement due to statement timeout (SQLSTATE 57014)"},
		{"ERROR: numeric field overflow (SQLSTATE 22003)",
			"ERROR: the range 2026-09-17 17:00:00+00 to 2026-09-18 17:00:00+00 has no closed day yet (SQLSTATE P0001)"},
		{"walk/2026/10/01/21/capture-adhunters-worker-2155.ndjson.zst: archive: key holds different bytes",
			`walk/2026/09/30/22/capture-adhunters-worker-2212.ndjson.zst: spool: "walk/2026/09/30/22/capture-adhunters-worker-2212.ndjson.zst" is not a sealed raw file key`},
	}
	for _, p := range different {
		if a, b := shape(p[0]), shape(p[1]); a == b {
			t.Errorf("two failures, one shape %q", a)
		}
	}
	if got := shape(strings.Repeat("x", 500)); len(got) != 200 {
		t.Errorf("shape not cut to 200: %d", len(got))
	}
}

func TestFailingNewWayIsNewIssue(t *testing.T) {
	got := catch(t)
	log := slog.New(Handler(slog.NewJSONHandler(io.Discard, nil))).With("service", "spy-numbers")
	log.Error("numbers job failed", "job", "direction", "err", errors.New("ERROR: numeric field overflow (SQLSTATE 22003)"))
	log.Error("numbers job failed", "job", "direction", "err", errors.New("ERROR: canceling statement due to statement timeout (SQLSTATE 57014)"))
	if len(*got) != 2 {
		t.Fatalf("want 2 events, got %d", len(*got))
	}
	if a, b := fmt.Sprint((*got)[0].Fingerprint), fmt.Sprint((*got)[1].Fingerprint); a == b {
		t.Errorf("an overflow and a timeout share the issue %s", a)
	}
}

func TestRepeatsAreLimited(t *testing.T) {
	got := catch(t)
	at := time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC)
	oldNow := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = oldNow })
	log := slog.New(Handler(slog.NewJSONHandler(io.Discard, nil))).With("service", "tracks-walker")
	fail := func(file int) {
		log.Error("archiving raw walk files", "err", fmt.Errorf("walk/2026/10/02/04/capture-%04d.ndjson.zst: archive: key holds different bytes", file))
	}

	for i := 0; i < 50; i++ {
		fail(i)
		at = at.Add(time.Minute)
	}
	if len(*got) != 1 {
		t.Fatalf("50 repeats in 50 minutes sent %d events, want 1", len(*got))
	}
	at = at.Add(10 * time.Minute) // an hour after the first
	fail(99)
	if len(*got) != 2 {
		t.Fatalf("a repeat an hour later sent %d events in all, want 2", len(*got))
	}

	// A new kind of failure is sent at once, until the hourly cap.
	for i := 0; i < perHour+5; i++ {
		log.Error("step failed", "err", fmt.Errorf("cause %c", 'a'+rune(i%26)), "n", i)
		log.Error(fmt.Sprintf("other step %c%c", 'a'+rune(i%26), 'a'+rune(i/26)), "err", errors.New("boom"))
	}
	// The first hour sent 1; this hour sends perHour, the repeat included.
	if len(*got) != 1+perHour {
		t.Fatalf("sent %d events, want %d (the hourly cap)", len(*got), 1+perHour)
	}
}
