// Package pages is funnels-web: Funnels' pages, in the Frame (shared/frame).
// They show what funnels-loader counted: journeys per landing site and
// page, each page's steps with their drop-off, split by Taboola campaign,
// the VSLs' plays and retention curves, one journey looked up by its click
// id, and the hosted sites with their versions. Nothing here writes;
// publishing a site stays on the command line (funnels-edge publish).
//
//	/funnels/                       sites and videos in the window
//	/funnels/s/<site>               one site: its landing pages and their steps
//	/funnels/s/<site>/lp?name=<lp>  one landing page, split by campaign
//	/funnels/videos, /funnels/v/<video>
//	/funnels/journeys?q=<click id or journey>
//	/funnels/hosting                hosted sites, versions and Clarity
//
// ?w= picks the window (today, yesterday, 7d, 30d), ?device= and ?sub1=
// narrow it. Hours still open are counted from the drafts, marked partial.
package pages

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/funnels/edge"
	"github.com/Raposa-Industries/adhunters/shared/frame"
)

//go:embed templates static
var files embed.FS

// Config is what the pages read.
type Config struct {
	DB    *pgxpool.Pool
	Sites *edge.Sites // the hosted sites' folder, read only; nil hides hosting
	Log   *slog.Logger
	Now   func() time.Time
}

// Handler serves every Funnels path under /funnels/.
func Handler(c Config) http.Handler {
	if c.Now == nil {
		c.Now = time.Now
	}
	t := template.Must(template.New("").Funcs(funcs).ParseFS(files, "templates/*.html"))
	h := &handler{st: store{db: c.DB}, sites: c.Sites, t: t, log: c.Log, now: c.Now}
	static, _ := fs.Sub(files, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /funnels/_frame/", http.StripPrefix("/funnels/_frame", frame.Handler()))
	mux.Handle("GET /funnels/_funnels/", http.StripPrefix("/funnels/_funnels", staticFiles(static)))
	mux.HandleFunc("GET /funnels", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/funnels/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /funnels/{$}", h.home)
	mux.HandleFunc("GET /funnels/s/{site}", h.site)
	mux.HandleFunc("GET /funnels/s/{site}/lp", h.lp)
	mux.HandleFunc("GET /funnels/videos", h.videos)
	mux.HandleFunc("GET /funnels/v/{video}", h.video)
	mux.HandleFunc("GET /funnels/journeys", h.journeys)
	mux.HandleFunc("GET /funnels/hosting", h.hosting)
	mux.HandleFunc("GET /funnels/api/search", h.search)
	return secure(mux)
}

func secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		h.ServeHTTP(w, r)
	})
}

var staticTypes = map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8"}

func staticFiles(sub fs.FS) http.Handler {
	fsrv := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		t, ok := staticTypes[path.Ext(name)]
		if !ok || strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", t)
		w.Header().Set("Cache-Control", "no-cache")
		fsrv.ServeHTTP(w, r)
	})
}

type handler struct {
	st    store
	sites *edge.Sites
	t     *template.Template
	log   *slog.Logger
	now   func() time.Time
}

type crumb struct{ Label, Href string }

type link struct {
	Label, Href string
	On          bool
}

// page is what every template gets; each page fills its part.
type page struct {
	Title, Tab, User string
	Window, Device   string
	Sub1             string
	Keep             template.URL // the window and filters as a query, for links
	Windows          []link
	Devices          []link
	Campaigns        []link
	Crumbs           []crumb
	Fresh            Freshness
	Hosting          bool

	Site      string
	LP        string
	Traffic   []Traffic
	LPs       []Traffic
	Steps     map[string][]StepRow
	StepOrder []string
	Cols      []string
	ByCamp    []CampaignRow
	Videos    []VideoRow
	Arms      []VideoRow
	Devs      []VideoRow
	Chart     template.HTML
	Query     string
	Journeys  []JourneyView
	Hosted    []Hosted
	Problem   string
}

// Hosted is one site the edge serves.
type Hosted struct {
	Host     string
	Clarity  string
	Versions []edge.Version
	Err      string
}

// user is who Cloudflare Access let in.
func user(r *http.Request) string {
	return r.Header.Get("Cf-Access-Authenticated-User-Email")
}

var devices = []struct{ ID, Label string }{{"", "Todos"}, {"phone", "Celular"}, {"tablet", "Tablet"}, {"desktop", "Computador"}}

