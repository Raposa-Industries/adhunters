package web_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/desk/internal/plan"
	"github.com/Raposa-Industries/adhunters/desk/internal/store"
	"github.com/Raposa-Industries/adhunters/desk/internal/testdb"
	"github.com/Raposa-Industries/adhunters/desk/internal/web"
	"github.com/Raposa-Industries/adhunters/shared/access"
)

type env struct {
	store *store.Store
	h     http.Handler
}

func setup(t *testing.T) env {
	t.Helper()
	pool := testdb.New(t)
	testdb.Demo(t, pool)
	s := store.New(pool)
	srv, err := web.New(s, testdb.Catalog(t), slog.New(slog.NewTextHandler(io.Discard, nil)), web.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return env{store: s, h: srv.Handler()}
}

// as signs a request in as person, the way Access's check records it.
func as(r *http.Request, person string) *http.Request {
	return r.WithContext(access.WithEmail(r.Context(), person))
}

// do sends a request as person ("" for no one), a form when form is set.
func (e env) do(t *testing.T, person, method, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, target, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if person != "" {
		r = as(r, person)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

const ana, mari = "ana@example.com", "mari@example.com"

func TestSignInFirst(t *testing.T) {
	e := setup(t)
	for _, target := range []string{"/desk/", "/desk/todos", "/desk/settings"} {
		if rec := e.do(t, "", "GET", target, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a person: %d", target, rec.Code)
		}
	}
	if rec := e.do(t, "", "POST", "/desk/c", url.Values{"text": {"oi"}}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a conversation without a person: %d", rec.Code)
	}
}

// With Access set, only its token says who is asking: Access's email header
// alone (which anything on the box could send) opens nothing. On a laptop
// the dev person is everyone.
func TestWhoIsAsking(t *testing.T) {
	pool := testdb.New(t)
	testdb.Demo(t, pool)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	checked, err := web.New(store.New(pool), testdb.Catalog(t), log,
		web.Config{Access: &access.Checker{Team: "https://acme.cloudflareaccess.com", Audience: "aud-1"}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/desk/", nil)
	r.Header.Set("Cf-Access-Authenticated-User-Email", ana)
	rec := httptest.NewRecorder()
	checked.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), ana) {
		t.Errorf("the email header without a token: %d %q", rec.Code, rec.Body.String())
	}

	dev, err := web.New(store.New(pool), testdb.Catalog(t), log, web.Config{DevPerson: "Mari@Example.com"})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	dev.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/desk/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), mari) {
		t.Errorf("the dev person: %d", rec.Code)
	}
}

func TestConversationIsItsPersons(t *testing.T) {
	e := setup(t)
	rec := e.do(t, "Ana@Example.com", "POST", "/desk/c", url.Values{"text": {"faz um brief <b>de</b> tinnitus"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/desk/c/1" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	page := e.do(t, ana, "GET", "/desk/c/1", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "faz um brief &lt;b&gt;de&lt;/b&gt; tinnitus") {
		t.Fatalf("%d: the message, escaped, is on the page", page.Code)
	}
	if !strings.Contains(page.Body.String(), "Desk está respondendo") {
		t.Error("the page does not say Desk is answering")
	}
	if rec := e.do(t, mari, "GET", "/desk/c/1", nil); rec.Code != http.StatusNotFound {
		t.Errorf("mari read ana's conversation: %d", rec.Code)
	}
	if rec := e.do(t, mari, "POST", "/desk/c/1/say", url.Values{"text": {"faz outra coisa"}}); rec.Code != http.StatusNotFound {
		t.Errorf("mari wrote in ana's conversation: %d", rec.Code)
	}
	if rec := e.do(t, mari, "POST", "/desk/c/1/stop", nil); rec.Code != http.StatusNotFound {
		t.Errorf("mari stopped ana's conversation: %d", rec.Code)
	}
	if rec := e.do(t, mari, "GET", "/desk/", nil); strings.Contains(rec.Body.String(), "tinnitus") {
		t.Error("ana's conversation is on mari's list")
	}

	v := e.do(t, ana, "GET", "/desk/c/1/v", nil).Body.String()
	if rec := e.do(t, ana, "POST", "/desk/c/1/say", url.Values{"text": {"e 6 imagens"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("say: %d", rec.Code)
	}
	if e.do(t, ana, "GET", "/desk/c/1/v", nil).Body.String() == v {
		t.Error("the page's version did not change with a new message")
	}
	if rec := e.do(t, ana, "POST", "/desk/c/1/say", url.Values{"text": {"   "}}); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty message: %d", rec.Code)
	}
}

// A form from another site is refused, whoever's browser carries it.
func TestOtherSites(t *testing.T) {
	e := setup(t)
	for _, h := range [][2]string{{"Sec-Fetch-Site", "cross-site"}, {"Origin", "https://evil.example"}} {
		r := httptest.NewRequest("POST", "/desk/switch", strings.NewReader("to=stop"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = as(r, ana)
		r.Header.Set(h[0], h[1])
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d", h[0], h[1], rec.Code)
		}
	}
	if e.store.Stopped(context.Background()) {
		t.Error("another site stopped Desk")
	}
}

func propose(t *testing.T, e env, conv int64, body string) (int64, string) {
	t.Helper()
	goal, steps, err := plan.Parse(testdb.Catalog(t), json.RawMessage(body), ana)
	if err != nil {
		t.Fatal(err)
	}
	id, fp, err := e.store.Propose(context.Background(), conv, goal, steps)
	if err != nil {
		t.Fatal(err)
	}
	return id, fp
}

func TestDecide(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	conv, _ := e.store.NewConversation(ctx, ana, "brief")
	id, fp := propose(t, e, conv, `{"goal": "Brief de Tinnitus", "steps": [{"kind": "action", "action": "demo.new_brief",
		"says": "Pedir o brief", "input": {"vertical": "Tinnitus", "images": 6}}]}`)
	page := e.do(t, ana, "GET", "/desk/c/1", nil).Body.String()
	for _, want := range []string{"Plano: Brief de Tinnitus", `value="` + fp + `"`, "OK, pode seguir", "images", "Tinnitus", "Make a brief"} {
		if !strings.Contains(page, want) {
			t.Errorf("the plan card lacks %q", want)
		}
	}
	target := "/desk/p/" + itoa(id) + "/decide"
	if rec := e.do(t, mari, "POST", target, url.Values{"answer": {"ok"}, "fp": {fp}}); rec.Code != http.StatusNotFound {
		t.Errorf("mari OK'd ana's plan: %d", rec.Code)
	}
	if rec := e.do(t, ana, "POST", target, url.Values{"answer": {"ok"}, "fp": {"other"}}); rec.Code != http.StatusConflict {
		t.Errorf("an OK for a plan she did not see: %d", rec.Code)
	}
	if rec := e.do(t, ana, "POST", target, url.Values{"answer": {"ok"}, "fp": {fp}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("ana's OK: %d %s", rec.Code, rec.Body)
	}
	p, _ := e.store.Plan(ctx, id)
	if p.State != "approved" || p.DecidedBy == nil || *p.DecidedBy != ana {
		t.Errorf("%s %v", p.State, p.DecidedBy)
	}
	if rec := e.do(t, ana, "POST", target, url.Values{"answer": {"ok"}, "fp": {fp}}); rec.Code != http.StatusConflict {
		t.Errorf("a second OK: %d", rec.Code)
	}
	if strings.Contains(e.do(t, ana, "GET", "/desk/c/1", nil).Body.String(), "OK, pode seguir") {
		t.Error("an OK'd plan still asks for its OK")
	}
}

func TestChoose(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	conv, _ := e.store.NewConversation(ctx, ana, "opções")
	id, fp := propose(t, e, conv, `{"goal": "Escolher", "steps": [{"kind": "choose", "action": "demo.options", "says": "Escolher headlines",
		"input": {"brief_id": 7}, "pick": 1}]}`)
	e.store.Decide(ctx, id, fp, ana, true)
	p, _ := e.store.Plan(ctx, id)
	st := p.Steps[0]
	res, _ := json.Marshal(store.ChoiceResult{Candidates: []store.Candidate{{ID: "1", Image: "/i/1.png", Text: "One <script>"}, {ID: "2", Text: "Two"}},
		Suggested: []string{"2"}, Why: map[string]string{"2": "mais clara"}})
	todo, err := store.AddTodo(ctx, e.store.Pool, store.Todo{Title: "Escolher: " + st.Says, Holder: ana, MadeBy: store.Desk,
		Link: "/desk/c/1#step-" + itoa(st.ID), ConversationID: &conv, StepID: &st.ID})
	if err != nil {
		t.Fatal(err)
	}
	st.State, st.Result, st.TodoID = "asked", res, &todo
	if err := store.UpdateStep(ctx, e.store.Pool, st); err != nil {
		t.Fatal(err)
	}
	e.store.Pool.Exec(ctx, `UPDATE desk.plan SET state = 'running'`)
	store.AddMessage(ctx, e.store.Pool, store.Message{ConversationID: conv, Author: store.Desk, Kind: "choice", Body: st.Says, StepID: &st.ID})

	// The choice's to-do is done by picking, not by hand.
	list := e.do(t, ana, "GET", "/desk/todos", nil).Body.String()
	if !strings.Contains(list, `href="/desk/c/1#step-`+itoa(st.ID)+`">Escolher</a>`) || strings.Contains(list, "/desk/todos/"+itoa(todo)+"/done") {
		t.Errorf("the choice's to-do offers Feito: %s", list)
	}
	if rec := e.do(t, ana, "POST", "/desk/todos/"+itoa(todo)+"/done", nil); rec.Code != http.StatusConflict {
		t.Errorf("a choice's to-do marked done by hand: %d", rec.Code)
	}

	page := e.do(t, ana, "GET", "/desk/c/1?part=timeline", nil).Body.String()
	if strings.Contains(page, "<script>") || !strings.Contains(page, "One &lt;script&gt;") {
		t.Error("an option's text is escaped")
	}
	if !strings.Contains(page, `value="2" data-keep checked`) || !strings.Contains(page, "Desk: mais clara") {
		t.Errorf("the suggestion is not ticked with its reason: %s", page)
	}
	if strings.Contains(page, "<html") {
		t.Error("the timeline part came with the whole page")
	}
	target := "/desk/s/" + itoa(st.ID) + "/choose"
	if rec := e.do(t, ana, "POST", target, url.Values{"pick": {"1", "2"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("two picks where one is allowed: %d", rec.Code)
	}
	if rec := e.do(t, ana, "POST", target, url.Values{"pick": {"7"}}); rec.Code != http.StatusConflict {
		t.Errorf("a pick never shown: %d", rec.Code)
	}
	if rec := e.do(t, mari, "POST", target, url.Values{"pick": {"1"}}); rec.Code != http.StatusNotFound {
		t.Errorf("mari picked for ana: %d", rec.Code)
	}
	if rec := e.do(t, ana, "POST", target, url.Values{"pick": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("ana's pick: %d %s", rec.Code, rec.Body)
	}
	p, _ = e.store.Plan(ctx, id)
	if p.Steps[0].State != "done" || !strings.Contains(string(p.Steps[0].Result), `"chosen": ["1"]`) {
		t.Errorf("%s %s", p.Steps[0].State, p.Steps[0].Result)
	}
	if got, _ := store.GetTodo(ctx, e.store.Pool, todo); got.DoneAt == nil || got.DoneBy == nil || *got.DoneBy != ana {
		t.Errorf("the pick did not close its to-do: %+v", got)
	}
}

func TestTodos(t *testing.T) {
	e := setup(t)
	if rec := e.do(t, ana, "POST", "/desk/todos", url.Values{"title": {"Revisar a landing"}, "holder": {"Mari@Example.com"}, "due": {"2026-10-02"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []url.Values{
		{"title": {""}},
		{"title": {"x"}, "holder": {"mari"}},
		{"title": {"x"}, "link": {"javascript:alert(1)"}},
		{"title": {"x"}, "due": {"amanhã"}},
	} {
		if rec := e.do(t, ana, "POST", "/desk/todos", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d", bad, rec.Code)
		}
	}
	mine := e.do(t, mari, "GET", "/desk/todos", nil).Body.String()
	if !strings.Contains(mine, "Revisar a landing") || !strings.Contains(mine, "02/10/2026") {
		t.Error("mari does not see her to-do")
	}
	if strings.Contains(e.do(t, ana, "GET", "/desk/todos", nil).Body.String(), "Revisar a landing") {
		t.Error("ana's list shows mari's to-do")
	}
	if !strings.Contains(e.do(t, ana, "GET", "/desk/todos?who=all", nil).Body.String(), "Revisar a landing") {
		t.Error("everyone's list lacks it")
	}
	if rec := e.do(t, mari, "POST", "/desk/todos/1/note", url.Values{"body": {"vejo amanhã"}}); rec.Code != http.StatusSeeOther {
		t.Errorf("note: %d", rec.Code)
	}
	if rec := e.do(t, mari, "POST", "/desk/todos/1/done", url.Values{"back": {"//evil.example"}}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/desk/todos" {
		t.Errorf("done: %d to %s", rec.Code, rec.Header().Get("Location"))
	}
	if strings.Contains(e.do(t, mari, "GET", "/desk/todos", nil).Body.String(), "Revisar a landing") {
		t.Error("a done to-do is still open")
	}
	if !strings.Contains(e.do(t, mari, "GET", "/desk/todos?done=1", nil).Body.String(), "vejo amanhã") {
		t.Error("the note is not on the list")
	}
}

func TestSettings(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	page := e.do(t, ana, "GET", "/desk/settings", nil).Body.String()
	for _, want := range []string{"Parar o Desk agora", "demo.new_pair", "pede", "claude-opus-5-5"} {
		if !strings.Contains(page, want) {
			t.Errorf("settings lack %q", want)
		}
	}
	if rec := e.do(t, ana, "POST", "/desk/switch", url.Values{"to": {"stop"}}); rec.Code != http.StatusSeeOther || !e.store.Stopped(ctx) {
		t.Fatalf("stop: %d", rec.Code)
	}
	if !strings.Contains(e.do(t, mari, "GET", "/desk/", nil).Body.String(), "Desk está parado") {
		t.Error("the pages do not say Desk is stopped")
	}
	if rec := e.do(t, mari, "POST", "/desk/switch", url.Values{"to": {"start"}}); rec.Code != http.StatusSeeOther || e.store.Stopped(ctx) {
		t.Fatalf("start: %d", rec.Code)
	}
	if rec := e.do(t, ana, "POST", "/desk/settings", url.Values{"effort": {"insane"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad effort: %d", rec.Code)
	}
	if rec := e.do(t, ana, "POST", "/desk/settings", url.Values{"daily_usd": {"5000"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("a budget past the page's limit: %d", rec.Code)
	}
	if rec := e.do(t, ana, "POST", "/desk/settings", url.Values{"daily_usd": {"35"}, "effort": {"high"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d", rec.Code)
	}
	if e.store.SettingFloat(ctx, "daily_usd", 0) != 35 || e.store.Setting(ctx, "effort", "") != "high" {
		t.Error("the settings were not saved")
	}
}

func TestFiles(t *testing.T) {
	e := setup(t)
	for target, want := range map[string]string{
		"/desk/static/desk.js":   "text/javascript; charset=utf-8",
		"/desk/static/desk.css":  "text/css; charset=utf-8",
		"/desk/_frame/frame.js":  "text/javascript; charset=utf-8",
		"/desk/_frame/frame.css": "text/css; charset=utf-8",
		"/desk/_frame/core.js":   "text/javascript; charset=utf-8",
	} {
		rec := e.do(t, "", "GET", target, nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != want {
			t.Errorf("%s: %d %s", target, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if rec := e.do(t, "", "GET", "/desk/static/../web.go", nil); rec.Code == 200 {
		t.Error("a file outside static was served")
	}
	rec := e.do(t, ana, "GET", "/desk/", nil)
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("csp %q", csp)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
