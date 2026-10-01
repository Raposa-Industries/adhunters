package load

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/funnels/edge"
	"github.com/Raposa-Industries/adhunters/funnels/internal/testdb"
	"github.com/Raposa-Industries/adhunters/shared/archive"
	"github.com/Raposa-Industries/adhunters/shared/spool"
)

func TestNetworksMatch(t *testing.T) {
	aws, err := parseAWS([]byte(`{"prefixes":[{"ip_prefix":"3.0.0.0/15"},{"ip_prefix":"52.94.76.0/22"}],
		"ipv6_prefixes":[{"ipv6_prefix":"2600:1f00::/24"}]}`))
	must(t, err)
	gcp, err := parseGCP([]byte(`{"prefixes":[{"ipv4Prefix":"34.80.0.0/15"},{"ipv6Prefix":"2600:1900::/35"}]}`))
	must(t, err)
	plain, err := ParsePlainList([]byte("# hetzner\n5.9.0.0/16,DE\n\n  88.198.0.0/16 \nnot a network\n10.0.0.1/40\n"))
	must(t, err)
	var list []dcNet
	for _, x := range []struct {
		src string
		ps  []netip.Prefix
	}{{"aws", aws}, {"gcp", gcp}, {"hetzner", plain}} {
		for _, p := range x.ps {
			list = append(list, dcNet{p: p, source: x.src})
		}
	}
	n := newNetworks(list)
	if n.Len() != 7 {
		t.Fatalf("%d networks, want 7", n.Len())
	}
	for net, want := range map[string]string{
		"3.1.2.0/24":       "aws",
		"52.94.77.0/24":    "aws",
		"34.81.255.0/24":   "gcp",
		"5.9.200.0/24":     "hetzner",
		"88.198.1.7":       "hetzner",
		"2600:1f00:5::/48": "aws",
		"2600:1900::/48":   "gcp",
		"203.0.113.0/24":   "",
		"3.2.0.0/24":       "",
		"52.94.72.0/21":    "", // wider than the /22 it overlaps
		"":                 "",
		"junk":             "",
	} {
		got, ok := n.Match(net)
		if got != want || ok != (want != "") {
			t.Errorf("Match(%q) = %q %v, want %q", net, got, ok, want)
		}
	}
	var none *Networks
	if _, ok := none.Match("3.1.2.0/24"); ok {
		t.Error("no list matched")
	}
}

// A journey from a data-center network is flagged; old events go, the
// journeys and counts stay, and replay brings the events back.
func TestDataCenterNetworksAndOldEvents(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	spoolDir := filepath.Join(dir, "spool")
	clk := &clock{t: time.Date(2026, 9, 30, 10, 5, 0, 0, time.UTC)}
	w, err := spool.Open(spoolDir, "edge", "a", quiet(), spool.Options{Now: clk.now})
	must(t, err)
	e := edge.New(edge.Config{
		Sites: &edge.Sites{Root: filepath.Join(dir, "sites")}, Spool: w, Instance: "a", Version: "test",
		IPKey: []byte("0123456789abcdef"), TrustCloudflare: true, Log: quiet(),
		Metrics: edge.NewMetrics(prometheus.NewRegistry()), Now: clk.now,
	})
	beacon := func(j string) string {
		return `{"v":1,"j":"` + j + `","site":"lp.example.com","lp":"vsl","url":"/","in":1,"wd":0,"e":[{"k":"view"},{"k":"exit","vis":30000,"sc":80}]}`
	}
	sendFrom(e, beacon("journeyHOME"), iphone, "198.51.100.20")
	sendFrom(e, beacon("journeyCLOUD"), iphone, "5.9.12.34")
	w.Close()

	store, err := archive.Open("file://" + filepath.Join(dir, "archive"))
	must(t, err)
	l := New(Config{DB: db, Store: store, Spool: spoolDir, Log: quiet(), Now: clk.now, KeepEvents: 48 * time.Hour})
	n, err := l.ImportNetworks(ctx, "hetzner", []byte("5.9.0.0/16\n"), nil)
	must(t, err)
	if n != 1 {
		t.Fatalf("imported %d networks", n)
	}
	var key string
	must(t, db.QueryRow(ctx, `SELECT raw_key FROM funnels.dc_network_load WHERE source = 'hetzner'`).Scan(&key))
	if b, err := os.ReadFile(filepath.Join(dir, "archive", key)); err != nil || string(b) != "5.9.0.0/16\n" {
		t.Errorf("the list as received is not in the archive at %q: %v", key, err)
	}
	if _, err := l.ImportNetworks(ctx, "../x", []byte("5.9.0.0/16"), nil); err == nil {
		t.Error("a source name with a path was taken")
	}

	_, err = l.Archive(ctx)
	must(t, err)
	for {
		more, err := l.LoadOne(ctx)
		must(t, err)
		if !more {
			break
		}
	}
	clk.set(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	_, err = l.CloseDue(ctx)
	must(t, err)
	reasons := map[string]string{}
	rows, err := db.Query(ctx, `SELECT id, bot_reason FROM funnels_api.journey_v1`)
	must(t, err)
	for rows.Next() {
		var id, why string
		must(t, rows.Scan(&id, &why))
		reasons[id] = why
	}
	if reasons["journeyHOME"] != "" || reasons["journeyCLOUD"] != "data-center network (hetzner)" {
		t.Errorf("bot reasons = %v", reasons)
	}

	// Not old enough yet.
	if n, err := l.DropOldEvents(ctx); err != nil || n != 0 {
		t.Errorf("dropped %d events too early: %v", n, err)
	}
	clk.set(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	before := snapshot(t, db)
	if n, err := l.DropOldEvents(ctx); err != nil || n != 4 {
		t.Errorf("dropped %d events, want 4: %v", n, err)
	}
	if after := snapshot(t, db); after != before {
		t.Errorf("dropping events changed journeys or counts")
	}
	// Replay loads them again, and the counts come out the same.
	_, err = l.Replay(ctx, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	must(t, err)
	for {
		more, err := l.LoadOne(ctx)
		must(t, err)
		if !more {
			break
		}
	}
	// While the hour waits to close again, its events stay.
	if n, _ := l.DropOldEvents(ctx); n != 0 {
		t.Errorf("dropped %d events of an hour still to close", n)
	}
	_, err = l.CloseDue(ctx)
	must(t, err)
	if after := snapshot(t, db); after != before {
		t.Errorf("replay changed the counts:\n%s\n%s", before, after)
	}
}
