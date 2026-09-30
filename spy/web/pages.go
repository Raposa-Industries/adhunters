package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

// pageFiles are Spy's pages: one HTML file per kind of page, and the
// scripts and styles they load. They draw themselves from the API.
//
//go:embed pages
var pageFiles embed.FS

// pageRoutes map a path to its page: the lists, and one page per ad,
// operator and publisher.
var pageRoutes = []struct {
	re   *regexp.Regexp
	file string
}{
	{regexp.MustCompile(`^/spy/(ads/?)?$`), "ads.html"},
	{regexp.MustCompile(`^/spy/ads/\d+$`), "ad.html"},
	{regexp.MustCompile(`^/spy/operators/?$`), "operators.html"},
	{regexp.MustCompile(`^/spy/operators/\d+$`), "operator.html"},
	{regexp.MustCompile(`^/spy/publishers/?$`), "publishers.html"},
	{regexp.MustCompile(`^/spy/publishers/\d+$`), "publisher.html"},
	{regexp.MustCompile(`^/spy/pulse/?$`), "pulse.html"},
}

var assetTypes = map[string]string{
	".js":  "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8",
}

// pageHandler serves the pages and /spy/assets/ (GET and HEAD only).
func (s *Server) pageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := r.URL.Path
		if name, ok := strings.CutPrefix(p, "/spy/assets/"); ok {
			name = path.Clean("/" + name)[1:]
			t, ok := assetTypes[path.Ext(name)]
			if !ok || strings.HasSuffix(name, ".html") {
				http.NotFound(w, r)
				return
			}
			s.serveFile(w, r, name, t)
			return
		}
		for _, rt := range pageRoutes {
			if rt.re.MatchString(p) {
				s.serveFile(w, r, rt.file, "text/html; charset=utf-8")
				return
			}
		}
		http.NotFound(w, r)
	})
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name, typ string) {
	b, err := fs.ReadFile(s.pages, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(b)
}
