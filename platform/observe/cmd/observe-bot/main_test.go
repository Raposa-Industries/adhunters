package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/platform/observe/internal/sentry"
	"github.com/Raposa-Industries/adhunters/shared/telegram"
)

func TestNextAt(t *testing.T) {
	sp, _ := time.LoadLocation("America/Sao_Paulo")
	at := 8 * time.Hour
	for _, c := range []struct{ now, want string }{
		{"2026-09-29T10:59:00Z", "2026-09-29T11:00:00Z"}, // 07:59 in São Paulo
		{"2026-09-29T11:00:00Z", "2026-09-30T11:00:00Z"}, // exactly 08:00: the next day
		{"2026-09-29T23:30:00Z", "2026-09-30T11:00:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, c.now)
		if got := nextAt(now, at, sp).UTC().Format(time.RFC3339); got != c.want {
			t.Errorf("nextAt(%s) = %s, want %s", c.now, got, c.want)
		}
	}
}

func TestRelaySendsEachNewIssueOnceAndKeepsItsPlace(t *testing.T) {
	sentryAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"2","shortId":"GO-2","title":"close hour","culprit":"load.close","permalink":"https://s/2","firstSeen":"2026-09-28T12:02:00Z","count":"1"},
			{"id":"1","shortId":"GO-1","title":"load raw file","permalink":"https://s/1","firstSeen":"2026-09-28T12:01:00Z","count":"4"}
		]`))
	}))
	defer sentryAPI.Close()
	var mu sync.Mutex
	var texts []string
	fail := false
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			http.Error(w, "down", 502)
			return
		}
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		texts = append(texts, m["text"].(string))
	}))
	defer tg.Close()

	c := &config{tg: telegram.New("t", "-1"), errs: &sentry.Client{URL: sentryAPI.URL, Org: "o", Project: "p", Token: "k"}}
	c.tg.API = tg.URL
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	n, cursor, err := relayOnce(context.Background(), c, start)
	if err != nil || n != 2 || !cursor.Equal(time.Date(2026, 9, 28, 12, 2, 0, 0, time.UTC)) {
		t.Fatalf("n=%d cursor=%v err=%v", n, cursor, err)
	}
	if len(texts) != 2 || texts[0] != "🟠 <b>New error</b> <a href=\"https://s/1\">GO-1</a>\nload raw file" {
		t.Fatalf("%q", texts)
	}
	// Nothing new: nothing sent.
	if n, _, _ := relayOnce(context.Background(), c, cursor); n != 0 {
		t.Fatalf("sent %d again", n)
	}
	// Telegram down: the cursor stays, so the issue goes out next time.
	fail = true
	n, next, err := relayOnce(context.Background(), c, start)
	if err == nil || n != 0 || !next.Equal(start) {
		t.Fatalf("n=%d next=%v err=%v", n, next, err)
	}

	// Sentry itself failing is marked, so the relay logs it as a warning and
	// does not report it to Sentry again; a Telegram failure is not.
	if errors.Is(err, errPoll) {
		t.Fatalf("a Telegram failure counted as a Sentry poll failure: %v", err)
	}
	c.errs.Token = ""
	c.errs.URL = "http://127.0.0.1:1"
	c.errs.Backoff = time.Millisecond
	if _, _, err := relayOnce(context.Background(), c, start); !errors.Is(err, errPoll) {
		t.Fatalf("want a poll error, got %v", err)
	}

	f := filepath.Join(t.TempDir(), "sentry-cursor")
	if err := writeCursor(f, cursor); err != nil || !readCursor(f).Equal(cursor) {
		t.Fatalf("cursor file: %v %v", readCursor(f), err)
	}
}

func TestOpsGroupTakesThePolicyChangesWhenItHasAChatID(t *testing.T) {
	for k, v := range map[string]string{
		"TELEGRAM_BOT_TOKEN": "t", "TELEGRAM_CHAT_ID": "-1",
		"GRAFANA_QUERY_URL": "https://g", "GRAFANA_QUERY_USER": "u", "GRAFANA_QUERY_TOKEN": "k",
	} {
		t.Setenv(k, v)
	}
	t.Setenv("OPS_TELEGRAM_CHAT_ID", "")
	c, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ops != c.tg {
		t.Fatalf("no ops chat id: policy changes should stay in the alerts group, got chat %q", c.ops.ChatID)
	}
	t.Setenv("OPS_TELEGRAM_CHAT_ID", "-2")
	if c, err = load(); err != nil {
		t.Fatal(err)
	}
	if c.ops.ChatID != "-2" || c.ops.Token != "t" || c.tg.ChatID != "-1" {
		t.Fatalf("ops %q/%q, alerts %q", c.ops.ChatID, c.ops.Token, c.tg.ChatID)
	}
}
