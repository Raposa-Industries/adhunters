package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPagesAndFiles(t *testing.T) {
	h := Secure(Handler())
	for _, c := range []struct {
		path, typ string
		code      int
		has       string
	}{
		{"/launch/", "text/html; charset=utf-8", 200, `src="/launch/app.js"`},
		{"/launch/new", "text/html; charset=utf-8", 200, "<main"},
		{"/launch/taboola/acme-sc/g/12/c/34", "text/html; charset=utf-8", 200, "<main"},
		{"/launch/campaigns", "text/html; charset=utf-8", 200, "<main"},
		{"/launch/manage.js", "text/javascript; charset=utf-8", 200, "newMenu"},
		{"/launch/style.css", "text/css; charset=utf-8", 200, ".steps-preview"},
		{"/launch/app.js", "text/javascript; charset=utf-8", 200, "mountFrame"},
		{"/launch/style.css", "text/css; charset=utf-8", 200, ".select-bar"},
		{"/launch/missing.js", "", 404, ""},
		{"/launch/../go.mod", "", 404, ""},
		{"/other/", "", 404, ""},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s: %d", c.path, rec.Code)
			continue
		}
		if c.code != 200 {
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != c.typ {
			t.Errorf("%s: type %q", c.path, got)
		}
		if !strings.Contains(rec.Body.String(), c.has) {
			t.Errorf("%s: no %q", c.path, c.has)
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: headers %v", c.path, rec.Header())
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/launch/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}
