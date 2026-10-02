package web

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// App is one app on hunt-teste.fyi: the path it claims and where it listens.
type App struct {
	Prefix string // "/launch": every path starting with it, as the tunnel's ^/launch did
	Addr   string // "127.0.0.1:8094"
}

// Apps are the apps behind create-web, as the tunnel routed them before the
// sign-in (platform/OPERATIONS.md has each one's port). All are on the data
// box except Raposa's pages, on the worker's private address.
var Apps = []App{
	{"/launch", "127.0.0.1:8094"},
	{"/create", "127.0.0.1:8095"},
	{"/intel", "127.0.0.1:8096"},
	{"/spy", "127.0.0.1:8097"},
	{"/funnels", "127.0.0.1:8099"},
	{"/desk", "127.0.0.1:8092"},
	// raposa-web stays on adhunters-worker and listens on its private
	// address; it strips /raposa itself (RAPOSA_WEB_BASE), so the prefix is
	// passed through like every other app's.
	{"/raposa", "10.20.1.10:8090"},
}

// Route names the app a request goes to, as Site sends it: an app's prefix
// without its slash ("spy"), or "home" for every other path. create-web's
// request metrics are counted by it.
func Route(apps []App, r *http.Request) string {
	for _, a := range apps {
		if strings.HasPrefix(r.URL.Path, a.Prefix) {
			return strings.TrimPrefix(a.Prefix, "/")
		}
	}
	return "home"
}

// Site sends each request to the app whose prefix it starts with, and every
// other path to Home (Root). Answers from the apps pass through as they are,
// streams included.
func Site(apps []App, log *slog.Logger) http.Handler {
	proxies := make([]http.Handler, len(apps))
	for i, a := range apps {
		target := &url.URL{Scheme: "http", Host: a.Addr}
		name := strings.TrimPrefix(a.Prefix, "/")
		proxies[i] = &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.Out.Host = pr.In.Host // the apps see the site's own address, as through the tunnel
			},
			FlushInterval: -1,
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				log.Warn("app not answering", "app", name, "err", err)
				http.Error(w, "o app "+name+" não está respondendo", http.StatusBadGateway)
			},
		}
	}
	root := Secure(Root())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i, a := range apps {
			if strings.HasPrefix(r.URL.Path, a.Prefix) {
				proxies[i].ServeHTTP(w, r)
				return
			}
		}
		root.ServeHTTP(w, r)
	})
}
