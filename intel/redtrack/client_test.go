package redtrack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "k3y/with+odd=chars"

func testClient(t *testing.T, h http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(testKey)
	c.BaseURL = srv.URL
	c.MinGap = 0
	// A fake clock that only moves when the client sleeps.
	clock := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	var slept []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error {
		if d > 0 {
			slept = append(slept, d)
			clock = clock.Add(d)
		}
		return ctx.Err()
	}
	return c, &slept
}

func TestKeySentButNeverKept(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("api_key"); got != testKey {
			t.Errorf("api_key = %q", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":"bad key %s"}`, r.URL.Query().Get("api_key"))
	})
	r, err := c.Get(context.Background(), PathCampaigns, url.Values{"api_key": {"x"}, "per": {"5"}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("key leaked in error: %v", err)
	}
	if strings.Contains(string(r.Body), testKey) || !strings.Contains(string(r.Body), "bad key REDACTED") {
		t.Fatalf("body = %s", r.Body)
	}
	if r.Query.Has("api_key") || r.Query.Get("per") != "5" {
		t.Fatalf("query kept = %v", r.Query)
	}
}

func TestNetworkErrorHidesKey(t *testing.T) {
	c := New(testKey)
	c.BaseURL = "http://127.0.0.1:1"
	c.MinGap, c.MaxRetries = 0, 0
	_, err := c.Get(context.Background(), PathReport, nil)
	if err == nil || strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), url.QueryEscape(testKey)) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetriesTooManyRequests(t *testing.T) {
	var calls atomic.Int32
	c, slept := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `[]`)
	})
	r, err := c.Get(context.Background(), PathReport, nil)
	if err != nil || r.Status != 200 || calls.Load() != 2 {
		t.Fatalf("r=%v err=%v calls=%d", r, err, calls.Load())
	}
	if len(*slept) != 1 || (*slept)[0] != 7*time.Second {
		t.Fatalf("slept %v, want [7s]", *slept)
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	c.MaxRetries = 2
	_, err := c.Get(context.Background(), PathReport, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 502 || calls.Load() != 3 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestItemsShapes(t *testing.T) {
	for _, tc := range []struct {
		body  string
		n, tt int
		bad   bool
	}{
		{`[{"a":1},{"a":2}]`, 2, -1, false},
		{`{"items":[{"a":1}],"total":40}`, 1, 40, false},
		{`{"items":[]}`, 0, -1, false},
		{`null`, 0, -1, false},                      // /campaigns with none
		{`{"items":null,"total":{}}`, 0, -1, false}, // /campaigns/v2 with none
		{`{"id":"x"}`, 0, -1, true},
	} {
		items, total, err := Items([]byte(tc.body))
		if (err != nil) != tc.bad || len(items) != tc.n || total != tc.tt {
			t.Errorf("%s: items=%d total=%d err=%v", tc.body, len(items), total, err)
		}
	}
}

func TestPagesStopsAtTotalAndShortPage(t *testing.T) {
	var pages []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pages = append(pages, r.URL.Query().Get("page")+"/"+r.URL.Query().Get("per"))
		switch r.URL.Path {
		case PathConversions: // 5 rows in all, 2 per page, with total
			n := min(2, 5-(p-1)*2)
			fmt.Fprintf(w, `{"items":[%s],"total":5}`, strings.TrimSuffix(strings.Repeat(`{"x":1},`, n), ","))
		case PathReport: // a bare array, short on page 2
			if p == 1 {
				fmt.Fprint(w, `[{"x":1},{"x":2}]`)
			} else {
				fmt.Fprint(w, `[{"x":3}]`)
			}
		}
	})
	count := 0
	fn := func(_ *Response, items []json.RawMessage) error { count += len(items); return nil }
	if err := c.Pages(context.Background(), PathConversions, nil, 2, 0, fn); err != nil || count != 5 {
		t.Fatalf("conversions: count=%d err=%v pages=%v", count, err, pages)
	}
	if strings.Join(pages, " ") != "1/2 2/2 3/2" {
		t.Fatalf("pages = %v", pages)
	}
	pages, count = nil, 0
	if err := c.Pages(context.Background(), PathReport, nil, 2, 0, fn); err != nil || count != 3 || len(pages) != 2 {
		t.Fatalf("report: count=%d err=%v pages=%v", count, err, pages)
	}
}

func TestReportQueryAndRows(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		want := url.Values{
			"group": {"campaign,sub1"}, "date_from": {"2026-09-01"}, "date_to": {"2026-09-28"},
			"timezone": {"America/Sao_Paulo"}, "source_id": {"abc"}, "page": {"1"}, "per": {"1000"}, "api_key": {testKey},
		}
		if q.Encode() != want.Encode() {
			t.Errorf("query = %s", q.Encode())
		}
		fmt.Fprint(w, `[{"campaign":"C1","sub1":"123","clicks":10,"cost":"4.50","revenue":null}]`)
	})
	q := ReportQuery{
		Group: []string{"campaign", "sub1"},
		From:  DayOf(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)), To: Day{2026, 9, 28},
		Timezone: "America/Sao_Paulo", Filters: url.Values{"source_id": {"abc"}},
	}
	var rows []Row
	err := c.Report(context.Background(), q, func(_ *Response, rs []Row) error { rows = append(rows, rs...); return nil })
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	r := rows[0]
	if r.String("sub1") != "123" || r.String("revenue") != "" || r.String("missing") != "" {
		t.Errorf("String: %q %q", r.String("sub1"), r.String("revenue"))
	}
	if f, ok := r.Float("cost"); !ok || f != 4.5 {
		t.Errorf("cost = %v %v", f, ok)
	}
	if f, ok := r.Float("clicks"); !ok || f != 10 {
		t.Errorf("clicks = %v %v", f, ok)
	}
	if _, ok := r.Float("revenue"); ok {
		t.Error("null revenue read as a number")
	}
}

func TestMinGapSpacesRequests(t *testing.T) {
	c, slept := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) })
	c.MinGap = time.Hour
	for range 2 {
		if _, err := c.Get(context.Background(), PathSources, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(*slept) != 1 || (*slept)[0] < 59*time.Minute {
		t.Fatalf("slept %v, want one wait of about an hour", *slept)
	}
}

func TestRateRetriesDoNotUseMaxRetries(t *testing.T) {
	var calls atomic.Int32
	c, slept := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 3 {
			w.Header().Set("Retry-After", "25")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `[]`)
	})
	c.MaxRetries = 0
	if _, err := c.Get(context.Background(), PathSources, nil); err != nil || calls.Load() != 4 {
		t.Fatalf("err=%v calls=%d slept=%v", err, calls.Load(), *slept)
	}
	c.MaxRateRetries = 1
	calls.Store(0)
	_, err := c.Get(context.Background(), PathSources, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestReportIsSpacedButListsAreNot(t *testing.T) {
	c, slept := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) })
	for _, p := range []string{PathReport, PathSources, PathReport, PathCampaigns} {
		if _, err := c.Get(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(*slept) != 1 || (*slept)[0] != 6*time.Second {
		t.Fatalf("slept %v, want one 6s wait before the second report", *slept)
	}
}

func TestReportWaitsForResetWhenMinuteIsUsed(t *testing.T) {
	c, slept := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Ratelimit-Remaining-Minute", "0")
		w.Header().Set("Ratelimit-Reset", "41")
		fmt.Fprint(w, `[]`)
	})
	for range 2 {
		if _, err := c.Get(context.Background(), PathReport, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(*slept) != 1 || (*slept)[0] != 41*time.Second {
		t.Fatalf("slept %v, want [41s]", *slept)
	}
}
