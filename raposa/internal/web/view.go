package web

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

// What the pages show beside the rows they read: the numbers along the top
// of each list, and the Portuguese the screens use for the values the
// database keeps in English.

// ListPage is the investigations list.
type ListPage struct {
	Rows   []Row
	All    bool
	Auto   int
	Counts Counts
}

// Counts are the numbers above the investigations list, over the rows it
// shows.
type Counts struct {
	Total, Cloaked, Clean, Running int
}

func countRows(rows []Row) Counts {
	c := Counts{Total: len(rows)}
	for _, r := range rows {
		switch {
		case r.Cloaked:
			c.Cloaked++
		case r.Status == "completed":
			c.Clean++
		}
		if r.Status == "waiting" || r.Status == "running" {
			c.Running++
		}
	}
	return c
}

// BurnsPage is the burned lines page.
type BurnsPage struct {
	Burns   []Burn
	Sites   int
	General int
	Checked *time.Time
	// Rungs are the rungs the burned lines are on, for the filter.
	Rungs []int16
}

func burnsPage(list []Burn) BurnsPage {
	p := BurnsPage{Burns: list}
	sites := map[string]bool{}
	rungs := map[int16]bool{}
	for i := range list {
		b := &list[i]
		sites[b.Scope] = true
		rungs[b.Rung] = true
		if b.General {
			p.General++
		}
		if p.Checked == nil || b.Checked.After(*p.Checked) {
			p.Checked = &b.Checked
		}
	}
	p.Sites = len(sites)
	for r := range rungs {
		p.Rungs = append(p.Rungs, r)
	}
	sort.Slice(p.Rungs, func(i, j int) bool { return p.Rungs[i] < p.Rungs[j] })
	return p
}

// CloakedPage is the cloaked-ads page.
type CloakedPage struct {
	Rows      []Cloaked
	Days      int
	Creatives int
	Funnels   int
	WithVideo int
}

func cloakedPage(rows []Cloaked, days int) CloakedPage {
	p := CloakedPage{Rows: rows, Days: days}
	creatives := map[int32]bool{}
	for _, r := range rows {
		creatives[r.CreativeID] = true
		p.Funnels += int(r.Variants)
		if len(r.Videos) > 0 {
			p.WithVideo++
		}
	}
	p.Creatives = len(creatives)
	return p
}

// words are the template functions that put the stored values in
// Portuguese. A value they do not know is shown as it is.
var words = template.FuncMap{
	"modeWord":    pick(map[string]string{"deep": "profunda", "quick": "rápida"}),
	"statusWord":  pick(map[string]string{"waiting": "esperando", "running": "rodando", "completed": "concluída", "failed": "falhou", "stopped": "parada"}),
	"stageWord":   pick(map[string]string{"queued": "na fila", "preparing": "preparando", "baseline": "lendo a página branca", "climbing": "subindo a escada", "sampling": "amostrando", "done": "terminada"}),
	"originWord":  pick(map[string]string{"user": "", "auto": "automática", "retry": "repetição"}),
	"purposeWord": pick(map[string]string{"ladder": "escada", "sample": "amostra"}),
	"linkWord":    pick(map[string]string{"live": "ao vivo", "saved": "salvo"}),
	"outcomeWord": pick(map[string]string{"white": "branca", "dark": "escura", "error": "erro"}),
	"eventWord":   pick(map[string]string{"started": "início", "dark_found": "página escura encontrada", "finished": "fim"}),
	"upper":       func(s string) string { return strings.ToUpper(s) },
	// head is what the top of every page needs: its title, its tab in the
	// Frame, what ⌘K searches, and who is looking.
	"head": func(title, tab, search, user string) map[string]string {
		return map[string]string{"Title": title, "Tab": tab, "Search": search, "User": user}
	},
	"title":     func(s string) string { return capital(s) },
	"short":     whenShort,
	"site":      func(scope string) string { return strings.TrimPrefix(scope, "site:") },
	"shortLink": func(u string) string { return shortLink(u, false) },
	"shortSite": func(u string) string { return shortLink(u, true) },
	"plural": func(n any, one, many string) string {
		if toInt(n) == 1 {
			return one
		}
		return many
	},
	"pct": func(v float64) template.CSS {
		if v < 0 {
			v = 0
		}
		if v > 100 {
			v = 100
		}
		return template.CSS(fmt.Sprintf("width:%.1f%%", v))
	},
}

func pick(m map[string]string) func(string) string {
	return func(v string) string {
		if w, ok := m[v]; ok {
			return w
		}
		return v
	}
}

// capital makes the first letter upper case: "profunda" → "Profunda".
func capital(s string) string {
	for i, r := range s {
		return strings.ToUpper(string(r)) + s[i+len(string(r)):]
	}
	return s
}

// whenShort is a moment without its seconds, for narrow places: 02/10 04:17.
func whenShort(t any) string { return stamp(t, "02/01 15:04") }

// shortLink writes a long address as its host and its last part, the way
// the screens list video players: scripts.converteai.net/…/player.js. With
// site, the host loses its first label when it has more than two
// (converteai.net/…/player.js).
func shortLink(raw string, site bool) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := u.Hostname()
	if site {
		if parts := strings.Split(host, "."); len(parts) > 2 {
			host = strings.Join(parts[1:], ".")
		}
	}
	p := strings.Trim(u.Path, "/")
	switch {
	case p == "":
		return host
	case !strings.Contains(p, "/"):
		return host + "/" + p
	}
	return host + "/…/" + path.Base(p)
}

func toInt(n any) int {
	switch v := n.(type) {
	case int:
		return v
	case int16:
		return int(v)
	case int32:
		return int(v)
	case int64:
		return int(v)
	}
	return 0
}

// assets serves raposa.css and raposa.js with their types set by hand, as
// the Frame's are.
func assets() http.Handler {
	sub, err := fs.Sub(assetFiles, "assets")
	if err != nil {
		panic(err) // the embed pattern guarantees the folder
	}
	files := http.FileServerFS(sub)
	types := map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
