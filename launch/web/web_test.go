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
		{"/launch/route.js", "text/javascript; charset=utf-8", 200, "'requests', 'history', 'presets', 'drafts'"},
		{"/launch/accountrows.js", "text/javascript; charset=utf-8", 200, "export function joining"},
		{"/launch/accounts.js", "text/javascript; charset=utf-8", 200, "Nova conta"},
		// Pedidos, Histórico, Presets and Rascunhos are no longer screens:
		// their addresses still get the page, which opens Campanhas.
		{"/launch/requests/12", "text/html; charset=utf-8", 200, "<main"},
		{"/launch/history", "text/html; charset=utf-8", 200, "<main"},
		{"/launch/requests.js", "", 404, ""},
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

// The top bar has only Campanhas and Contas, and Contas never says where a
// login comes from (IMPLEMENT d587e1b829).
func TestOnlyCampanhasAndContas(t *testing.T) {
	h := Handler()
	get := func(p string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		return rec.Body.String()
	}
	app := get("/launch/app.js")
	for _, gone := range []string{"label: 'Pedidos'", "label: 'Rascunhos'", "label: 'Histórico'", "label: 'Presets'", "/launch/presets", "requests.js", "history.js"} {
		if strings.Contains(app, gone) {
			t.Errorf("app.js still has %q", gone)
		}
	}
	if strings.Contains(get("/launch/presets.js"), "/launch/presets'") {
		t.Error("presets.js still links to the Presets screen")
	}
	if c := get("/launch/accounts.js"); strings.Contains(c, "do servidor") || strings.Contains(c, "Adicionar login") {
		t.Error("Contas still names the server's login or Adicionar login")
	}
}
