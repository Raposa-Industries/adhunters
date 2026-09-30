// Package web is desk-web: the pages people talk to Desk on, in the Frame.
// A conversation shows what was said, the plans Desk proposes with their
// OK and No, the choices it asks for, and each step as it runs; the to-do
// list and the settings (with the stop switch) have their own pages.
//
// Who is asking comes from Cloudflare Access (its authenticated email
// header); desk-web listens on localhost behind the tunnel, so nothing else
// can set it. Each conversation is its person's alone.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Raposa-Industries/adhunters/contract/actions"
	"github.com/Raposa-Industries/adhunters/desk/internal/store"
	"github.com/Raposa-Industries/adhunters/shared/frame"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

// Server serves the pages.
type Server struct {
	store     *store.Store
	catalog   *actions.Catalog
	log       *slog.Logger
	tmpl      *template.Template
	devPerson string
}

// New builds the pages. devPerson, when set, is who is asking when no
// Cloudflare Access header came: for running desk-web on a laptop only.
func New(s *store.Store, c *actions.Catalog, log *slog.Logger, devPerson string) (*Server, error) {
	t, err := template.New("").Funcs(template.FuncMap{
		"when":    when,
		"day":     day,
		"linkify": linkify,
		"usd":     func(f float64) string { return fmt.Sprintf("US$ %.2f", f) },
		"stateword": func(s string) string {
			if w, ok := stateWords[s]; ok {
				return w
			}
			return s
		},
	}).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{store: s, catalog: c, log: log, tmpl: t, devPerson: strings.ToLower(strings.TrimSpace(devPerson))}, nil
}

// stateWords are the plan and step states as the page says them.
var stateWords = map[string]string{
	"proposed": "esperando OK", "approved": "OK dado", "refused": "recusado", "replaced": "substituído",
	"running": "rodando", "done": "feito", "failed": "falhou", "stopped": "parado",
	"waiting": "na fila", "asked": "esperando", "skipped": "não rodou",
}

// Handler routes the pages, all under /desk/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /desk/_frame/", http.StripPrefix("/desk/_frame", frame.Handler()))
	mux.Handle("GET /desk/static/", http.StripPrefix("/desk/static", static()))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/desk/", http.StatusSeeOther) })
	mux.HandleFunc("GET /desk/{$}", s.home)
	mux.HandleFunc("POST /desk/c", s.newConversation)
	mux.HandleFunc("GET /desk/c/{id}", s.conversation)
	mux.HandleFunc("GET /desk/c/{id}/v", s.version)
	mux.HandleFunc("POST /desk/c/{id}/say", s.say)
	mux.HandleFunc("POST /desk/c/{id}/stop", s.stop)
	mux.HandleFunc("POST /desk/p/{id}/decide", s.decide)
	mux.HandleFunc("POST /desk/s/{id}/choose", s.choose)
	mux.HandleFunc("GET /desk/todos", s.todos)
	mux.HandleFunc("POST /desk/todos", s.addTodo)
	mux.HandleFunc("POST /desk/todos/{id}/done", s.doneTodo)
	mux.HandleFunc("POST /desk/todos/{id}/note", s.addNote)
	mux.HandleFunc("GET /desk/settings", s.settings)
	mux.HandleFunc("POST /desk/settings", s.saveSettings)
	mux.HandleFunc("POST /desk/switch", s.stopSwitch)
	return secure(mux)
}

// csp: the page's own files; pictures of options may come from the apps
// or, for competitors' creatives, from the networks' image hosts.
const csp = "default-src 'self'; img-src 'self' https: data:; style-src 'self' 'unsafe-inline'; script-src 'self'; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// secure sets the headers every answer carries and refuses a form posted
// from another site: a browser signed in to Access would carry such a post.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			site := r.Header.Get("Sec-Fetch-Site")
			origin := r.Header.Get("Origin")
			if (site != "" && site != "same-origin" && site != "none") || (origin != "" && !sameHost(origin, r.Host)) {
				http.Error(w, "um formulário de outro site", http.StatusForbidden)
				return
			}
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

func sameHost(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Host == host
}

var types = map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8"}

