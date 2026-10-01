package spyad_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/create/internal/spyad"
	"github.com/Raposa-Industries/adhunters/create/internal/testdb"
)

func TestAd(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	// Stand-ins for the published views, with the columns Create reads.
	if _, err := db.Exec(ctx, `
		CREATE SCHEMA tracks_api;
		CREATE TABLE tracks_api.creative_v1 (id BIGINT PRIMARY KEY, image_url TEXT, format_type TEXT);
		CREATE TABLE tracks_api.ad_v1 (id BIGINT, creative_id BIGINT, headline TEXT, brand_id BIGINT, last_seen_at TIMESTAMPTZ);
		CREATE TABLE tracks_api.brand_v1 (id BIGINT, name TEXT);
		INSERT INTO tracks_api.creative_v1 VALUES (1, 'https://cdn.example/1.jpg', 'image'), (2, NULL, NULL);
		INSERT INTO tracks_api.brand_v1 VALUES (5, 'Acme');
		INSERT INTO tracks_api.ad_v1 VALUES (10, 1, 'Old headline', 5, now() - interval '2 days'),
		                                    (11, 1, 'New headline', 5, now()),
		                                    (12, 1, '', 5, now() + interval '1 hour');`); err != nil {
		t.Fatal(err)
	}
	r := spyad.New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// No spy_api yet: the ad still reads, with no vertical.
	a, err := r.Ad(ctx, 1)
	if err != nil || a.ImageURL != "https://cdn.example/1.jpg" || a.Headline != "New headline" || a.Brand != "Acme" || a.VerticalID != "" {
		t.Fatalf("ad %+v %v", a, err)
	}
	if _, err := db.Exec(ctx, `
		CREATE SCHEMA spy_api;
		CREATE TABLE spy_api.creative_class_v1 (creative_id BIGINT, vertical_id TEXT);
		INSERT INTO spy_api.creative_class_v1 VALUES (1, 'tinnitus');`); err != nil {
		t.Fatal(err)
	}
	if a, err = r.Ad(ctx, 1); err != nil || a.VerticalID != "tinnitus" {
		t.Fatalf("with a vertical %+v %v", a, err)
	}
	if a, err = r.Ad(ctx, 2); err != nil || a.ImageURL != "" || a.Headline != "" {
		t.Errorf("a bare creative %+v %v", a, err)
	}
	if _, err := r.Ad(ctx, 3); err != spyad.ErrNotFound {
		t.Errorf("missing: %v", err)
	}
}

func TestPictureReachesPublicAddressesOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer srv.Close()
	r := spyad.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, u := range []string{srv.URL + "/x.png", "file:///etc/passwd", "", "http://[::1]:1/x"} {
		if b, err := r.Picture(context.Background(), u); err == nil {
			t.Errorf("%q was fetched: %q", u, b)
		}
	}
	if _, err := r.Picture(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("loopback: %v", err)
	}
	for ip, want := range map[string]bool{"8.8.8.8": true, "127.0.0.1": false, "10.1.2.3": false, "192.168.0.1": false,
		"100.101.1.1": false, "169.254.169.254": false, "::1": false, "2606:4700::1111": true} {
		if got := spyad.Public(net.ParseIP(ip)); got != want {
			t.Errorf("Public(%s) = %v", ip, got)
		}
	}
}
