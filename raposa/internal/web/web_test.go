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

	"github.com/Raposa-Industries/adhunters/raposa/internal/files"
	"github.com/Raposa-Industries/adhunters/raposa/internal/testdb"
)

func newServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	pool := testdb.New(t)
	s, err := New(pool, &files.Dir{Root: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, s
}

// noRedirect reads a redirect instead of following it.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func post(t *testing.T, ts *httptest.Server, path string, form url.Values, site string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if site != "" {
		req.Header.Set("Sec-Fetch-Site", site)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func get(t *testing.T, ts *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestAskFollowWatchAndStop(t *testing.T) {
	ts, s := newServer(t)
	ctx := context.Background()

	resp := post(t, ts, "/request", url.Values{"creative": {"42"}, "mode": {"deep"}, "by": {"ana"}}, "same-origin")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/i/1" {
		t.Fatalf("request answered %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Asking again opens the same one.
	if again := post(t, ts, "/request", url.Values{"creative": {"42"}}, ""); again.Header.Get("Location") != "/i/1" {
		t.Fatalf("a second request went to %q", again.Header.Get("Location"))
	}
	// A form from another site is refused.
	if cross := post(t, ts, "/request", url.Values{"creative": {"7"}}, "cross-site"); cross.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-site form answered %d", cross.StatusCode)
	}

	if _, body := get(t, ts, "/"); !strings.Contains(body, `<a href="/i/1">1</a>`) || !strings.Contains(body, "by ana") {
		t.Fatalf("the list does not show it:\n%s", body)
	}

	w := post(t, ts, "/i/1/watch", url.Values{"notification": {"Raposa"}, "kind": {"dark_found", "bogus"}, "scope": {"creative"}}, "")
	if w.StatusCode != http.StatusSeeOther {
		t.Fatalf("watch answered %d", w.StatusCode)
	}
	var creative *int32
	var kinds []string
	if err := s.db.QueryRow(ctx, `SELECT creative_id, kinds FROM raposa.watch`).Scan(&creative, &kinds); err != nil {
		t.Fatal(err)
	}
	if creative == nil || *creative != 42 || len(kinds) != 1 || kinds[0] != "dark_found" {
		t.Fatalf("watch stored as creative %v kinds %v", creative, kinds)
	}

	resp, body := get(t, ts, "/i/1")
	if resp.StatusCode != 200 || !strings.Contains(body, "Investigation 1: creative 42") ||
		!strings.Contains(body, "the creative") || !strings.Contains(body, "Stop it") {
		t.Fatalf("detail page (%d):\n%s", resp.StatusCode, body)
	}

	if st := post(t, ts, "/i/1/stop", nil, ""); st.StatusCode != http.StatusSeeOther {
		t.Fatalf("stop answered %d", st.StatusCode)
	}
	var status string
	if err := s.db.QueryRow(ctx, `SELECT status FROM raposa.investigation WHERE id = 1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "stopped" {
		t.Fatalf("status %s after stop", status)
	}

	if resp, _ := get(t, ts, "/i/99"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a missing investigation answered %d", resp.StatusCode)
	}
	if resp, body := get(t, ts, "/burns"); resp.StatusCode != 200 || !strings.Contains(body, "No line is burned") {
		t.Fatalf("burns (%d):\n%s", resp.StatusCode, body)
	}
}

func TestStoredPagesAreSandboxed(t *testing.T) {
	ts, s := newServer(t)
	if _, err := s.db.Exec(context.Background(), `
		INSERT INTO raposa.page (content_hash, page_key, text_digest, url, host, path, title, html)
		VALUES (gen_random_uuid(), 'x.com/', 'd', 'https://x.com/', 'x.com', '/', 'Offer',
		        '<script>alert(1)</script><img src="https://x.com/pixel.gif">')`); err != nil {
		t.Fatal(err)
	}
	resp, body := get(t, ts, "/p/1/html")
	if resp.StatusCode != 200 || !strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("stored html (%d): %s", resp.StatusCode, body)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox;") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("stored html went out with policy %q", csp)
	}
	if resp, _ := get(t, ts, "/p/1/kept"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a page never kept answered %d", resp.StatusCode)
	}
	if resp, body := get(t, ts, "/p/1"); resp.StatusCode != 200 || !strings.Contains(body, "Page 1: Offer") {
		t.Fatalf("page (%d):\n%s", resp.StatusCode, body)
	}
	if resp, _ := get(t, ts, "/f/not-a-hash"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a bad file address answered %d", resp.StatusCode)
	}
}

// Every table of the detail page and the burns page filled once, so a
// template that breaks on real rows fails here, not in front of someone.
func TestPagesWithEverythingFilled(t *testing.T) {
	ts, s := newServer(t)
	if _, err := s.db.Exec(context.Background(), `
		INSERT INTO raposa.page (content_hash, page_key, text_digest, url, host, path, title, is_dark, html, rendered_html)
		VALUES (gen_random_uuid(), 'x.com/offer', 'd', 'https://x.com/offer', 'x.com', '/offer', 'Offer', true, '<p>o</p>', '<p>o</p>');
		INSERT INTO raposa.asset (content_hash, media_type, size_bytes, object_key)
		VALUES ('00000000-0000-0000-0000-000000000001', 'image/png', 2048, 'files/900150983cd24fb0d6963f7d28e17f72');
		INSERT INTO raposa.page_asset (page_id, content_hash, role, source_url)
		VALUES (1, '00000000-0000-0000-0000-000000000001', 'image', 'https://x.com/a.png');
		INSERT INTO raposa.investigation (creative_id, mode, status, stage, is_cloaked, cloaked_confidence, breach_rung,
		                                  visits_done, visits_target, variants_count, completed_at, white_page_id, retry_of)
		VALUES (9, 'deep', 'completed', 'done', true, 87.5, 3, 40, 40, 1, now(), 1, NULL);
		INSERT INTO raposa.variant (investigation_id, path_hash, page_ids, first_page_id, label, visits, share_pct)
		VALUES (1, gen_random_uuid(), '{1,1}', 1, 'A', 35, 87.5);
		INSERT INTO raposa.evidence (investigation_id, creative_id, account_id, outcome, landing_page_id, final_url, title)
		VALUES (1, 9, 7, 'dark', 1, 'https://x.com/offer', 'Offer');
		INSERT INTO raposa.visit (investigation_id, purpose, rung, outcome, landed_page_id, exit_ip, error, status_code)
		VALUES (1, 'sample', 3, 'dark', 1, '1.2.3.4', NULL, 200), (1, 'ladder', 1, 'error', NULL, NULL, 'timed out', NULL);
		INSERT INTO raposa.log (investigation_id, line) VALUES (1, 'broke through on rung 3');
		INSERT INTO raposa.watch (investigation_id, pushcut_notification) VALUES (1, 'Raposa');
		INSERT INTO raposa.line_burn (scope, line_key, rung, visits, dark, other_visits, other_dark)
		VALUES ('site:x.com', 'res-4', 3, 5, 0, 20, 18);`); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]string{
		"/":      {"cloaked 88%", "rung 3"},
		"/i/1":   {"Cloaked: 88%", `<a href="/p/1">1</a> → <a href="/p/1">1</a>`, "1.2.3.4", "timed out", "broke through on rung 3", "Raposa", "0 sent"},
		"/p/1":   {"the kept copy", `<a href="/f/00000000-0000-0000-0000-000000000001">https://x.com/a.png</a>`, "2 KB"},
		"/burns": {"site:x.com", "18 dark of 20"},
	} {
		resp, body := get(t, ts, path)
		if resp.StatusCode != 200 {
			t.Fatalf("%s answered %d", path, resp.StatusCode)
		}
		if !strings.HasSuffix(strings.TrimSpace(body), "</html>") {
			t.Fatalf("%s stopped rendering part way:\n%s", path, body)
		}
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Fatalf("%s does not say %q:\n%s", path, w, body)
			}
		}
	}
}
