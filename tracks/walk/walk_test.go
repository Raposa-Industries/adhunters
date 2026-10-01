package walk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/shared/archive"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
	"github.com/Raposa-Industries/adhunters/tracks/internal/testdb"
)

// lander serves a tracker redirect, an advertorial whose main button repeats,
// and a ClickBank order page behind it. The advertorial sets a cookie the
// next step needs, as trackers do.
func lander(t *testing.T, nonce *string) *httptest.Server {
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/go", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://www.foxnews.com/" {
			t.Errorf("first hop referer %q", r.Header.Get("Referer"))
		}
		http.Redirect(w, r, "/story?utm=1", http.StatusFound)
	})
	mux.HandleFunc("/story", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "rt", Value: "abc", Path: "/"})
		w.Header().Set("Content-Type", "text/html")
		offer := srv.URL + "/click/1"
		fmt.Fprintf(w, `<html><head><title>Doctors Stunned By Belly Fat Trick</title>
<meta name="description" content="One odd trick">
<script nonce="%s">fbq('init', '1234567890123456'); gtag('config', 'AW-123456789');</script></head>
<body><h1>The Belly Fat Trick</h1><p>Read how Mary lost 30 pounds.</p>
<a href="%s">Watch the video</a> <a href="%s">Get it here</a> <a href="/privacy">Privacy</a>
<footer>© 2026 Acme Health LLC. Contact support@acmehealth.com</footer></body></html>`, *nonce, offer, offer)
	})
	mux.HandleFunc("/click/1", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("rt"); err != nil || c.Value != "abc" {
			t.Errorf("the next step lost the landing page's cookie")
		}
		if !strings.HasSuffix(r.Header.Get("Referer"), "/story?utm=1") {
			t.Errorf("next step referer %q", r.Header.Get("Referer"))
		}
		http.Redirect(w, r, "/order", http.StatusFound)
	})
	mux.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Order SlimPro</title></head><body><h1>Choose your package</h1>
