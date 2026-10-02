package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// exportAds is the ads list, with the same filters, range and sort, as one
// HTML file to keep or send: each creative's image, headline, description,
// call to action and brand, its operator and vertical, and its numbers.
// limit is 100 by default, 300 at most.
func (s *Server) exportAds(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("limit") == "" {
		q.Set("limit", "100")
	}
	q.Del("offset")
	r.URL.RawQuery = q.Encode()
	v, err := s.adsUpTo(r, 300)
	var b errBad
	switch {
	case err == nil:
	case errors.As(err, &b):
		http.Error(w, b.msg, http.StatusBadRequest)
		return
	case r.Context().Err() != nil:
		return
	default:
		s.log.Error("spy export failed", "err", err)
		http.Error(w, "something failed on our side", http.StatusInternalServerError)
		return
	}
	out := v.(map[string]any)
	win := out["window"].(map[string]any)
	items := out["items"].([]map[string]any)
	base := "https://" + r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		base = "https://" + h
	}
	if strings.HasPrefix(r.Host, "127.0.0.1") || strings.HasPrefix(r.Host, "localhost") {
		base = "http://" + r.Host
	}
	page := exportPage{
		Range:   rangeText(win),
		Made:    s.cfg.Now().In(saoPaulo).Format("02/01/2006 15:04"),
		Total:   out["total"],
		Shown:   len(items),
		Filters: filtersText(r),
	}
	for _, it := range items {
		page.Ads = append(page.Ads, exportAd{
			Link:        fmt.Sprintf("%s/spy/ads/%v", base, it["id"]),
			Image:       str(it["image_url"]),
			Headline:    str(it["headline"]),
			Description: str(it["description"]),
			CTA:         str(it["cta"]),
			Brand:       str(it["brand"]),
			Operator:    strings.TrimSpace(str(it["operator_name"]) + " " + paren(str(it["operator_code"]))),
			Vertical:    joinNonEmpty(" · ", str(it["category_name"]), str(it["vertical_name"])),
			Presence:    presenceText(it["presence"]),
			Momentum:    momentumText(it["momentum"]),
			Sightings:   numText(it["sightings"]),
			FirstSeen:   dayText(it["first_seen_at"]),
			LastSeen:    dayText(it["last_seen_at"]),
			Running:     it["running"] == true,
		})
	}
	var buf bytes.Buffer
	if err := exportTmpl.Execute(&buf, page); err != nil {
		s.log.Error("spy export failed", "err", err)
		http.Error(w, "something failed on our side", http.StatusInternalServerError)
		return
	}
	name := "spy-anuncios-" + s.cfg.Now().In(saoPaulo).Format("2006-01-02-1504") + ".html"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

type exportPage struct {
	Range, Made, Filters string
	Total                any
	Shown                int
	Ads                  []exportAd
}

