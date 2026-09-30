package adsweb

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServesTheSharedFiles(t *testing.T) {
	h := http.StripPrefix("/launch/_ads", Handler())
	for target, want := range map[string]int{
		"/launch/_ads/pairing.js":        200,
		"/launch/_ads/checks.js":         200,
		"/launch/_ads/template.js":       200,
		"/launch/_ads/realize-base.xlsx": 200,
		"/launch/_ads/":                  404,
		"/launch/_ads/nope.js":           404,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", target, rec.Code, want)
		}
	}
}