<a href="https://slimpro.pay.clickbank.net/?cbitems=1">Buy now</a></body></html>`)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestTraceAndParse(t *testing.T) {
	nonce := "n1"
	srv := lander(t, &nonce)
	pages := Trace(context.Background(), http.DefaultTransport, srv.URL+"/go", "https://www.foxnews.com/", 5*time.Second)
	if len(pages) != 2 {
		t.Fatalf("%d pages: %+v", len(pages), pages)
	}
	p := pages[0]
	if p.Status != 200 || !strings.HasSuffix(p.FinalURL, "/story?utm=1") || len(p.Hops) != 2 || p.Hops[0].Status != 302 {
		t.Errorf("landing: %+v", p)
	}
	if pages[1].URL != srv.URL+"/click/1" || !strings.HasSuffix(pages[1].FinalURL, "/order") {
		t.Errorf("step: %+v", pages[1])
	}

	rec := &Record{Pages: pages}
	parsed := Parse(rec)
	v := parsed[0].Version
	if v == nil || v.Title != "Doctors Stunned By Belly Fat Trick" || parsed[0].Host != "127.0.0.1" {
		t.Fatalf("landing parsed: %+v", parsed[0])
	}
	if got := v.Pixels["facebook"]; len(got) != 1 || got[0] != "1234567890123456" {
		t.Errorf("facebook pixel: %v", v.Pixels)
	}
	if got := v.Pixels["google"]; len(got) != 1 || got[0] != "AW-123456789" {
		t.Errorf("google: %v", v.Pixels)
	}
	if len(v.Emails) != 1 || v.Emails[0] != "support@acmehealth.com" || len(v.Companies) != 1 || !strings.HasPrefix(v.Companies[0], "Acme Health LLC") {
		t.Errorf("legal: %v %v", v.Emails, v.Companies)
	}
	if !strings.Contains(v.Text, "Mary lost 30 pounds") || v.Headings["h1"][0] != "The Belly Fat Trick" {
		t.Errorf("text: %q %v", v.Text, v.Headings)
	}
	if s := parsed[1]; s.Checkout != "ClickBank" || s.Seller != "slimpro" || s.Version == nil {
		t.Errorf("next step: %+v", s)
	}

	// The same page with another nonce is the same version.
	nonce = "n2"
	again := Parse(&Record{Pages: Trace(context.Background(), http.DefaultTransport, srv.URL+"/go", "https://www.foxnews.com/", 5*time.Second)})
	if again[0].Version.Hash != v.Hash {
		t.Errorf("a nonce made a new version")
	}

	// A raw record reads back to the same parse.
	rec.ID = "01TEST"
	b, err := rec.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if Parse(&back)[0].Version.Hash != v.Hash {
		t.Errorf("the raw record does not parse the same")
	}
}

func TestTraceFailures(t *testing.T) {
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"x", http.StatusFound)
	}))
	defer loop.Close()
	pages := Trace(context.Background(), http.DefaultTransport, loop.URL+"/a", "", 5*time.Second)
	if len(pages) != 1 || pages[0].Error == "" || len(pages[0].Hops) != 10 {
		t.Errorf("redirect loop: %+v", pages)
	}
	if (&Record{Pages: pages}).Outcome() != "error" {
		t.Errorf("a loop is an error")
	}
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer gone.Close()
	pages = Trace(context.Background(), http.DefaultTransport, gone.URL, "", 5*time.Second)
	if r := (&Record{Pages: pages}); r.Outcome() != "http_error" || len(pages) != 1 || Parse(r)[0].Version != nil {
		t.Errorf("404: %+v", pages)
	}
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html><body>"+strings.Repeat("word ", MaxBody/4)+"</body></html>")
	}))
	defer big.Close()
	pages = Trace(context.Background(), http.DefaultTransport, big.URL, "", 5*time.Second)
	if !pages[0].Truncated || len(pages[0].BodyBytes()) != MaxBody {
		t.Errorf("a big page is cut at %d bytes: %v %d", MaxBody, pages[0].Truncated, len(pages[0].BodyBytes()))
	}
}

type direct struct{}

func (d *direct) Next() (string, http.RoundTripper, bool) {
	return "dc-test", http.DefaultTransport, true
}
func (d *direct) Failed(string) {}

func TestWalkerSavesAndReplays(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	nonce := "n"
	srv := lander(t, &nonce)
	now := time.Now().UTC()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	exec(`SELECT tracks.ensure_day_partitions('sighting', $1::date - 1, $1::date)`, now)
	exec(`INSERT INTO tracks.publisher (id, network_id, name, domain, first_seen_at, last_seen_at) VALUES (1, 1, 'foxnews', 'www.foxnews.com', now(), now())`)
	exec(`INSERT INTO tracks.creative (id, creative_key, image_url, first_seen_at, last_seen_at) VALUES (10, 'ck', '', now(), now())`)
	exec(`INSERT INTO tracks.ad (id, creative_id, headline, first_seen_at, last_seen_at) VALUES (100, 10, 'h', now(), now()), (101, 10, 'h2', now(), now())`)
	exec(`INSERT INTO tracks.link (id, link_key, host, path, sample_url, first_seen_at, last_seen_at)
		VALUES (1, gen_random_uuid(), '127.0.0.1', '/go', $1, now(), now()), (2, gen_random_uuid(), 'x', '/', 'javascript:void(0)', now(), now())`, srv.URL+"/go")
	exec(`INSERT INTO tracks.sighting (seen_at, scrape_id, ad_id, creative_id, publisher_id, device_id, link_id)
		VALUES ($1, 1, 100, 10, 1, 1, 1), ($1, 1, 101, 10, 1, 1, 2), ($1 - interval '2 hours', 1, 100, 10, 1, 1, 1)`, now.Add(-10*time.Minute))

	dues, err := Dues(ctx, db, 10, now)
	if err != nil || len(dues) != 1 || dues[0].AdID != 100 || *dues[0].Referer != "https://www.foxnews.com/" {
		t.Fatalf("due: %+v %v", dues, err)
	}

	dir := t.TempDir()
	w, err := spool.Open(dir, "adhunters-worker", slog.New(slog.NewTextHandler(io.Discard, nil)), spool.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := Config{DB: db, Lines: &direct{}, Spool: w, Instance: "adhunters-worker", Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		PageTimeout: 5 * time.Second, Now: func() time.Time { return now }}
	c.defaults()
	c.walkOne(dues[0])
	w.Close()

	var pages, versions int
	var host, seller string
	if err := db.QueryRow(ctx, `SELECT count(*), count(DISTINCT version_hash), min(host), max(seller_account) FROM tracks_api.walk_page_v1 WHERE ad_id = 100`).
		Scan(&pages, &versions, &host, &seller); err != nil {
		t.Fatal(err)
	}
	if pages != 2 || versions != 2 || host != "127.0.0.1" || seller != "slimpro" {
		t.Errorf("saved: %d pages, %d versions, %s, %s", pages, versions, host, seller)
	}
	if dues, _ := Dues(ctx, db, 10, now); len(dues) != 0 {
		t.Errorf("walked ad still due: %+v", dues)
	}
	if dues, _ := Dues(ctx, db, 10, now.Add(7*time.Hour)); len(dues) != 0 {
		t.Errorf("no sighting in the last hour, still due: %+v", dues)
	}

	// Archive, then replay into emptied tables.
	store := &archive.Dir{Root: t.TempDir()}
	n, err := Archive(ctx, db, store, dir)
	if err != nil || n != 1 {
		t.Fatalf("archived %d: %v", n, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, Network, "*", "*", "*", "*", "*"))
	if len(left) != 0 {
		t.Errorf("local files left: %v", left)
	}
	exec(`DELETE FROM tracks.walk_page`)
	exec(`UPDATE tracks.walk SET outcome = 'error'`)
	r, err := Replay(ctx, db, store, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || r.Files != 1 || r.Walks != 1 {
		t.Fatalf("replay: %+v %v", r, err)
	}
	var walks int
	var outcome string
	if err := db.QueryRow(ctx, `SELECT count(DISTINCT walk_id), min(outcome) FROM tracks_api.walk_page_v1`).Scan(&walks, &outcome); err != nil {
		t.Fatal(err)
	}
	if walks != 1 || outcome != "ok" {
		t.Errorf("after replay: %d walks, %s", walks, outcome)
	}
	var title string
	if err := db.QueryRow(ctx, `SELECT v.title FROM tracks_api.walk_page_v1 p JOIN tracks_api.page_version_v1 v ON v.hash = p.version_hash WHERE p.step = 0`).Scan(&title); err != nil || title != "Doctors Stunned By Belly Fat Trick" {
		t.Errorf("title %q %v", title, err)
	}
}

func TestWalkedBackoff(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	next := func() time.Duration {
		var n time.Time
		if err := db.QueryRow(ctx, `SELECT next_at FROM tracks.walk_state WHERE ad_id = 1`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n.Sub(at)
	}
	for i, want := range []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour} {
		if err := Walked(ctx, db, 1, at, false, 6*time.Hour); err != nil {
			t.Fatal(err)
		}
		if got := next(); got != want {
			t.Errorf("failure %d: next in %v, want %v", i+1, got, want)
		}
	}
	if err := Walked(ctx, db, 1, at, true, 6*time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != 6*time.Hour {
		t.Errorf("after a good walk: %v", got)
	}
}
