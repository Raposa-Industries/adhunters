package pages

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/funnels/edge"
	"github.com/Raposa-Industries/adhunters/funnels/internal/testdb"
)

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Closed hours and the drafts of the open one show together, every page
// answers, and the numbers on them are the ones in the tables.
func TestPages(t *testing.T) {
	db := testdb.New(t)
	h := seeded(t, db)
	get := func(path string, want int) string {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Cf-Access-Authenticated-User-Email", "mari@example.com")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("GET %s = %d, want %d: %s", path, rec.Code, want, rec.Body.String())
		}
		return rec.Body.String()
	}
	has := func(page string, parts ...string) {
		t.Helper()
		for _, p := range parts {
			if !strings.Contains(page, p) {
				t.Errorf("page lacks %q", p)
			}
		}
	}

	home := get("/funnels/", 200)
	// 3 closed + 1 draft journey; the open hour is marked partial.
	has(home, "lp.example.com", "<td class=\"num\">4</td>", "parcial", "Contas fechadas até 01/10 13:00 UTC", `data-user="mari@example.com"`, ">bp<")
	get("/funnels", 301)

	site := get("/funnels/s/lp.example.com?w=today", 200)
	has(site, "Abriu a página", "Deu play: bp", "Clicou: buy", "/offer", "50549004")
	// The filters narrow: on phones only, the draft desktop journey is gone.
	phone := get("/funnels/s/lp.example.com?w=today&device=phone", 200)
	has(phone, "<td class=\"num\">3</td>")
	get("/funnels/s/nowhere.example.com", 404)

	lp := get("/funnels/s/lp.example.com/lp?name=vsl&w=today", 200)
	has(lp, "Por campanha", "50549004", "50549005", "66.7%")
	get("/funnels/s/lp.example.com/lp", 404)

	get("/funnels/videos", 200)
	video := get("/funnels/v/bp?w=today", 200)
	has(video, "<svg class=\"curve\"", "polyline", "Por braço", "Celular", "0:15") // 30 s heard over 2 plays
	get("/funnels/v/nothing", 404)

	j := get("/funnels/journeys?q=rt-click-1", 200)
	has(j, "journeyAAAA", "Clicou: buy", "chegou na oferta", "rt-click-1")
	j = get("/funnels/journeys?q=rt-click-2", 200)
	has(j, "hora ainda aberta", "robô?", "data-center network (aws)")
	has(get("/funnels/journeys?q=nobody", 200), "Nenhuma jornada")

	host := get("/funnels/hosting", 200)
	has(host, "Clarity abcd1234", "30/09/2026 20:00 UTC", "no ar")

	var found []Found
	must(t, json.Unmarshal([]byte(get("/funnels/api/search?q=offer", 200)), &found))
	if len(found) != 1 || found[0].Href != "/funnels/s/lp.example.com/lp?name=%2Foffer" {
		t.Errorf("search offer = %+v", found)
	}
	must(t, json.Unmarshal([]byte(get("/funnels/api/search?q=rt-click-2", 200)), &found))
	if len(found) != 1 || found[0].Title != "journeyDRAFT" {
		t.Errorf("search click id = %+v", found)
	}

	// The shell's files and the page's own are served; nothing else is.
	get("/funnels/_frame/frame.js", 200)
	get("/funnels/_funnels/funnels.js", 200)
	get("/funnels/_funnels/web.go", 404)
	get("/funnels/_funnels/templates/home.html", 404)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/funnels/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", rec.Code)
	}
}

