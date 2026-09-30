// Package frame serves the Frame: the shell every AdHunters app's pages sit
// in (Command Frame, chosen 2026-09-28), with the Ember look and the parts
// the apps share. It is the files in assets/, built into each binary that
// mounts it, so every app shows the same shell without a build step:
//
//	mux.Handle("/launch/_frame/", http.StripPrefix("/launch/_frame", frame.Handler()))
//
// A page then loads _frame/frame.css and calls mountFrame from
// _frame/frame.js (assets/frame.js says how). Each app lives under its own
// path (/create/, /launch/), so one address with a router in front can hold
// them all and the links between apps are plain paths.
package frame

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed assets
var assets embed.FS

// types are set by hand: the table Go falls back to can come from the host's
// /etc/mime.types, and an ES module served under the wrong type does not run.
var types = map[string]string{
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".woff2": "font/woff2",
	".svg":   "image/svg+xml",
}

// Files is the Frame's files, for a binary that serves them its own way.
func Files() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err) // the embed pattern guarantees the folder
	}
	return sub
}

// Handler serves the Frame's files at "/" (mount it under a prefix with
// http.StripPrefix). Only GET and HEAD, only the files above by name: no
// listings, no README.
func Handler() http.Handler {
	sub := Files()
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
		// Fonts never change under a name; the code may change with any
		// deploy, so the browser asks again (and gets a 304 when it is the same).
		if path.Ext(name) == ".woff2" {
			w.Header().Set("Cache-Control", "public, max-age=604800")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
