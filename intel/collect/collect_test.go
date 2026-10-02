package collect

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/intel/internal/testdb"
	"github.com/Raposa-Industries/adhunters/intel/taboola"
)

func TestSpoolRoundTrip(t *testing.T) {
	s := Spool{Dir: t.TempDir()}
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for i, body := range []string{`{"a":1}`, `{"b":2}`} {
		a := &Answer{Source: "taboola", Login: "main", Kind: KindTbCampaigns, Path: "x/campaigns",
			Status: 200, FetchedAt: at.Add(time.Duration(i) * time.Second), Body: []byte(body)}
		if err := s.Put(a); err != nil {
			t.Fatal(err)
		}
	}
	files, err := s.Pending()
	if err != nil || len(files) != 2 {
		t.Fatalf("pending %v %v", files, err)
	}
	a, err := Read(files[0])
	if err != nil || string(a.Body) != `{"a":1}` || a.Kind != KindTbCampaigns {
		t.Fatalf("read %+v %v", a, err)
	}

	db := testdb.New(t)
	n, err := s.Drain(context.Background(), db)
	if err != nil || n != 2 {
		t.Fatalf("drained %d: %v", n, err)
	}
	if left, _ := s.Pending(); len(left) != 0 {
		t.Fatalf("%d files left", len(left))
	}
	// Storing the same answer again changes nothing.
	if err := Store(context.Background(), db, a); err != nil {
		t.Fatal(err)
	}
	var count int
	db.QueryRow(context.Background(), `SELECT count(*) FROM intel.answer`).Scan(&count)
	if count != 2 {
		t.Fatalf("%d answers, want 2", count)
	}
}

type fakeTaboola struct{ paths []string }

func (f *fakeTaboola) Get(_ context.Context, path string, q url.Values) (*taboola.Response, error) {
	f.paths = append(f.paths, path)
	switch {
	case strings.HasSuffix(path, "allowed-accounts"):
		return &taboola.Response{Status: 200, Body: []byte(`{"results":[{"account_id":"acme-network","type":"NETWORK","time_zone_name":"US/Eastern"},{"account_id":"acme-1-sc","type":"PARTNER","time_zone_name":"US/Eastern"}]}`)}, nil
	case strings.HasSuffix(path, "/campaigns"):
		return &taboola.Response{Status: 200, Body: []byte(`{"results":[{"id":"501"}]}`)}, nil
	case strings.HasSuffix(path, "/items/"):
		return &taboola.Response{Status: 403, Body: []byte(`{"message":"no"}`)}, errors.New("403")
	}
	return &taboola.Response{Status: 200, Body: []byte(`{"results":[]}`)}, nil
}

func TestTaboolaSettingsSpoolsEveryAnswer(t *testing.T) {
	s := Spool{Dir: t.TempDir()}
	f := &fakeTaboola{}
	tb := &Taboola{Login: "main", API: f, Spool: s, Pace: &Pacer{disabled: true},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now}
	err := tb.Settings(context.Background())
	if err == nil {
		t.Fatal("a refused items answer should report an error")
	}
	for _, p := range f.paths {
		if strings.HasPrefix(p, "acme-network") {
			t.Fatalf("asked the network account: %s", p)
		}
	}
	// accounts, campaigns, and the refused items answer are all kept.
	files, _ := s.Pending()
	if len(files) != 3 {
		t.Fatalf("%d answers spooled, want 3 (%v)", len(files), f.paths)
	}
	last, _ := Read(files[2])
	if last.Kind != KindTbItems || last.Status != 403 || last.Account != "acme-1-sc" {
		t.Fatalf("last answer %+v", last)
	}
}

