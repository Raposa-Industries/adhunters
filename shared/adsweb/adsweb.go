// Package adsweb serves the browser code the ad pages share (Launch, and
// create-web's launcher page until Launch replaces it): pairing creatives,
// headlines and CTAs into ads, Taboola's warnings, the ad id, the tracker
// link split, and Realize's bulk sheet (the built-in template, the zip and
// xlsx code). A binary mounts it under its own path:
//
//	mux.Handle("/launch/_ads/", http.StripPrefix("/launch/_ads", adsweb.Handler()))
//
// Tests: node --test shared/adsweb/test/*.test.js.
package adsweb

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed assets
var assets embed.FS

var types = map[string]string{
	".js":   "text/javascript; charset=utf-8",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

// Handler serves the files at "/" (mount it with http.StripPrefix): GET and
// HEAD only, the files by name, no listings.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		t, ok := types[path.Ext(name)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if st, err := fs.Stat(sub, name); err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", t)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