func (h *handler) base(r *http.Request, title, tab string) (*page, Filter, error) {
	q := r.URL.Query()
	w := q.Get("w")
	if _, ok := windowNames[w]; !ok {
		w = "7d"
	}
	dev := q.Get("device")
	if dev != "phone" && dev != "tablet" && dev != "desktop" {
		dev = ""
	}
	sub1 := strings.TrimSpace(q.Get("sub1"))
	if len(sub1) > 100 {
		sub1 = ""
	}
	p := &page{Title: title, Tab: tab, User: user(r), Window: w, Device: dev, Sub1: sub1, Hosting: h.sites != nil}
	p.Keep = template.URL(keep(w, dev, sub1)) //nolint:gosec // built by url.Values.Encode
	for _, id := range Windows {
		p.Windows = append(p.Windows, link{windowNames[id], r.URL.Path + "?" + keep(id, dev, sub1) + extra(r), id == w})
	}
	for _, d := range devices {
		p.Devices = append(p.Devices, link{d.Label, r.URL.Path + "?" + keep(w, d.ID, sub1) + extra(r), d.ID == dev})
	}
	from, to := span(w, h.now())
	f := Filter{From: from, To: to, Device: dev, Sub1: sub1}
	var err error
	p.Fresh, err = h.st.freshness(r.Context(), f)
	return p, f, err
}

// keep is the window and filters as a query string.
func keep(w, dev, sub1 string) string {
	v := url.Values{"w": {w}}
	if dev != "" {
		v.Set("device", dev)
	}
	if sub1 != "" {
		v.Set("sub1", sub1)
	}
	return v.Encode()
}

// extra keeps a landing page's name on links that change the filters.
func extra(r *http.Request) string {
	if n := r.URL.Query().Get("name"); n != "" {
		return "&name=" + url.QueryEscape(n)
	}
	return ""
}

func (h *handler) render(w http.ResponseWriter, name string, p *page) {
	var b strings.Builder
	if err := h.t.ExecuteTemplate(&b, name, p); err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(b.String()))
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	h.log.Error("page failed", "err", err)
	http.Error(w, "Algo deu errado ao ler os números. Tente de novo em um minuto.", http.StatusInternalServerError)
}

