package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/intel/internal/testdb"
)

func TestPages(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO intel.tb_account VALUES ('acme-sc', 'main', 1, 'Acme', 'PARTNER', 'USD', 'US/Eastern', now())`,
		`INSERT INTO intel.tb_campaign (campaign_id, account, group_id, name, status, is_active, daily_cap, cpc, settings, first_seen_at, fetched_at)
			VALUES (1, 'acme-sc', 10, 'BP mobile', 'RUNNING', true, 100, 0.3, '{}', now(), now()),
			       (2, 'acme-sc', 20, 'BP mobile (moved)', 'PAUSED', false, 50, 0.3, '{}', now(), now()),
			       (3, 'acme-sc', NULL, 'No group', 'PAUSED', false, 50, 0.3, '{}', now(), now())`,
		`INSERT INTO intel.campaign_link VALUES (1, 2, 'acme-sc', now(), 'launch')`,
		`INSERT INTO intel.tb_item (item_id, campaign_id, account, title, status, is_active, approval_state, settings, first_seen_at, fetched_at)
			VALUES (11, 1, 'acme-sc', 'Sip This at Breakfast', 'RUNNING', true, 'APPROVED', '{}', now(), now())`,
		`INSERT INTO intel.campaign_result VALUES (1, '7d', 'acme-sc', 100000, 1200, 1234.5, 3, 1180, 1000, 300, 20, 1800, 565.5, 0.458, 61.7, 45, 90, 'tracker', now())`,
		`INSERT INTO intel.campaign_result VALUES (99, '7d', 'redtrack:sandbox', 0, 0, 0, 0, 152, 0, 7, 0, 0, 0, NULL, NULL, NULL, NULL, 'tracker', now())`,
		`INSERT INTO intel.ad_result (item_id, time_window, campaign_id, account, impressions, clicks, spent, tracker_clicks, lp_views, lp_clicks,
			sales, revenue, profit, ctr, ctr_low, ctr_high, profit_per_1000, profit_per_1000_low, profit_per_1000_high, profit_basis, word, sureness, refreshed_at)
			VALUES (11, '7d', 1, 'acme-sc', 50000, 400, 200, 390, 350, 90, 0, 0, -200, 0.008, 0.007, 0.009, -4, -5, -3, 'estimated', 'worse', 'clear', now())`,
		`INSERT INTO intel.suggestion (key, kind, account, group_id, campaign_id, item_ids, title, why, launch_url, created_at, seen_at)
			VALUES ('k', 'pause-ads', 'acme-sc', 10, 1, '{11}', 'Pause 1 ad in BP mobile', 'It spent $200 with no sale.',
			        '/launch/taboola/acme-sc/g/10/c/1?do=pause-ads&ads=11&from=intel:1', now(), now())`,
		`INSERT INTO intel.alert (key, kind, account, campaign_id, title, detail, opened_at, seen_at)
			VALUES ('r', 'runaway', 'acme-sc', 1, 'BP mobile spent $300 today with no sale', 'x', now(), now())`,
		`INSERT INTO intel.job_mark VALUES ('round', now(), '')`,
		`INSERT INTO intel.status_change (campaign_id, account, old_status, new_status, changed_at, found_at)
			VALUES (1, 'acme-sc', 'PAUSED', 'RUNNING', now(), now())`,
	} {
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	srv := httptest.NewServer(Handler(db, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	get := func(path string, want ...string) {
		t.Helper()
		resp, err := noFollow.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
		}
		for _, w := range want {
			if !strings.Contains(string(b), w) {
				t.Errorf("%s: no %q in page", path, w)
			}
		}
	}
	get("/intel/", "BP mobile", "$1,234.50", "46%", "Gasto sem venda", "Pausar anúncios",
		`href="/launch/taboola/acme-sc/g/10/c/1?do=pause-ads&amp;ads=11&amp;from=intel:1"`, "Só no RedTrack", "sandbox")
	// The overview's campaigns sit under their account, whose row sums them.
	get("/intel/", `data-account="acme-sc"`, "3 campanhas", "id 1", "$565.50")
	get("/intel/taboola/acme-sc?w=7d", "BP mobile")
	get("/intel/taboola/acme-sc/g/10", "BP mobile")
	get("/intel/taboola/acme-sc/g/10/c/1", "Sip This at Breakfast", "pior", "com certeza", "0.80%",
		`href="/launch/taboola/acme-sc/g/10/c/1"`, "BP mobile (moved)", "depois", "rodando")
	get("/intel/taboola/acme-sc/g/20/c/2", "antes", "BP mobile")
	get("/intel/taboola/acme-sc/g/-", "No group", `href="/launch/taboola/acme-sc/g/-"`)
	get("/intel/taboola/acme-sc/g/-/c/3", "No group", `href="/launch/taboola/acme-sc/g/-/c/3"`)
	get("/intel/suggestions", "Pause 1 ad")
	get("/intel/alerts", "spent $300 today", "Status de entrega", "pausada", "rodando")
	get("/intel/_frame/frame.js", "mountFrame")
	get("/intel/_intel/intel.js", "mountFrame")
	get("/intel/api/search?q=moved", `"href":"/intel/taboola/acme-sc/g/20/c/2"`)

	// An old link with the wrong group lands on the right one.
	resp, err := noFollow.Get(srv.URL + "/intel/taboola/acme-sc/g/10/c/2?w=30d")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/intel/taboola/acme-sc/g/20/c/2?w=30d" {
		t.Fatalf("wrong group: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, p := range []string{"/intel/taboola/nobody-sc", "/intel/taboola/acme-sc/g/10/c/404", "/intel/_intel/../web.go"} {
		resp, _ := noFollow.Get(srv.URL + p)
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusNotFound, http.StatusMovedPermanently, http.StatusTemporaryRedirect:
		default:
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}

	// "Not now" from another site is refused; from the page it works and
	// goes back.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/intel/suggestions/1/not-now", strings.NewReader("back=/intel/"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err = noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site not-now: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/intel/suggestions/1/not-now",
		strings.NewReader(url.Values{"back": {"https://evil.example/"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Cf-Access-Authenticated-User-Email", "mari@example.com")
	resp, err = noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/intel/suggestions" {
		t.Fatalf("not-now: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	var state, by string
	db.QueryRow(ctx, `SELECT state, answered_by FROM intel.suggestion WHERE id = 1`).Scan(&state, &by)
	if state != "dismissed" || by != "mari@example.com" {
		t.Fatalf("suggestion %s by %q", state, by)
	}
}

func TestFormat(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{money(1234.5), "$1,234.50"},
		{money(-0.5), "−$0.50"},
		{thousands(1234567), "1,234,567"},
		{pct(0.0081), "0.81%"},
		{pct(0.123), "12.3%"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