type exportAd struct {
	Link, Image, Headline, Description, CTA, Brand, Operator, Vertical string
	Presence, Momentum, Sightings, FirstSeen, LastSeen                 string
	Running                                                            bool
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func paren(s string) string {
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// number reads any number a row holds (an int, a float, a NUMERIC).
func number(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.Trim(string(b), `"`), 64)
	return f, err == nil
}

// ptNum writes a number as people in Brazil read it: 1.234,5.
func ptNum(f float64, digits int) string {
	s := strconv.FormatFloat(math.Abs(f), 'f', digits, 64)
	whole, frac, _ := strings.Cut(s, ".")
	var g []string
	for len(whole) > 3 {
		g = append([]string{whole[len(whole)-3:]}, g...)
		whole = whole[:len(whole)-3]
	}
	g = append([]string{whole}, g...)
	out := strings.Join(g, ".")
	if frac != "" {
		out += "," + frac
	}
	if f < 0 {
		out = "-" + out
	}
	return out
}

func numText(v any) string {
	f, ok := number(v)
	if !ok {
		return "–"
	}
	return ptNum(f, 0)
}

func presenceText(v any) string {
	f, ok := number(v)
	if !ok {
		return "–"
	}
	if f < 10 {
		return ptNum(f, 2)
	}
	return ptNum(f, 1)
}

func momentumText(v any) string {
	f, ok := number(v)
	if !ok {
		return "–"
	}
	c := (f - 1) * 100
	sign := ""
	if math.Round(c) > 0 {
		sign = "+"
	}
	return sign + ptNum(math.Round(c), 0) + "%"
}

func dayText(v any) string {
	t, ok := v.(time.Time)
	if !ok {
		return "–"
	}
	return t.In(saoPaulo).Format("02/01/06")
}

// rangeText is the window as the pages write it.
func rangeText(win map[string]any) string {
	from, _ := win["from"].(time.Time)
	to, _ := win["to"].(time.Time)
	if h, _ := win["hours"].(int); h > 0 {
		return fmt.Sprintf("Últimas %d horas, até %s", h, to.In(saoPaulo).Format("02/01 15:04"))
	}
	if win["recent"] == true {
		return "Últimas 24 horas, até " + to.In(saoPaulo).Format("02/01 15:04")
	}
	return from.In(saoPaulo).Format("02/01/06") + " a " + to.Add(-time.Second).In(saoPaulo).Format("02/01/06")
}

// filtersText lists the filters the export was made with.
func filtersText(r *http.Request) string {
	q := r.URL.Query()
	var out []string
	for _, f := range [][2]string{{"q", "busca"}, {"category", "categoria"}, {"vertical", "vertical"}, {"operator", "operador"},
		{"publisher", "publisher"}, {"network", "rede"}, {"account", "conta"}, {"tracker", "tracker"}, {"affiliate", "afiliado"},
		{"device", "dispositivo"}, {"status", "situação"}, {"min_days", "dias ativos"}, {"sort", "ordem"}} {
		if vs := q[f[0]]; len(vs) > 0 && vs[0] != "" {
			out = append(out, f[1]+": "+strings.Join(vs, ", "))
		}
	}
	if q.Get("rev") == "1" {
		out = append(out, "ordem invertida")
	}
	if q.Get("hidden") == "1" {
		out = append(out, "com os operadores ocultos")
	}
	if q.Get("vs") == "before" {
		out = append(out, "contra o período anterior")
	}
	return strings.Join(out, " · ")
}

var exportTmpl = template.Must(template.New("export").Parse(`<!doctype html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>Anúncios · Spy · {{.Range}}</title>
<style>
:root { color-scheme: light dark; --bg: #faf8f5; --card: #fff; --ink: #1d1b19; --muted: #6b655e; --line: #e6e0d8; --accent: #c2541b; }
@media (prefers-color-scheme: dark) { :root { --bg: #161412; --card: #201d1a; --ink: #f1ece6; --muted: #a39b91; --line: #34302b; --accent: #f08a4b; } }
* { box-sizing: border-box; }
body { margin: 0; padding: 24px 16px; background: var(--bg); color: var(--ink); font: 15px/1.45 system-ui, -apple-system, "Segoe UI", sans-serif; }
header { max-width: 1200px; margin: 0 auto 20px; }
h1 { margin: 0 0 4px; font-size: 22px; }
.muted { color: var(--muted); font-size: 13px; }
.grid { max-width: 1200px; margin: 0 auto; display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 16px; }
article { background: var(--card); border: 1px solid var(--line); border-radius: 10px; overflow: hidden; display: flex; flex-direction: column; }
article img { width: 100%; aspect-ratio: 16 / 9; object-fit: cover; background: var(--line); display: block; }
.body { padding: 12px 14px 14px; display: flex; flex-direction: column; gap: 6px; flex: 1; }
.head { font-weight: 600; font-size: 16px; }
.desc { color: var(--muted); }
.cta { align-self: flex-start; border: 1px solid var(--accent); color: var(--accent); border-radius: 6px; padding: 2px 10px; font-size: 13px; font-weight: 600; }
dl { display: grid; grid-template-columns: auto 1fr; gap: 2px 10px; margin: 6px 0 0; font-size: 13px; }
dt { color: var(--muted); }
dd { margin: 0; }
a { color: var(--accent); }
</style>
</head>
<body>
<header>
<h1>Anúncios do Spy</h1>
<div class="muted">{{.Range}} · {{.Shown}} de {{.Total}} criativos · feito em {{.Made}} (horário de São Paulo){{if .Filters}} · {{.Filters}}{{end}}</div>
</header>
<main class="grid">
{{range .Ads}}<article>
{{if .Image}}<img src="{{.Image}}" alt="" loading="lazy" referrerpolicy="no-referrer">{{end}}
<div class="body">
<div class="head">{{if .Headline}}{{.Headline}}{{else}}Sem headline{{end}}</div>
{{if .Description}}<div class="desc">{{.Description}}</div>{{end}}
{{if .CTA}}<span class="cta">{{.CTA}}</span>{{end}}
<dl>
<dt>Marca</dt><dd>{{if .Brand}}{{.Brand}}{{else}}–{{end}}</dd>
<dt>Operador</dt><dd>{{if .Operator}}{{.Operator}}{{else}}–{{end}}</dd>
<dt>Vertical</dt><dd>{{if .Vertical}}{{.Vertical}}{{else}}–{{end}}</dd>
<dt>Presença</dt><dd>{{.Presence}} · {{.Momentum}} contra o usual</dd>
<dt>Vistas</dt><dd>{{.Sightings}}</dd>
<dt>No ar</dt><dd>{{.FirstSeen}} a {{.LastSeen}}{{if not .Running}} (saiu){{end}}</dd>
</dl>
<a href="{{.Link}}">Abrir no Spy</a>
</div>
</article>
{{end}}</main>
</body>
</html>
`))
