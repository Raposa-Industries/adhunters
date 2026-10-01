// Package web serves Create's page: the launcher folder, built into the
// binary, at "/".
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed launcher
var launcher embed.FS

// csp lets the page load only its own files. Pictures may also be blob: and
// data: URLs, because generated images arrive as base64 and references are
// previewed from the person's disk before upload.
const csp = "default-src 'self'; img-src 'self' blob: data:; style-src 'self' 'unsafe-inline'; " +
	"script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// types are set by hand, because the mime table Go falls back to can come
// from the host's /etc/mime.types, and an ES module served under the wrong
// type does not run.
var types = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
	".xlsx":  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

// Handler serves the launcher folder at "/", index.html at "/".
func Handler() http.Handler {
	sub, err := fs.Sub(launcher, "launcher")
	if err != nil {
		panic(err) // the embed pattern guarantees the folder
	}
	return serve(sub)
}

// OldPath is where the old launcher page lives now that the site's root
// opens Home.
const OldPath = "/old/"

// Home is where the site's root sends people: Intel (the tunnel sends
// /intel to intel-web).
const Home = "/intel/"

// Root routes create-web's address: "/" opens Home, the old launcher page and its API stay
// reachable under OldPath, and every other path goes to app unchanged, so
// /api/ and /_ads/ keep answering old bookmarks and open tabs.
func Root(app http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", http.RedirectHandler(Home, http.StatusFound))
	mux.Handle(OldPath, http.StripPrefix(strings.TrimSuffix(OldPath, "/"), app))
	mux.Handle("/", app)
	return mux
}

// serve is Handler over any folder, for tests.
func serve(sub fs.FS) http.Handler {
	files := http.FileServerFS(sub)
	return Secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		// A missing file is answered here: the file server's own error drops
		// Cache-Control. No directory listings either: a folder is served only
		// through its index.
		st, err := fs.Stat(sub, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if st.IsDir() {
			if _, err := fs.Stat(sub, path.Join(name, "index.html")); err != nil {
				http.NotFound(w, r)
				return
			}
			name = path.Join(name, "index.html")
		}
		if t, ok := types[path.Ext(name)]; ok {
			w.Header().Set("Content-Type", t)
		}
		files.ServeHTTP(w, r)
	}))
}

// Secure sets the headers every response of create-web carries.
func Secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Cache-Control", "no-cache")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Content-Security-Policy", csp)
		h.ServeHTTP(w, r)
	})
}