func (h *handler) home(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.base(r, "Visão geral", "home")
	if err == nil {
		if p.Traffic, err = h.st.traffic(r.Context(), f, ""); err == nil {
			p.Videos, err = h.st.videos(r.Context(), f, "", "")
		}
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	h.render(w, "home", p)
}

func (h *handler) site(w http.ResponseWriter, r *http.Request) {
	site := r.PathValue("site")
	p, f, err := h.base(r, site, "home")
	if err != nil {
		h.fail(w, err)
		return
	}
	p.Site = site
	p.Crumbs = []crumb{{"Funnels", "/funnels/"}, {site, ""}}
	ctx := r.Context()
	if p.LPs, err = h.st.traffic(ctx, f, site); err == nil {
		if p.Steps, p.StepOrder, err = h.st.steps(ctx, f, site, ""); err == nil {
			err = h.campaignLinks(r, p, f, site)
		}
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	if len(p.LPs) == 0 && len(p.StepOrder) == 0 && f.Sub1 == "" && f.Device == "" {
		if ok, err := h.siteKnown(r, site); err != nil || !ok {
			http.NotFound(w, r)
			return
		}
	}
	h.render(w, "site", p)
}

// siteKnown says whether a site with no traffic in the window exists at
// all: seen in the last 30 days, or hosted here.
func (h *handler) siteKnown(r *http.Request, site string) (bool, error) {
	if h.sites != nil {
		if hosts, err := h.sites.Hosts(); err == nil {
			for _, x := range hosts {
				if x == site {
					return true, nil
				}
			}
		}
	}
	from, to := span("30d", h.now())
	t, err := h.st.traffic(r.Context(), Filter{From: from, To: to}, site)
	return len(t) > 0, err
}

func (h *handler) campaignLinks(r *http.Request, p *page, f Filter, site string) error {
	subs, err := h.st.campaigns(r.Context(), Filter{From: f.From, To: f.To}, site)
	if err != nil {
		return err
	}
	p.Campaigns = append(p.Campaigns, link{"Todas", r.URL.Path + "?" + keep(p.Window, p.Device, "") + extra(r), p.Sub1 == ""})
	for _, s := range subs {
		p.Campaigns = append(p.Campaigns, link{s, r.URL.Path + "?" + keep(p.Window, p.Device, s) + extra(r), s == p.Sub1})
	}
	if p.Sub1 != "" && !containsLink(p.Campaigns, p.Sub1) {
		p.Campaigns = append(p.Campaigns, link{p.Sub1, "", true})
	}
	return nil
}

func containsLink(l []link, label string) bool {
	for _, x := range l {
		if x.Label == label {
			return true
		}
	}
	return false
}

func (h *handler) lp(w http.ResponseWriter, r *http.Request) {
	site, lp := r.PathValue("site"), r.URL.Query().Get("name")
	if lp == "" || len(lp) > 300 {
		http.NotFound(w, r)
		return
	}
	p, f, err := h.base(r, lp, "home")
	if err != nil {
		h.fail(w, err)
		return
	}
	p.Site, p.LP = site, lp
	p.Crumbs = []crumb{{"Funnels", "/funnels/"}, {site, sitePath(site)}, {lp, ""}}
	ctx := r.Context()
	if p.Steps, p.StepOrder, err = h.st.steps(ctx, f, site, lp); err == nil {
		err = h.campaignLinks(r, p, f, site)
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	// The campaign split shows up to five steps past the view: the deepest
	// ones people reach.
	for _, s := range p.Steps[lp] {
		if s.Step != "view" && len(p.Cols) < 5 && !strings.HasPrefix(s.Step, "scroll") {
			p.Cols = append(p.Cols, s.Step)
		}
	}
	if p.ByCamp, err = h.st.byCampaign(ctx, Filter{From: f.From, To: f.To, Device: f.Device}, site, lp, p.Cols); err != nil {
		h.fail(w, err)
		return
	}
	h.render(w, "lp", p)
}

func (h *handler) videos(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.base(r, "Vídeos", "videos")
	if err == nil {
		p.Videos, err = h.st.videos(r.Context(), f, "", "")
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	h.render(w, "videos", p)
}

func (h *handler) video(w http.ResponseWriter, r *http.Request) {
	video := r.PathValue("video")
	p, f, err := h.base(r, video, "videos")
	if err != nil {
		h.fail(w, err)
		return
	}
	p.Crumbs = []crumb{{"Vídeos", "/funnels/videos"}, {video, ""}}
	ctx := r.Context()
	if p.Videos, err = h.st.videos(ctx, f, video, ""); err == nil {
		if p.Arms, err = h.st.videos(ctx, f, video, "arm"); err == nil {
			p.Devs, err = h.st.videos(ctx, f, video, "device")
		}
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	if len(p.Videos) == 0 {
		from, to := span("30d", h.now())
		if seen, err := h.st.videos(ctx, Filter{From: from, To: to}, video, ""); err != nil || len(seen) == 0 {
			http.NotFound(w, r)
			return
		}
	}
	plays := map[string]int64{}
	for _, a := range p.Arms {
		plays[a.Arm] = a.Plays
	}
	// The curve is not kept per campaign: with a campaign chosen, the
	// device filter alone applies, and the plays are counted the same way.
	if f.Sub1 != "" {
		arms, err := h.st.videos(ctx, Filter{From: f.From, To: f.To, Device: f.Device}, video, "arm")
		if err != nil {
			h.fail(w, err)
			return
		}
		for _, a := range arms {
			plays[a.Arm] = a.Plays
		}
	}
	curves, err := h.st.curves(ctx, f, video, plays)
	if err != nil {
		h.fail(w, err)
		return
	}
	var length int64
	for _, v := range p.Videos {
		length = max(length, v.LenS)
	}
	p.Chart = retentionChart(curves, int(length))
	h.render(w, "video", p)
}

func (h *handler) journeys(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.base(r, "Jornadas", "journeys")
	if err != nil {
		h.fail(w, err)
		return
	}
	p.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	if p.Query != "" && len(p.Query) <= 200 {
		if p.Journeys, err = h.st.findJourneys(r.Context(), p.Query); err != nil {
			h.fail(w, err)
			return
		}
	}
	h.render(w, "journeys", p)
}

func (h *handler) hosting(w http.ResponseWriter, r *http.Request) {
	if h.sites == nil {
		http.NotFound(w, r)
		return
	}
	p, _, err := h.base(r, "Hospedagem", "hosting")
	if err != nil {
		h.fail(w, err)
		return
	}
	hosts, err := h.sites.Hosts()
	if err != nil {
		p.Problem = "Não deu para ler a pasta dos sites: " + err.Error()
	}
	for _, host := range hosts {
		x := Hosted{Host: host}
		if c, err := h.sites.Config(host); err == nil {
			x.Clarity = c.Clarity
		}
		vs, err := h.sites.Versions(host)
		if err != nil {
			x.Err = err.Error()
		}
		// Newest first.
		for i := len(vs) - 1; i >= 0; i-- {
			x.Versions = append(x.Versions, vs[i])
		}
		p.Hosted = append(p.Hosted, x)
	}
	h.render(w, "hosting", p)
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	found := []Found{}
	if q != "" && len(q) <= 200 {
		var err error
		if found, err = h.st.search(r.Context(), q, h.now()); err != nil {
			h.fail(w, err)
			return
		}
		if found == nil {
			found = []Found{}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(found)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func sitePath(site string) string { return "/funnels/s/" + url.PathEscape(site) }

func lpPath(site, lp string) string { return sitePath(site) + "/lp?name=" + url.QueryEscape(lp) }

func videoPath(video string) string { return "/funnels/v/" + url.PathEscape(video) }

// stepName is a step in the page's words.
func stepName(s string) string {
	switch {
	case s == "view":
		return "Abriu a página"
	case s == "stay10s":
		return "Ficou 10 s"
	case strings.HasPrefix(s, "scroll"):
		return "Rolou " + strings.TrimPrefix(s, "scroll") + "%"
	case strings.HasPrefix(s, "click:"):
		return "Clicou: " + strings.TrimPrefix(s, "click:")
	case strings.HasPrefix(s, "form:"):
		return "Começou o formulário: " + strings.TrimPrefix(s, "form:")
	case strings.HasPrefix(s, "play:"):
		return "Deu play: " + strings.TrimPrefix(s, "play:")
	case strings.HasPrefix(s, "pitch:"):
		return "Chegou na oferta: " + strings.TrimPrefix(s, "pitch:")
	case strings.HasPrefix(s, "end:"):
		return "Viu até o fim: " + strings.TrimPrefix(s, "end:")
	}
	return s
}

var funcs = template.FuncMap{
	"sitePath":   sitePath,
	"lpPath":     lpPath,
	"videoPath":  videoPath,
	"stepName":   stepName,
	"windowName": func(w string) string { return windowNames[w] },
	"int":        thousands,
	"pct":        pct,
	"secs":       secs,
	"when":       func(t time.Time) string { return t.UTC().Format("02/01 15:04:05 UTC") },
	"hour":       func(t time.Time) string { return t.UTC().Format("02/01 15:00 UTC") },
	"ago":        ago,
	"bar":        func(v float64) string { return strconv.FormatFloat(math.Max(0, math.Min(1, v))*100, 'f', 1, 64) },
	"share":      func(a, b int64) float64 { return ratio(a, b) },
	"index64":    func(l []int64, i int) int64 { return l[i] },
	"int64":      func(n int) int64 { return int64(n) },
	"deviceName": deviceName,
	"version":    versionTime,
	"orNone": func(s string) string {
		if s == "" {
			return "(sem campanha)"
		}
		return s
	},
	"armName": func(s string) string {
		if s == "" {
			return "único"
		}
		return s
	},
}

func deviceName(d string) string {
	for _, x := range devices {
		if x.ID == d && d != "" {
			return x.Label
		}
	}
	return d
}

// versionTime reads a version's name (its UTC publish time).
func versionTime(name string) string {
	t, err := time.Parse("20060102T150405Z", name)
	if err != nil {
		return name
	}
	return t.Format("02/01/2006 15:04 UTC")
}

func pct(v float64) string {
	x := v * 100
	switch {
	case x == 0:
		return "0%"
	case x < 1:
		return strconv.FormatFloat(x, 'f', 2, 64) + "%"
	default:
		return strconv.FormatFloat(x, 'f', 1, 64) + "%"
	}
}

// secs is a duration in seconds as m:ss.
func secs(v float64) string {
	s := int64(math.Round(v))
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func thousands(n int64) string {
	s := itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "agora"
	case d < time.Hour:
		return fmt.Sprintf("há %d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("há %d h", int(d.Hours()))
	default:
		return fmt.Sprintf("há %d dias", int(d.Hours()/24))
	}
}