// A login from Launch's Contas page keeps to its chosen accounts, and leaves
// out the ones another login already reads.
func TestOnlyAndSkipKeepToTheChosenAccounts(t *testing.T) {
	f := &accountsFake{body: `{"results":[{"account_id":"n","type":"NETWORK","time_zone_name":"UTC"},
		{"account_id":"a-sc","type":"PARTNER","time_zone_name":"UTC"},
		{"account_id":"b-sc","type":"PARTNER","time_zone_name":"UTC"},
		{"account_id":"c-sc","type":"PARTNER","time_zone_name":"UTC"}]}`}
	tb := &Taboola{Login: "contas-1", API: f, Spool: Spool{Dir: t.TempDir()}, Pace: &Pacer{disabled: true},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now,
		Only: map[string]bool{"a-sc": true, "b-sc": true}, Skip: func(a string) bool { return a == "b-sc" }}
	accs, err := tb.Known(context.Background())
	if err != nil || len(accs) != 1 || accs[0].ID != "a-sc" {
		t.Fatalf("accounts %+v %v, want only a-sc", accs, err)
	}
}

type accountsFake struct{ body string }

func (f *accountsFake) Get(context.Context, string, url.Values) (*taboola.Response, error) {
	return &taboola.Response{Status: 200, Body: []byte(f.body)}, nil
}

func TestRealtimeWindow(t *testing.T) {
	// Taboola refuses 13:00 to 14:00 as "2 hours"; the span never starts on
	// the hour unless it also ends in that hour.
	for _, c := range []struct {
		now        time.Time
		start, end string
	}{
		{time.Date(2026, 9, 30, 18, 7, 30, 0, time.UTC), "2026-09-30T13:10:00", "2026-09-30T14:07:00"},
		{time.Date(2026, 9, 30, 18, 0, 20, 0, time.UTC), "2026-09-30T13:05:00", "2026-09-30T14:00:00"},
		{time.Date(2026, 9, 30, 18, 4, 0, 0, time.UTC), "2026-09-30T13:05:00", "2026-09-30T14:04:00"},
		{time.Date(2026, 9, 30, 18, 57, 0, 0, time.UTC), "2026-09-30T14:00:00", "2026-09-30T14:57:00"},
	} {
		f := &fakeTaboola{}
		var q url.Values
		now := c.now
		tb := &Taboola{Login: "main", API: readerFunc(func(p string, v url.Values) { f.paths = append(f.paths, p); q = v }),
			Spool: Spool{Dir: t.TempDir()}, Pace: &Pacer{disabled: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now: func() time.Time { return now }}
		tb.accounts = []TbAccount{{ID: "acme-1-sc", TimeZone: "US/Eastern", loc: mustLoc(t, "US/Eastern")}}
		if err := tb.Realtime(context.Background()); err != nil {
			t.Fatal(err)
		}
		if q.Get("start_date") != c.start || q.Get("end_date") != c.end {
			t.Errorf("at %s: window %v", c.now, q)
		}
	}
}

type readerFunc func(string, url.Values)

func (r readerFunc) Get(_ context.Context, path string, q url.Values) (*taboola.Response, error) {
	r(path, q)
	return &taboola.Response{Status: 200, Body: []byte(`{"results":[]}`)}, nil
}

func mustLoc(t *testing.T, name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestRedTrackReadOnly(t *testing.T) {
	called := false
	rt := ReadOnly{Next: roundTrip(func(*http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	req, _ := http.NewRequest(http.MethodPost, "https://api.redtrack.io/campaigns", nil)
	if _, err := rt.RoundTrip(req); !errors.Is(err, ErrRedTrackWrite) || called {
		t.Fatalf("POST went through: %v", err)
	}
	req, _ = http.NewRequest(http.MethodGet, "https://api.redtrack.io/report", nil)
	if _, err := rt.RoundTrip(req); err != nil || !called {
		t.Fatalf("GET refused: %v", err)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPacerSpacesRequests(t *testing.T) {
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	var slept []time.Duration
	p := &Pacer{PerMinute: 60, RealtimePerMinute: 6, now: func() time.Time { return now },
		sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }}
	ctx := context.Background()
	for range 3 {
		p.Wait(ctx, false)
	}
	p.Wait(ctx, true)
	p.Wait(ctx, true)
	// The first realtime request waits behind the queued standard ones.
	want := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 13 * time.Second}
	if len(slept) != len(want) {
		t.Fatalf("slept %v, want %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Fatalf("slept %v, want %v", slept, want)
		}
	}
}

func TestIANAZone(t *testing.T) {
	if z := IANAZone("US/Eastern"); z != "America/New_York" {
		t.Fatalf("got %s", z)
	}
}