func static() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err) // the embed pattern guarantees the folder
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		t, ok := types[path.Ext(name)]
		if st, err := fs.Stat(sub, name); !ok || err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", t)
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// who is the person asking: Cloudflare Access's email, or the dev person.
// Without either the page says to sign in, and nothing else happens.
func (s *Server) who(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := strings.ToLower(strings.TrimSpace(r.Header.Get("Cf-Access-Authenticated-User-Email")))
	if p == "" {
		p = s.devPerson
	}
	if p == "" {
		http.Error(w, "Entre pelo Cloudflare Access para usar o Desk.", http.StatusUnauthorized)
		return "", false
	}
	return p, true
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("render", "page", name, "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		http.NotFound(w, r)
	case errors.Is(err, store.ErrNotAllowed):
		http.Error(w, "Isso não é seu para fazer.", http.StatusForbidden)
	case errors.Is(err, store.ErrStale):
		http.Error(w, "Isso mudou desde que a página abriu. Volte e recarregue.", http.StatusConflict)
	case errors.Is(err, store.ErrChoice):
		http.Error(w, "Este fica feito quando a escolha é feita na conversa.", http.StatusConflict)
	default:
		s.log.Error("page failed", "path", r.URL.Path, "err", err)
		http.Error(w, "Algo deu errado: "+err.Error(), http.StatusInternalServerError)
	}
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// page is what every page's frame needs.
type page struct {
	Title, Tab, Person string
	Stopped            bool
	Spent, Cap         float64
}

func (s *Server) page(ctx context.Context, title, tab, person string) page {
	spent, _ := s.store.SpentToday(ctx)
	return page{Title: title, Tab: tab, Person: person, Stopped: s.store.Stopped(ctx),
		Spent: spent, Cap: s.store.SettingFloat(ctx, "daily_usd", 20)}
}

// OverBudget says whether today's spend reached the cap.
func (p page) OverBudget() bool { return p.Spent >= p.Cap }

// maxText is the most characters a person's message holds.
const maxText = 8000

func formText(r *http.Request, name string) (string, bool) {
	t := strings.TrimSpace(r.FormValue(name))
	return t, t != "" && utf8.RuneCountInString(t) <= maxText
}

// ---- conversations ---------------------------------------------------------

type homePage struct {
	page
	Conversations []store.Conversation
	OpenTodos     int
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	convs, err := s.store.Conversations(ctx, person, 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	todos, err := s.store.Todos(ctx, store.TodoFilter{Holder: person, Open: true})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "home.html", homePage{page: s.page(ctx, "Conversas", "talk", person), Conversations: convs, OpenTodos: len(todos)})
}

func (s *Server) newConversation(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	text, ok := formText(r, "text")
	if !ok {
		http.Error(w, fmt.Sprintf("Escreva o pedido (até %d caracteres).", maxText), http.StatusBadRequest)
		return
	}
	id, err := s.store.NewConversation(r.Context(), person, text)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/desk/c/%d", id), http.StatusSeeOther)
}

// mine returns the conversation if it is person's; anyone else gets a 404,
// the same as for one that does not exist.
func (s *Server) mine(ctx context.Context, r *http.Request, person string) (store.Conversation, error) {
	id, ok := pathID(r)
	if !ok {
		return store.Conversation{}, pgx.ErrNoRows
	}
	c, err := s.store.Conversation(ctx, id)
	if err == nil && c.Person != person {
		err = pgx.ErrNoRows
	}
	return c, err
}

type convPage struct {
	page
	Conv  store.Conversation
	Items []item
	V     string
}

// item is one line of the conversation: a message, and the plan or the
// choice it stands for.
type item struct {
	Msg    store.Message
	Plan   *planView
	Choice *choiceView
}

type planView struct {
	store.Plan
	Steps     []stepView
	CanDecide bool
}

type stepView struct {
	store.Step
	App, Does string
	Inputs    []kv
	Uses      []string
}

type kv struct{ K, V string }

type choiceView struct {
	Step      store.Step
	Result    store.ChoiceResult
	Suggested map[string]bool
	Chosen    map[string]bool
	Open      bool
}

func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	c, err := s.mine(ctx, r, person)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data, err := s.convData(ctx, c, person)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if r.URL.Query().Get("part") == "timeline" {
		s.render(w, "timeline", data)
		return
	}
	s.render(w, "conversation.html", data)
}

