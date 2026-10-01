package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSiteSendsEachPathToItsApp(t *testing.T) {
	app := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", "app's own")
			io.WriteString(w, name+" "+r.Host+" "+r.URL.RequestURI())
		}))
	}
	launch, spy := app("launch"), app("spy")
	defer launch.Close()
	defer spy.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	apps := []App{
		{"/launch", strings.TrimPrefix(launch.URL, "http://")},
		{"/spy", strings.TrimPrefix(spy.URL, "http://")},
		{"/intel", strings.TrimPrefix(down.URL, "http://")},
	}
	h := Site(apps, slog.New(slog.DiscardHandler))

	for path, want := range map[string]string{
		"/launch":                "launch hunt-teste.fyi /launch",
		"/launch/new?set=3":      "launch hunt-teste.fyi /launch/new?set=3",
		"/spy/api/creatives?q=a": "spy hunt-teste.fyi /spy/api/creatives?q=a",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "hunt-teste.fyi"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 || rec.Body.String() != want {
			t.Errorf("%s: %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
		if got := rec.Header().Values("Content-Security-Policy"); len(got) != 1 || got[0] != "app's own" {
			t.Errorf("%s: the app's headers changed: %q", path, got)
		}
	}

	res := get(t, h, "/old/bookmark")
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != Home {
		t.Errorf("unclaimed path: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if res := get(t, h, "/intel/"); res.StatusCode != http.StatusBadGateway {
		t.Errorf("app down: %d", res.StatusCode)
	}
}
