package frame

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestServesTheFramesFilesWithTheirTypes(t *testing.T) {
	h := http.StripPrefix("/launch/_frame", Handler())
	for target, want := range map[string]string{
		"/launch/_frame/frame.js":                  "text/javascript; charset=utf-8",
		"/launch/_frame/core.js":                   "text/javascript; charset=utf-8",
		"/launch/_frame/frame.css":                 "text/css; charset=utf-8",
		"/launch/_frame/loader.css":                "text/css; charset=utf-8",
		"/launch/_frame/mascot.svg":                "image/svg+xml",
		"/launch/_frame/fonts/archivo-125.woff2":   "font/woff2",
		"/launch/_frame/fonts/plex-mono-600.woff2": "font/woff2",
	} {
		rec := get(t, h, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d", target, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != want {
			t.Errorf("%s: type %q, want %q", target, got, want)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff", target)
		}
	}
}

func TestServesNothingElse(t *testing.T) {
	h := Handler()
	for _, target := range []string{"/", "/fonts/", "/fonts/README.md", "/nope.js", "/../frame.go"} {
		if rec := get(t, h, http.MethodGet, target); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", target, rec.Code)
		}
	}
	if rec := get(t, h, http.MethodPost, "/frame.js"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestTheModuleImportsOnlyItsNeighbour(t *testing.T) {
	// frame.js is loaded from wherever an app mounts it, so its imports must
	// be relative to itself.
	rec := get(t, Handler(), http.MethodGet, "/frame.js")
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "import ") && !strings.Contains(line, "from './") {
			t.Errorf("frame.js imports from outside its folder: %s", line)
		}
	}
}

func TestEveryAppGetsTheLoadingFox(t *testing.T) {
	// Apps load only frame.css, so the loader comes in through it.
	css := get(t, Handler(), http.MethodGet, "/frame.css").Body.String()
	if !strings.Contains(css, `@import url("loader.css")`) {
		t.Error("frame.css does not import loader.css")
	}
	loader := get(t, Handler(), http.MethodGet, "/loader.css").Body.String()
	for _, want := range []string{"body:not(.fr)::before", ".fr-loader", `url("mascot.svg")`} {
		if !strings.Contains(loader, want) {
			t.Errorf("loader.css has no %s", want)
		}
	}
	svg := get(t, Handler(), http.MethodGet, "/mascot.svg").Body.String()
	if !strings.Contains(svg, "prefers-reduced-motion: reduce") {
		t.Error("mascot.svg moves even when people ask for less motion")
	}
}