func (s *Server) convData(ctx context.Context, c store.Conversation, person string) (convPage, error) {
	data := convPage{page: s.page(ctx, c.Title, "talk", person), Conv: c}
	msgs, err := s.store.Messages(ctx, c.ID, 0)
	if err != nil {
		return data, err
	}
	plans, err := s.store.Plans(ctx, c.ID)
	if err != nil {
		return data, err
	}
	byID := map[int64]store.Plan{}
	steps := map[int64]store.Step{}
	for _, p := range plans {
		byID[p.ID] = p
		for _, st := range p.Steps {
			steps[st.ID] = st
		}
	}
	for _, m := range msgs {
		it := item{Msg: m}
		switch {
		case m.Kind == "plan" && m.PlanID != nil:
			if p, ok := byID[*m.PlanID]; ok {
				it.Plan = s.planView(p, person == c.Person)
			}
		case m.Kind == "choice" && m.StepID != nil:
			if st, ok := steps[*m.StepID]; ok {
				it.Choice = choiceOf(st, byID[st.PlanID])
			}
		}
		data.Items = append(data.Items, it)
	}
	data.V, err = s.store.Version(ctx, c.ID)
	return data, err
}

func (s *Server) planView(p store.Plan, owner bool) *planView {
	v := &planView{Plan: p, CanDecide: owner && p.State == "proposed"}
	for _, st := range p.Steps {
		sv := stepView{Step: st}
		if st.Action != "" {
			if a, ok := s.catalog.Version(st.Action, int(st.ActionVersion)); ok {
				sv.App, sv.Does = a.App(), a.Says
			}
			var in map[string]any
			_ = json.Unmarshal(st.Input, &in)
			sv.Inputs = flatten("", in)
		}
		for _, u := range st.Uses {
			sv.Uses = append(sv.Uses, fmt.Sprintf("%s ← passo %d", u.Into, u.Step))
		}
		if st.Kind == "person" {
			sv.Inputs = append(sv.Inputs, kv{"quem", st.Holder})
			if st.Due != nil {
				sv.Inputs = append(sv.Inputs, kv{"até", st.Due.Format("02/01/2006")})
			}
		}
		v.Steps = append(v.Steps, sv)
	}
	return v
}

// flatten writes an input as lines a person reads: "input.account: acme".
func flatten(prefix string, m map[string]any) []kv {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []kv
	for _, k := range keys {
		name := k
		if prefix != "" {
			name = prefix + "." + k
		}
		if sub, ok := m[k].(map[string]any); ok {
			out = append(out, flatten(name, sub)...)
			continue
		}
		b, _ := json.Marshal(m[k])
		out = append(out, kv{name, strings.Trim(string(b), `"`)})
	}
	return out
}

func choiceOf(st store.Step, p store.Plan) *choiceView {
	v := &choiceView{Step: st, Suggested: map[string]bool{}, Chosen: map[string]bool{}}
	_ = json.Unmarshal(st.Result, &v.Result)
	for _, id := range v.Result.Suggested {
		v.Suggested[id] = true
	}
	for _, id := range v.Result.Chosen {
		v.Chosen[id] = true
	}
	v.Open = st.State == "asked" && p.State == "running"
	return v
}

// version is what the page polls: it changes whenever the conversation
// does, and the page then redraws its timeline.
func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	c, err := s.mine(r.Context(), r, person)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := s.store.Version(r.Context(), c.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"v": v})
}

