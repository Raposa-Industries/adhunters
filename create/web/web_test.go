package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Result()
}

func checkHeaders(t *testing.T, res *http.Response) {
	t.Helper()
	want := map[string]string{
		"Cache-Control":           "no-cache",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": csp,
	}
	for k, v := range want {
		if got := res.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "img-src 'self' blob: data:") {
		t.Errorf("csp lost a rule: %s", csp)
	}
}

func TestServesEmbeddedIndex(t *testing.T) {
	res := get(t, Handler(), "/")
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type %q", ct)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(strings.ToLower(string(body)), "<html") {
		t.Errorf("not the page: %.80s", body)
	}
	checkHeaders(t, res)
}

func TestTypesAndMissing(t *testing.T) {
	h := serve(fstest.MapFS{
		"index.html":   {Data: []byte("<!doctype html><html></html>")},
		"app.js":       {Data: []byte("export const x = 1;")},
		"style.css":    {Data: []byte("body{}")},
		"lib/util.mjs": {Data: []byte("export {}")},
	})
	for path, want := range map[string]string{
		"/app.js":       "text/javascript",
		"/lib/util.mjs": "text/javascript",
		"/style.css":    "text/css",
	} {
		res := get(t, h, path)
		if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), want) {
			t.Errorf("%s: %d %q", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
		checkHeaders(t, res)
	}
	if res := get(t, h, "/nope.js"); res.StatusCode != 404 {
		t.Errorf("missing file: %d", res.StatusCode)
	} else {
		checkHeaders(t, res)
	}
	if res := get(t, h, "/lib/"); res.StatusCode != 404 {
		t.Errorf("folder listing served: %d", res.StatusCode)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
	if rec.Code != 405 {
		t.Errorf("POST /: %d", rec.Code)
	}
}

func TestRootOpensLaunch(t *testing.T) {
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "app "+r.URL.Path)
	})
	h := Root(app)
	for _, c := range []struct{ path, loc, body string }{
		{"/", "/launch/", ""},
		{"/?x=1", "/launch/", ""},
		{"/old", "/old/", ""},
		{"/old/", "", "app /"},
		{"/old/app.js", "", "app /app.js"},
		{"/old/api/status", "", "app /api/status"},
		{"/old/_ads/sheet.js", "", "app /_ads/sheet.js"},
		{"/api/status", "", "app /api/status"},
		{"/style.css", "", "app /style.css"},
	} {
		res := get(t, h, c.path)
		body, _ := io.ReadAll(res.Body)
		if c.loc != "" {
			if res.StatusCode/100 != 3 || res.Header.Get("Location") != c.loc {
				t.Errorf("%s: %d to %q, want a redirect to %q", c.path, res.StatusCode, res.Header.Get("Location"), c.loc)
			}
			continue
		}
		if res.StatusCode != 200 || string(body) != c.body {
			t.Errorf("%s: %d %q, want %q", c.path, res.StatusCode, body, c.body)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/old/api/plan", nil))
	if rec.Body.String() != "app /api/plan" {
		t.Errorf("POST /old/api/plan reached %q", rec.Body.String())
	}
}

func TestOldPageLoadsUnderOldPath(t *testing.T) {
	h := Root(Handler())
	res := get(t, h, "/old/")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(body), `src="app.js"`) {
		t.Fatalf("/old/: %d %.120s", res.StatusCode, body)
	}
	if res := get(t, h, "/old/app.js"); res.StatusCode != 200 {
		t.Errorf("/old/app.js: %d", res.StatusCode)
	}
}
