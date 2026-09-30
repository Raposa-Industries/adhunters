// Package web serves Launch's pages under /launch/: the files in pages,
// built into the binary. Every page path (/launch/, /launch/new,
// /launch/taboola/<account>/g/<group>/c/<campaign>, …) gets index.html, and
// app.js draws the page the path names, so a link to any campaign opens it.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed pages
var pages embed.FS

// csp lets the page load only its own files. Pictures may also come from
// the network's image servers (https:) and from the person's disk (blob:,
// data:) before upload.
const csp = "default-src 'self'; img-src 'self' blob: data: https:; style-src 'self' 'unsafe-inline'; " +
	"script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// types are set by hand: the host's mime table may not know them, and an
// ES module served under the wrong type does not run.
var types = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
}

// Handler serves pages under /launch/.
func Handler() http.Handler {
	sub, err := fs.Sub(pages, "pages")
	if err != nil {
		panic(err) // the embed pattern guarantees the folder
	}
	return serve(sub)
}

func serve(sub fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rest, ok := strings.CutPrefix(path.Clean(r.URL.Path), "/launch")
		if !ok {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(rest, "/")
		// A path with a file extension is a file, and a missing one is a
		// 404; any other path is a page, drawn by app.js.
		if ext := path.Ext(name); ext == "" {
			name = "index.html"
		}
		data, err := fs.ReadFile(sub, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if t, ok := types[path.Ext(name)]; ok {
			w.Header().Set("Content-Type", t)
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		// Pages change with each release and are small: always revalidate.
		w.Header().Set("Cache-Control", "no-cache")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(data)
	})
}

// Secure adds the headers every answer carries: the page may load only its
// own files, is never framed, and types are never guessed.
func Secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