func (s *Server) say(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	text, ok := formText(r, "text")
	if !ok {
		http.Error(w, fmt.Sprintf("Escreva a mensagem (até %d caracteres).", maxText), http.StatusBadRequest)
		return
	}
	if err := s.store.Say(r.Context(), id, person, text); err != nil {
		s.fail(w, r, notYours(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/desk/c/%d#end", id), http.StatusSeeOther)
}

// notYours answers another person's conversation as one that is not there.
func notYours(err error) error {
	if errors.Is(err, store.ErrNotAllowed) {
		return pgx.ErrNoRows
	}
	return err
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.store.Stop(r.Context(), id, person); err != nil {
		s.fail(w, r, notYours(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/desk/c/%d#end", id), http.StatusSeeOther)
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	p, err := s.store.Plan(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var yes bool
	switch r.FormValue("answer") {
	case "ok":
		yes = true
	case "no":
	default:
		http.Error(w, "OK ou não?", http.StatusBadRequest)
		return
	}
	if err := s.store.Decide(ctx, id, r.FormValue("fp"), person, yes); err != nil {
		s.fail(w, r, notYours(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/desk/c/%d#plan-%d", p.ConversationID, id), http.StatusSeeOther)
}

func (s *Server) choose(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	ids := r.PostForm["pick"]
	if len(ids) == 0 {
		http.Error(w, "Marque pelo menos uma opção.", http.StatusBadRequest)
		return
	}
	convID, pick, err := s.store.StepOf(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if pick > 0 && len(ids) > pick {
		http.Error(w, fmt.Sprintf("Escolha no máximo %d.", pick), http.StatusBadRequest)
		return
	}
	if err := s.store.Choose(ctx, id, person, ids); err != nil {
		s.fail(w, r, notYours(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/desk/c/%d#step-%d", convID, id), http.StatusSeeOther)
}

// ---- to-dos --------------------------------------------------------------------

type todosPage struct {
	page
	Todos    []store.Todo
	Notes    map[int64][]store.Note
	Everyone bool
	Done     bool
	Today    string
}

func (s *Server) todos(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	data := todosPage{page: s.page(ctx, "A fazer", "todos", person), Everyone: q.Get("who") == "all", Done: q.Get("done") == "1",
		Today: time.Now().UTC().Format("2006-01-02"), Notes: map[int64][]store.Note{}}
	f := store.TodoFilter{Holder: person, Open: !data.Done}
	if data.Everyone {
		f.Holder = ""
	}
	list, err := s.store.Todos(ctx, f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Todos = list
	ids := make([]int64, len(list))
	for i, t := range list {
		ids[i] = t.ID
	}
	notes, err := s.store.Notes(ctx, ids)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, n := range notes {
		data.Notes[n.TodoID] = append(data.Notes[n.TodoID], n)
	}
	s.render(w, "todos.html", data)
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func (s *Server) addTodo(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	title, ok := formText(r, "title")
	if !ok || utf8.RuneCountInString(title) > 300 {
		http.Error(w, "O que é para fazer? (uma linha)", http.StatusBadRequest)
		return
	}
	t := store.Todo{Title: title, Holder: strings.TrimSpace(r.FormValue("holder")), MadeBy: person, Link: strings.TrimSpace(r.FormValue("link"))}
	if t.Holder == "" {
		t.Holder = person
	}
	if !emailRe.MatchString(t.Holder) {
		http.Error(w, "Quem faz: o email da pessoa.", http.StatusBadRequest)
		return
	}
	if t.Link != "" && !strings.HasPrefix(t.Link, "/") && !strings.HasPrefix(t.Link, "https://") {
		http.Error(w, "O link é uma página dos apps (/launch/…) ou um endereço https.", http.StatusBadRequest)
		return
	}
	if due := r.FormValue("due"); due != "" {
		d, err := time.Parse("2006-01-02", due)
		if err != nil {
			http.Error(w, "Data no formato 2026-10-01.", http.StatusBadRequest)
			return
		}
		t.Due = &d
	}
	if _, err := s.store.AddTodo(r.Context(), t); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/desk/todos", http.StatusSeeOther)
}

func (s *Server) doneTodo(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := store.GetTodo(r.Context(), s.store.Pool, id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.DoneTodo(r.Context(), id, person); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, back(r, "/desk/todos"), http.StatusSeeOther)
}

func (s *Server) addNote(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, ok := formText(r, "body")
	if !ok {
		http.Error(w, "Escreva o comentário.", http.StatusBadRequest)
		return
	}
	if _, err := store.GetTodo(r.Context(), s.store.Pool, id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.AddNote(r.Context(), id, person, body); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, back(r, "/desk/todos")+fmt.Sprintf("#todo-%d", id), http.StatusSeeOther)
}

// back is the page a form came from, when it is one of Desk's, else def.
func back(r *http.Request, def string) string {
	if b := r.FormValue("back"); strings.HasPrefix(b, "/desk/") && !strings.ContainsAny(b, "\\\r\n") && !strings.HasPrefix(b, "//") {
		return b
	}
	return def
}

// ---- settings ------------------------------------------------------------------

type settingsPage struct {
	page
	Settings []store.SettingRow
	Apps     []appActions
	Saved    bool
}

type appActions struct {
	App     string
	Actions []actions.Action
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	list, err := s.store.Settings(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data := settingsPage{page: s.page(ctx, "Ajustes", "settings", person), Settings: list, Saved: r.URL.Query().Get("saved") == "1"}
	for _, a := range s.catalog.Latest() {
		if n := len(data.Apps); n == 0 || data.Apps[n-1].App != a.App() {
			data.Apps = append(data.Apps, appActions{App: a.App()})
		}
		data.Apps[len(data.Apps)-1].Actions = append(data.Apps[len(data.Apps)-1].Actions, a)
	}
	s.render(w, "settings.html", data)
}

// editable are the settings the page changes, and what each takes.
var editable = map[string]func(string) bool{
	"model": func(v string) bool { return regexp.MustCompile(`^claude-[a-z0-9-]+$`).MatchString(v) },
	"effort": func(v string) bool {
		return v == "low" || v == "medium" || v == "high" || v == "xhigh" || v == "max"
	},
	"daily_usd": func(v string) bool {
		f, err := strconv.ParseFloat(v, 64)
		return err == nil && f >= 0 && f <= 500
	},
	"turn_calls": func(v string) bool {
		n, err := strconv.Atoi(v)
		return err == nil && n >= 1 && n <= 50
	},
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	changes := map[string]string{}
	for key, valid := range editable {
		v := strings.TrimSpace(r.PostFormValue(key))
		if v == "" {
			continue
		}
		if !valid(v) {
			http.Error(w, fmt.Sprintf("%s não aceita %q.", key, v), http.StatusBadRequest)
			return
		}
		changes[key] = v
	}
	if err := s.store.SetSettings(r.Context(), changes, person); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/desk/settings?saved=1", http.StatusSeeOther)
}

// stopSwitch turns Desk off or on for everyone: anyone on the team may
// stop it, and start it again.
func (s *Server) stopSwitch(w http.ResponseWriter, r *http.Request) {
	person, ok := s.who(w, r)
	if !ok {
		return
	}
	var on bool
	switch r.FormValue("to") {
	case "stop":
		on = true
	case "start":
	default:
		http.Error(w, "parar ou ligar?", http.StatusBadRequest)
		return
	}
	if err := s.store.SetStopped(r.Context(), on, person); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, back(r, "/desk/settings"), http.StatusSeeOther)
}

// ---- helpers ---------------------------------------------------------------------

func when(t any) string {
	var v time.Time
	switch x := t.(type) {
	case time.Time:
		v = x
	case *time.Time:
		if x == nil {
			return ""
		}
		v = *x
	default:
		return ""
	}
	return v.UTC().Format("02/01 15:04") + " UTC"
}

func day(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("02/01/2006")
}

var linkRe = regexp.MustCompile(`https://[^\s<>"']+|(?:^|[\s(])(/[a-z]+/[^\s<>"'()]*)`)

// linkify writes text as HTML with its https addresses and app paths
// (/launch/requests/12) as links; everything else is escaped.
func linkify(text string) template.HTML {
	var b strings.Builder
	last := 0
	for _, m := range linkRe.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[0], m[1]
		if m[2] >= 0 { // a path: the group, without the space before it
			start, end = m[2], m[3]
		}
		href := strings.TrimRight(text[start:end], ".,;:!?")
		end = start + len(href)
		b.WriteString(template.HTMLEscapeString(text[last:start]))
		fmt.Fprintf(&b, `<a href="%s" rel="noreferrer">%s</a>`, template.HTMLEscapeString(href), template.HTMLEscapeString(href))
		last = end
	}
	b.WriteString(template.HTMLEscapeString(text[last:]))
	return template.HTML(b.String())
}
