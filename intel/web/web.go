// Package web is intel-web: Intel's pages, in the Frame (shared/frame).
// They show what intel-numbers worked out: results per campaign and ad with
// likely ranges, alerts, and suggestions. Intel changes nothing on Taboola:
// a suggestion's button opens Launch with the change filled in, and a person
// makes it there. The one thing a person writes here is "not now" on a
// suggestion.
//
// Paths hold Taboola's tree the way Launch's do, so G then L (or I) keeps
// the same object:
//
//	/intel/                                            every campaign, suggestions and alerts
//	/intel/taboola/<account>                           one account
//	/intel/taboola/<account>/g/<group>                 one campaign group
//	/intel/taboola/<account>/g/<group>/c/<campaign>    one campaign and its ads
//	/intel/suggestions, /intel/alerts
//
// ?w= picks the window: today, yesterday, 7d (the default) or 30d.
package web

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

	"github.com/Raposa-Industries/adhunters/shared/frame"
)

//go:embed templates static
var files embed.FS

// Handler serves every Intel path under /intel/.
func Handler(db *pgxpool.Pool, log *slog.Logger) http.Handler {
	t := template.Must(template.New("").Funcs(funcs).ParseFS(files, "templates/*.html"))
	h := &handler{st: store{db: db}, t: t, log: log}
	static, _ := fs.Sub(files, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /intel/_frame/", http.StripPrefix("/intel/_frame", frame.Handler()))
	mux.Handle("GET /intel/_intel/", http.StripPrefix("/intel/_intel", staticFiles(static)))
	mux.HandleFunc("GET /intel", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/intel/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /intel/{$}", h.home)
	mux.HandleFunc("GET /intel/taboola/{account}", h.account)
	mux.HandleFunc("GET /intel/taboola/{account}/g/{group}", h.account)
	mux.HandleFunc("GET /intel/taboola/{account}/g/{group}/c/{campaign}", h.campaign)
	mux.HandleFunc("GET /intel/suggestions", h.suggestionsPage)
	mux.HandleFunc("GET /intel/alerts", h.alertsPage)
	mux.HandleFunc("POST /intel/suggestions/{id}/not-now", h.notNow)
	mux.HandleFunc("GET /intel/api/search", h.search)
	// "Not now" is the one write; a form another site makes the browser
	// post is refused.
	return secure(http.NewCrossOriginProtection().Handler(mux))
}

func secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		// Ad thumbnails come from Taboola's image servers.
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: data:; script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
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
	st  store
	t   *template.Template
	log *slog.Logger
}

type crumb struct{ Label, Href string }

type windowLink struct {
	ID, Label, Href string
	On              bool
}

// page is what every template gets; each page fills its part.
type page struct {
	Title    string
	Tab      string
	User     string
	Window   string
	Windows  []windowLink
	Accounts []string
	Account  string
	Group    int64
	AsOf     *time.Time
	Path     string
	Crumbs   []crumb

	Campaigns   []Campaign
	TrackerOnly []Campaign
	Campaign    *Campaign
	Results     []*Result
	Ads         []Ad
	Line        []LineCampaign
	Suggestions []Suggestion
	Alerts      []Alert
	Statuses    []StatusChange
	Recent      bool
}

// user is who Cloudflare Access let in.
func user(r *http.Request) string {
	return r.Header.Get("Cf-Access-Authenticated-User-Email")
}

func (h *handler) base(r *http.Request, title, tab string) (*page, error) {
	w := r.URL.Query().Get("w")
	if _, ok := windowNames[w]; !ok {
		w = "7d"
	}
	p := &page{Title: title, Tab: tab, User: user(r), Window: w, Path: r.URL.Path}
	for _, id := range Windows {
		p.Windows = append(p.Windows, windowLink{ID: id, Label: windowNames[id], Href: r.URL.Path + "?w=" + id, On: id == w})
	}
	var err error
	if p.Accounts, err = h.st.accounts(r.Context()); err != nil {
		return nil, err
	}
	if p.AsOf, err = h.st.asOf(r.Context()); err != nil {
		return nil, err
	}
	return p, nil
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
	p, err := h.base(r, "Visão geral", "home")
	if err != nil {
		h.fail(w, err)
		return
	}
	ctx := r.Context()
	if p.Suggestions, err = h.st.suggestions(ctx, 0, false); err == nil {
		if p.Alerts, err = h.st.alerts(ctx, 0, false); err == nil {
			if p.Campaigns, err = h.st.campaigns(ctx, "", 0, p.Window); err == nil {
				p.TrackerOnly, err = h.st.trackerOnly(ctx, p.Window)
			}
		}
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	h.render(w, "home", p)
}

func (h *handler) account(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("account")
	var group int64
	if g := r.PathValue("group"); g != "" {
		var err error
		if group, err = parseGroup(g); err != nil {
			http.NotFound(w, r)
			return
		}
		if group == 0 {
			group = noGroup
		}
	}
	p, err := h.base(r, account, "home")
	if err != nil {
		h.fail(w, err)
		return
	}
	ok, err := h.st.accountKnown(r.Context(), account)
	if err != nil {
		h.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	p.Account, p.Group = account, group
	p.Crumbs = []crumb{{"Intel", "/intel/"}, {account, accountPath(account)}}
	if group == noGroup {
		p.Title = "Sem grupo"
		p.Crumbs = append(p.Crumbs, crumb{p.Title, ""})
	} else if group != 0 {
		p.Title = fmt.Sprintf("Grupo %d", group)
		p.Crumbs = append(p.Crumbs, crumb{p.Title, ""})
	} else {
		p.Crumbs[1].Href = ""
	}
	if p.Campaigns, err = h.st.campaigns(r.Context(), account, group, p.Window); err != nil {
		h.fail(w, err)
		return
	}
	h.render(w, "account", p)
}

func (h *handler) campaign(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("account")
	group, err1 := parseGroup(r.PathValue("group"))
	id, err2 := strconv.ParseInt(r.PathValue("campaign"), 10, 64)
	if err1 != nil || err2 != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	c, err := h.st.campaign(ctx, account, id)
	if err != nil {
		h.fail(w, err)
		return
	}
	if c == nil {
		http.NotFound(w, r)
		return
	}
	// A campaign's group is what Taboola says; an old link lands on the
	// right path.
	if c.GroupID != group {
		to := campaignPath(account, c.GroupID, id)
		if q := r.URL.RawQuery; q != "" {
			to += "?" + q
		}
		http.Redirect(w, r, to, http.StatusFound)
		return
	}
	p, err := h.base(r, c.Name, "home")
	if err != nil {
		h.fail(w, err)
		return
	}
	p.Account, p.Group, p.Campaign = account, c.GroupID, c
	p.Crumbs = []crumb{{"Intel", "/intel/"}, {account, accountPath(account)}, {groupName(c.GroupID), groupPath(account, c.GroupID)}, {c.Name, ""}}
	if p.Results, err = h.st.results(ctx, id); err == nil {
		if p.Ads, err = h.st.ads(ctx, id, p.Window); err == nil {
			if p.Line, err = h.st.line(ctx, id); err == nil {
				if p.Suggestions, err = h.st.suggestions(ctx, id, false); err == nil {
					if p.Alerts, err = h.st.alerts(ctx, id, false); err == nil {
						p.Statuses, err = h.st.statusChanges(ctx, id)
					}
				}
			}
		}
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	h.render(w, "campaign", p)
}

func (h *handler) suggestionsPage(w http.ResponseWriter, r *http.Request) {
	p, err := h.base(r, "Sugestões", "suggestions")
	if err != nil {
		h.fail(w, err)
		return
	}
	p.Recent = true
	if p.Suggestions, err = h.st.suggestions(r.Context(), 0, true); err != nil {
		h.fail(w, err)
		return
	}
	h.render(w, "suggestions", p)
}

func (h *handler) alertsPage(w http.ResponseWriter, r *http.Request) {
	p, err := h.base(r, "Alertas", "alerts")
	if err != nil {
		h.fail(w, err)
		return
	}
	p.Recent = true
	if p.Alerts, err = h.st.alerts(r.Context(), 0, true); err == nil {
		p.Statuses, err = h.st.statusChanges(r.Context(), 0)
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	h.render(w, "alerts", p)
}

// notNow sets a suggestion aside ("not now"); it comes back after a day if
// its reason still holds. It then goes back to the page it was on.
func (h *handler) notNow(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := h.st.notNow(r.Context(), id, user(r)); err != nil {
		h.fail(w, err)
		return
	}
	back := r.FormValue("back")
	if u, err := url.Parse(back); err != nil || u.Host != "" || u.Scheme != "" || !strings.HasPrefix(u.Path, "/intel/") {
		back = "/intel/suggestions"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	found := []Found{}
	if q != "" {
		var err error
		if found, err = h.st.search(r.Context(), q); err != nil {
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

func groupName(g int64) string {
	if g == 0 {
		return "Sem grupo"
	}
	return fmt.Sprintf("Grupo %d", g)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func accountPath(account string) string { return "/intel/taboola/" + url.PathEscape(account) }

// groupSeg is a group in a path; a campaign with no group is g/-, as in
// Launch.
func groupSeg(group int64) string {
	if group == 0 {
		return "-"
	}
	return itoa(group)
}

// noGroup asks a list for the campaigns with no group.
const noGroup = -1

func parseGroup(s string) (int64, error) {
	if s == "-" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

func groupPath(account string, group int64) string {
	return accountPath(account) + "/g/" + groupSeg(group)
}

func campaignPath(account string, group, id int64) string {
	return groupPath(account, group) + "/c/" + itoa(id)
}

// launchPath opens the same campaign in Launch.
func launchPath(account string, group, id int64) string {
	return "/launch/taboola/" + url.PathEscape(account) + "/g/" + groupSeg(group) + "/c/" + itoa(id)
}

type cards struct {
	List []Suggestion
	Back string
}

var funcs = template.FuncMap{
	"cards":        func(l []Suggestion, back string) cards { return cards{l, back} },
	"byAccount":    byAccount,
	"deref":        func(p *int64) int64 { return *p },
	"derefT":       func(p *time.Time) time.Time { return *p },
	"accountPath":  accountPath,
	"groupPath":    groupPath,
	"campaignPath": campaignPath,
	"launchPath":   launchPath,
	"launchGroup": func(account string, group int64) string {
		if group == noGroup {
			group = 0
		}
		return "/launch/taboola/" + url.PathEscape(account) + "/g/" + groupSeg(group)
	},
	"windowName": func(w string) string { return windowNames[w] },
	"money":      money,
	"moneyp": func(v *float64) string {
		if v == nil {
			return "–"
		}
		return money(*v)
	},
	"int": thousands,
	"pct": func(v *float64) string {
		if v == nil {
			return "–"
		}
		return pct(*v)
	},
	"pctRange": func(lo, hi *float64) string {
		if lo == nil || hi == nil {
			return ""
		}
		return pct(*lo) + " a " + pct(*hi)
	},
	"moneyRange": func(lo, hi *float64) string {
		if lo == nil || hi == nil {
			return ""
		}
		if math.IsInf(*hi, 1) {
			return money(*lo) + " ou mais"
		}
		return money(*lo) + " a " + money(*hi)
	},
	"roi": func(v *float64) string {
		if v == nil {
			return "–"
		}
		return fmt.Sprintf("%+.0f%%", *v*100)
	},
	"signed": func(v float64) string {
		if v < 0 {
			return "−" + money(-v)
		}
		return money(v)
	},
	"word":      func(w string) string { return wordNames[w] },
	"status":    statusName,
	"wordClass": func(w string) string { return wordClasses[w] },
	"sureness":  func(s string) string { return surenessNames[s] },
	"kind":      func(k string) string { return kindNames[k] },
	"ago":       ago,
	"when":      func(t time.Time) string { return t.UTC().Format("02/01 15:04 UTC") },
	"source": func(s string) string {
		if s == "network" {
			return "vendas da Taboola (sem RedTrack)"
		}
		return "vendas do RedTrack"
	},
	"statusClass": func(s string) string {
		switch s {
		case "RUNNING":
			return "running"
		case "PAUSED":
			return "paused"
		case "REJECTED":
			return "rejected"
		case "PENDING_APPROVAL":
			return "review"
		}
		return ""
	},
	"trackerLogin": func(account string) string { return strings.TrimPrefix(account, "redtrack:") },
	"ids": func(ids []int64) string {
		s := make([]string, len(ids))
		for i, id := range ids {
			s[i] = itoa(id)
		}
		return strings.Join(s, ", ")
	},
}

var wordNames = map[string]string{
	"better": "melhor", "worse": "pior", "usual": "normal",
	"unclear": "ainda não dá pra saber", "too_little": "poucos dados",
}

var wordClasses = map[string]string{"better": "running", "worse": "rejected", "usual": "", "unclear": "review", "too_little": "faint"}

var surenessNames = map[string]string{"clear": "com certeza", "likely": "provavelmente"}

var kindNames = map[string]string{
	"runaway":        "Gasto sem venda",
	"tracking_gap":   "Cliques sem rastreio",
	"page_gap":       "Página não abre",
	"postback_gap":   "Vendas não chegam na Taboola",
	"item_rejected":  "Anúncio rejeitado",
	"pause-ads":      "Pausar anúncios",
	"pause-campaign": "Pausar campanha",
	"set-daily-cap":  "Baixar o teto diário",
}

func money(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	cents := int64(math.Round(v * 100))
	s := "$" + thousands(cents/100) + fmt.Sprintf(".%02d", cents%100)
	if neg {
		return "−" + s
	}
	return s
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

// statusName is a delivery status in the page's words.
func statusName(s string) string {
	if n, ok := statusNames[s]; ok {
		return n
	}
	return strings.ReplaceAll(strings.ToLower(s), "_", " ")
}

var statusNames = map[string]string{
	"":                   "nova",
	"RUNNING":            "rodando",
	"PAUSED":             "pausada",
	"PENDING_APPROVAL":   "aguardando aprovação",
	"PENDING_START_DATE": "agendada",
	"REJECTED":           "rejeitada",
	"DEPLETED":           "orçamento esgotado",
	"DEPLETED_MONTHLY":   "orçamento do mês esgotado",
	"EXPIRED":            "expirada",
	"TERMINATED":         "encerrada",
	"FROZEN":             "congelada",
	"DELETED":            "apagada",
	"GROUP_DELETED":      "grupo apagado",
}