// seeded fills the tables with one closed hour, one open hour's drafts, two
// journeys and a hosted site, and returns the pages over them.
func seeded(t testing.TB, db *pgxpool.Pool) http.Handler {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 15, 20, 0, 0, time.UTC)
	for _, q := range []string{
		`INSERT INTO funnels.hour_state (hour, closed_at, journeys) VALUES ('2026-10-01 13:00Z', '2026-10-01 14:05Z', 3)`,
		`INSERT INTO funnels.hour_state (hour, dirty_since, draft_at) VALUES ('2026-10-01 15:00Z', '2026-10-01 15:01Z', '2026-10-01 15:18Z')`,
		`INSERT INTO funnels.journey_hourly VALUES ('2026-10-01 13:00Z', 'lp.example.com', 'vsl', '50549004', '777', '1322', 'phone', 'US', 3, 2, 2, 90000, 1)`,
		`INSERT INTO funnels_draft.journey_hourly VALUES ('2026-10-01 15:00Z', 'lp.example.com', 'vsl', '50549005', '', '', 'desktop', 'US', 1, 1, 1, 10000, 0)`,
		`INSERT INTO funnels.step_hourly VALUES
		   ('2026-10-01 13:00Z', 'lp.example.com', 'vsl', 'view', '50549004', '777', '1322', 'phone', 3, 1, 1),
		   ('2026-10-01 13:00Z', 'lp.example.com', 'vsl', 'play:bp', '50549004', '777', '1322', 'phone', 2, 1, 0),
		   ('2026-10-01 13:00Z', 'lp.example.com', 'vsl', 'click:buy', '50549004', '777', '1322', 'phone', 1, 1, 0),
		   ('2026-10-01 13:00Z', 'lp.example.com', '/offer', 'view', '50549004', '777', '1322', 'phone', 1, 0, 0)`,
		`INSERT INTO funnels_draft.step_hourly VALUES ('2026-10-01 15:00Z', 'lp.example.com', 'vsl', 'view', '50549005', '', '', 'desktop', 1, 1, 0)`,
		`INSERT INTO funnels.video_hourly VALUES ('2026-10-01 13:00Z', 'lp.example.com', 'bp', 'a', '50549004', '777', '1322', 'phone', 3, 3, 2, 30, 20, 1)`,
		`INSERT INTO funnels.video_second_hourly SELECT '2026-10-01 13:00Z', 'bp', 'a', 'phone', s, CASE WHEN s < 5 THEN 2 ELSE 1 END FROM generate_series(0, 19) s`,
		`INSERT INTO funnels.journey (id, hour, started_at, last_at, site, first_lp, last_lp, last_step, lps, clickid, sub1, sub4, sub8, subs, device, country, visible_ms, max_scroll, had_input, bot_suspect, bot_reason)
		 VALUES ('journeyAAAA', '2026-10-01 13:00Z', '2026-10-01 13:10Z', '2026-10-01 13:12Z', 'lp.example.com', 'vsl', 'vsl', 'click:buy', 1, 'rt-click-1', '50549004', '777', '1322', '{}', 'phone', 'US', 45000, 80, true, false, '')`,
		`INSERT INTO funnels.journey_step VALUES ('journeyAAAA', 1, 'vsl', 'view', '2026-10-01 13:10Z'), ('journeyAAAA', 2, 'vsl', 'click:buy', '2026-10-01 13:12Z')`,
		`INSERT INTO funnels.journey_video VALUES ('journeyAAAA', 'bp', 'a', 'vsl', 20, 10, true, true, '{[0,15)}', 15, 14, true)`,
		`INSERT INTO funnels_draft.journey (id, hour, started_at, last_at, site, first_lp, last_lp, last_step, lps, clickid, sub1, sub4, sub8, subs, device, country, visible_ms, max_scroll, had_input, bot_suspect, bot_reason)
		 VALUES ('journeyDRAFT', '2026-10-01 15:00Z', '2026-10-01 15:02Z', '2026-10-01 15:03Z', 'lp.example.com', 'vsl', 'vsl', 'view', 1, 'rt-click-2', '50549005', '', '', '{}', 'desktop', 'US', 10000, 10, true, true, 'data-center network (aws)')`,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	sites := &edge.Sites{Root: t.TempDir()}
	src := t.TempDir()
	must(t, os.WriteFile(filepath.Join(src, "index.html"), []byte("<html><head></head><body>hi</body></html>"), 0o644))
	_, err := sites.Publish("lp.example.com", src, time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC))
	must(t, err)
	must(t, sites.SetConfig("lp.example.com", edge.SiteConfig{Clarity: "abcd1234"}))

	return Handler(Config{DB: db, Sites: sites, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }})
}
