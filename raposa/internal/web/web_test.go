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

	"github.com/Raposa-Industries/adhunters/raposa/internal/testdb"
	"github.com/Raposa-Industries/adhunters/shared/files"
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

	if _, body := get(t, ts, "/"); !strings.Contains(body, `href="/i/1">1</a>`) || !strings.Contains(body, "· ana") {
		t.Fatalf("the list does not show it:\n%s", body)
	}

	w := post(t, ts, "/i/1/watch", url.Values{"kind": {"dark_found", "bogus"}, "scope": {"creative"}, "by": {"vini"}}, "")
	if w.StatusCode != http.StatusSeeOther {
		t.Fatalf("watch answered %d", w.StatusCode)
	}
	var creative *int32
	var kinds []string
	var by string
	if err := s.db.QueryRow(ctx, `SELECT creative_id, kinds, created_by FROM raposa.watch`).Scan(&creative, &kinds, &by); err != nil {
		t.Fatal(err)
	}
	if creative == nil || *creative != 42 || len(kinds) != 1 || kinds[0] != "dark_found" || by != "vini" {
		t.Fatalf("watch stored as creative %v kinds %v by %q", creative, kinds, by)
	}

	resp, body := get(t, ts, "/i/1")
	if resp.StatusCode != 200 || !strings.Contains(body, "Investigação 1 · criativo 42") ||
		!strings.Contains(body, "toda investigação do criativo") || !strings.Contains(body, ">Parar</button>") ||
		!strings.Contains(body, "AdHunters operation") || strings.Contains(body, "Pushcut") {
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
	if resp, body := get(t, ts, "/burns"); resp.StatusCode != 200 || !strings.Contains(body, "Nenhuma linha está queimada") {
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
	if resp, body := get(t, ts, "/p/1"); resp.StatusCode != 200 || !strings.Contains(body, "Página 1 · Offer") {
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
		"/":      {"Com cloak 88%", "degrau 3"},
		"/i/1":   {"88% das visitas no degrau 3", `<a href="/p/1?i=1">1</a> → <a href="/p/1?i=1">1</a>`, "1.2.3.4", "timed out", "broke through on rung 3", "Funis inteiros", "Evidências", "esta investigação"},
		"/p/1":   {"Abrir a cópia guardada inteira", `<a href="/f/00000000-0000-0000-0000-000000000001">https://x.com/a.png</a>`, "2 KB", `src="/p/1/html" sandbox=""`},
		"/burns": {"x.com", "18 escuras de 20", "RES-4"},
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

// A cloaked investigation shows its splits per step and its video players,
// can be followed day by day, and is on the cloaked-ads page. Raposa's own
// quick checks stay out of the list unless asked for.
func TestSplitsFollowDaysAndCloaked(t *testing.T) {
	ts, s := newServer(t)
	ctx := context.Background()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO raposa.page (id, content_hash, page_key, text_digest, url, host, path, title, page_kind, is_dark, video_links) VALUES
		(1, md5('w')::uuid, 'news.com/a', 'w', 'https://news.com/a', 'news.com', '/a', 'Seven habits', 'article', false, '{}'),
		(2, md5('a1')::uuid, 'offer.xyz/a', 'a1', 'https://offer.xyz/a', 'offer.xyz', '/a', 'Doctor reveals', 'advertorial', true, '{}'),
		(3, md5('a2')::uuid, 'offer.xyz/b', 'a2', 'https://offer.xyz/b', 'offer.xyz', '/b', 'Nurse reveals', 'advertorial', true, '{}'),
		(4, md5('v')::uuid, 'offer.xyz/vsl', 'v', 'https://offer.xyz/vsl', 'offer.xyz', '/vsl', 'Watch now', 'vsl', true,
		 '{https://scripts.converteai.net/a1/players/p9/v4/player.js}')`)
	mustExec(`INSERT INTO raposa.investigation (id, creative_id, mode, status, is_cloaked, cloaked_confidence, breach_rung,
			requested_by, started_at, completed_at, raposa_data, reviewer_data) VALUES
		(1, 42, 'deep', 'completed', true, 75, 2, 'ana', now() - interval '1 hour', now(),
		 '{"domain": "offer.xyz", "title": "Doctor reveals", "pageKind": "advertorial"}', '{"domain": "news.com"}'),
		(2, 43, 'quick', 'completed', false, 0, NULL, 'raposa', now(), now(), '{}', '{}')`)
	// Four sample visits: three past the white page (two on advertorial a,
	// one on b, all then on the VSL) and one white.
	for i, pages := range [][]int{{2, 4}, {2, 4}, {3, 4}, {1}} {
		outcome := "dark"
		if len(pages) == 1 {
			outcome = "white"
		}
		mustExec(`INSERT INTO raposa.visit (id, investigation_id, purpose, rung, disguise_id, outcome, landed_page_id)
			VALUES ($1, 1, 'sample', 2, (SELECT id FROM raposa.disguise WHERE rung = 2), $2, $3)`, i+1, outcome, pages[0])
		for n, p := range pages {
			mustExec(`INSERT INTO raposa.step (visit_id, step_no, page_id) VALUES ($1, $2, $3)`, i+1, n+1, p)
		}
	}

	_, body := get(t, ts, "/i/1")
	for _, want := range []string{"Divisões por etapa", "Etapa 1 · advertoriais", "Doctor reveals", "67%", "Nurse reveals", "33%",
		"Etapa 2 · VSLs", "100%", `href="https://scripts.converteai.net/a1/players/p9/v4/player.js"`, "scripts.converteai.net/…/player.js",
		"Roda este anúncio de novo a cada 24 horas"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the investigation page lacks %q:\n%s", want, body)
		}
	}

	if resp := post(t, ts, "/i/1/follow", url.Values{"days": {"9"}}, ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("9 days answered %d", resp.StatusCode)
	}
	if resp := post(t, ts, "/i/1/follow", url.Values{"days": {"3"}, "by": {"ana"}}, ""); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("follow answered %d", resp.StatusCode)
	}
	post(t, ts, "/i/1/follow", url.Values{"days": {"5"}}, "") // already followed: nothing changes
	var days, open int
	if err := s.db.QueryRow(ctx, `SELECT max(days), count(*) FROM raposa.follow WHERE ended_at IS NULL`).Scan(&days, &open); err != nil || days != 3 || open != 1 {
		t.Fatalf("follows: %d open, %d days (%v)", open, days, err)
	}
	if _, body := get(t, ts, "/i/1"); !strings.Contains(body, "0 de 3 feitas") || !strings.Contains(body, `action="/follow/1/end"`) {
		t.Fatalf("the follow is not shown:\n%s", body)
	}

	// Day 1 ran: advertorial b took over.
	mustExec(`INSERT INTO raposa.investigation (id, creative_id, mode, status, follow_id, follow_day, started_at, completed_at)
		VALUES (3, 42, 'deep', 'completed', (SELECT id FROM raposa.follow), 1, now(), now())`)
	for i, p := range []int{3, 3, 2} {
		mustExec(`INSERT INTO raposa.visit (id, investigation_id, purpose, rung, disguise_id, outcome, landed_page_id)
			VALUES ($1, 3, 'sample', 2, (SELECT id FROM raposa.disguise WHERE rung = 2), 'dark', $2)`, 10+i, p)
		mustExec(`INSERT INTO raposa.step (visit_id, step_no, page_id) VALUES ($1, 1, $2)`, 10+i, p)
	}
	resp, body := get(t, ts, "/i/3/days")
	if resp.StatusCode != 200 {
		t.Fatalf("days answered %d:\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"Dia a dia · investigação", `Passaram da página branca: 75% → <span class="dark-ink">100%</span>`,
		`Etapa 1 «Nurse reveals»: 33% → <span class="dark-ink">67%</span>`, `Etapa 1 «Doctor reveals»: 67% → <span class="clean-ink">33%</span>`,
		"Etapa 2 «Watch now» (100% na véspera) não apareceu", "&#43;33"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the days page lacks %q:\n%s", want, body)
		}
	}

	if _, body := get(t, ts, "/cloaked"); !strings.Contains(body, "offer.xyz") || !strings.Contains(body, "news.com") ||
		!strings.Contains(body, "converteai.net") {
		t.Fatalf("the cloaked page:\n%s", body)
	}

	_, body = get(t, ts, "/")
	if strings.Contains(body, `href="/i/2"`) || !strings.Contains(body, "1 checagem rápida própria") {
		t.Fatalf("the list shows Raposa's own runs:\n%s", body)
	}
	if _, body := get(t, ts, "/?all=1"); !strings.Contains(body, `href="/i/2"`) {
		t.Fatalf("all=1 hides Raposa's own runs:\n%s", body)
	}
}

// Behind another site the pages answer under its path and link under it.
func TestBasePath(t *testing.T) {
	_, s := newServer(t)
	s.Base = "/raposa"
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	resp, body := get(t, ts, "/raposa/")
	if resp.StatusCode != 200 || !strings.Contains(body, `href="/raposa/_frame/frame.css"`) || !strings.Contains(body, `data-base="/raposa"`) ||
		!strings.Contains(body, `action="/raposa/request"`) {
		t.Fatalf("under /raposa (%d):\n%s", resp.StatusCode, body)
	}
	// The Frame and Raposa's own style and script answer under the path too,
	// with the types a browser runs them under.
	for path, want := range map[string]string{
		"/raposa/_frame/frame.js":    "text/javascript; charset=utf-8",
		"/raposa/_frame/core.js":     "text/javascript; charset=utf-8",
		"/raposa/_frame/frame.css":   "text/css; charset=utf-8",
		"/raposa/_raposa/raposa.js":  "text/javascript; charset=utf-8",
		"/raposa/_raposa/raposa.css": "text/css; charset=utf-8",
	} {
		resp, _ := get(t, ts, path)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != want {
			t.Fatalf("%s answered %d %q", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	if resp, _ := get(t, ts, "/raposa/_raposa/../web.go"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a file outside the assets answered %d", resp.StatusCode)
	}
	if r := post(t, ts, "/raposa/request", url.Values{"creative": {"5"}}, ""); r.Header.Get("Location") != "/raposa/i/1" {
		t.Fatalf("request went to %q", r.Header.Get("Location"))
	}
}
