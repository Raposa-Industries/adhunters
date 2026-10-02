package site_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/create/internal/library"
	"github.com/Raposa-Industries/adhunters/create/internal/openai"
	"github.com/Raposa-Industries/adhunters/create/internal/sessions"
	"github.com/Raposa-Industries/adhunters/create/internal/site"
	"github.com/Raposa-Industries/adhunters/create/internal/spyad"
	"github.com/Raposa-Industries/adhunters/create/internal/testdb"
	"github.com/Raposa-Industries/adhunters/shared/files"
)

type ai struct{}

func (ai) Why() string { return "" }
func (ai) Plan(_ context.Context, r openai.PlanRequest) (openai.Plan, error) {
	p := openai.Plan{Headlines: []string{"This SHOCKING Memory Trick", "A Calm Morning Habit After 60"}}
	for i := 0; i < r.Images; i++ {
		p.Briefs = append(p.Briefs, openai.Brief{Angle: "Colher", Brief: "idea"})
	}
	return p, nil
}
func (ai) Image(context.Context, openai.ImageRequest) (openai.Image, error) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 32, 18)))
	return openai.Image{Data: b.Bytes(), MIME: "image/png", Width: 32, Height: 18}, nil
}

type lib struct {
	sets    int
	renamed []string
	refuse  bool
}

func (l *lib) RenameSet(_ context.Context, id int64, name string) error {
	if l.refuse {
		return &library.Error{Status: 400, Message: "taken"}
	}
	l.renamed = append(l.renamed, itoa(id)+" "+name)
	return nil
}

type spy struct{ pics int }

func (s *spy) Ad(_ context.Context, id int64) (spyad.Ad, error) {
	if id != 77 {
		return spyad.Ad{}, spyad.ErrNotFound
	}
	return spyad.Ad{CreativeID: 77, ImageURL: "https://cdn.example/77.png", Headline: "Doctors Hate This Trick", Brand: "Acme/Health", VerticalID: "tinnitus"}, nil
}

func (s *spy) Picture(context.Context, string) ([]byte, error) {
	s.pics++
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 40, 30)))
	return b.Bytes(), nil
}

func (l *lib) File(context.Context, string) ([]byte, error) { return nil, library.ErrNotFound }
func (l *lib) AddSet(context.Context, library.NewSet) (library.Set, error) {
	l.sets++
	return library.Set{ID: 9}, nil
}
func (l *lib) AddCreative(context.Context, library.CreativeMeta, string, []byte) (library.Creative, error) {
	return library.Creative{ID: 1}, nil
}
func (l *lib) AddHeadlines(context.Context, []library.NewHeadline) error { return nil }
func (l *lib) Headlines(context.Context, string, int) ([]string, error)  { return nil, nil }

type on struct{}

func (on) OpenAIWhy() string { return "" }
func (on) HeadlineModels() []site.HeadlineModel {
	return []site.HeadlineModel{{ID: "grok", Name: "Grok"}}
}

func call(t *testing.T, h http.Handler, method, path, ct string, body io.Reader, out any) int {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Cf-Access-Authenticated-User-Email", "mari@example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if out != nil {
		_ = json.Unmarshal(rec.Body.Bytes(), out)
	}
	return rec.Code
}

func TestSite(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := sessions.New(testdb.New(t), &files.Dir{Root: t.TempDir()}, func() {})
	l := &lib{}
	sp := &spy{}
	w := sessions.NewWorker(st, ai{}, l, log)
	browse := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { _, _ = rw.Write([]byte("lib " + r.URL.Path)) })
	web, err := site.New(st, l, sp, browse, on{}, log, "test")
	if err != nil {
		t.Fatal(err)
	}
	h := web.Handler()
	drain := func() {
		for {
			ran, err := w.RunOne(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !ran {
				return
			}
		}
	}

	for _, p := range []string{"/create/", "/create/s/3", "/create/library", "/create/rules", "/create/static/app.js", "/create/_frame/frame.js"} {
		if code := call(t, h, "GET", p, "", nil, nil); code != 200 {
			t.Errorf("%s: %d", p, code)
		}
	}
	for _, p := range []string{"/create/new", "/create/briefs/3"} {
		if code := call(t, h, "GET", p, "", nil, nil); code != http.StatusFound {
			t.Errorf("%s should land on the chat: %d", p, code)
		}
	}
	var vs struct {
		Categories []struct {
			Verticals []struct{ ID, Name string }
		}
	}
	if code := call(t, h, "GET", "/create/api/verticals", "", nil, &vs); code != 200 || len(vs.Categories) < 10 {
		t.Fatalf("verticals: %d %d", code, len(vs.Categories))
	}

	var sess sessions.Session
	if code := call(t, h, "POST", "/create/api/sessions", "application/json", strings.NewReader(`{"name":"X","vertical_id":"made-up"}`), &sess); code != 400 {
		t.Errorf("a vertical not on the list: %d", code)
	}
	if code := call(t, h, "POST", "/create/api/sessions", "application/json", strings.NewReader(`{"name":"Colher","vertical_id":"memory-loss"}`), &sess); code != 201 ||
		sess.VerticalName != "Memory Loss" || sess.MadeBy != "mari@example.com" {
		t.Fatalf("new session: %d %+v", code, sess)
	}
	id := itoa(sess.ID)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "ad.png")
	_ = png.Encode(fw, image.NewRGBA(image.Rect(0, 0, 60, 40)))
	_ = mw.Close()
	var up sessions.Item
	if code := call(t, h, "POST", "/create/api/sessions/"+id+"/items", mw.FormDataContentType(), &body, &up); code != 201 || up.ImageURL == "" || up.Origin != "upload" {
		t.Fatalf("upload: %d %+v", code, up)
	}
	if code := call(t, h, "GET", up.ImageURL, "", nil, nil); code != 200 {
		t.Errorf("item file: %d", code)
	}
	var typed sessions.Item
	if code := call(t, h, "POST", "/create/api/sessions/"+id+"/items", "application/json", strings.NewReader(`{"headline":"This SHOCKING Memory Trick"}`), &typed); code != 201 ||
		len(typed.Warnings) < 2 {
		t.Fatalf("typed headline with its warnings: %d %+v", code, typed)
	}
	if code := call(t, h, "POST", "/create/api/sessions/"+id+"/items", "application/json", strings.NewReader(`{"library_creative":5}`), nil); code != 400 {
		t.Errorf("a creative the library does not have: %d", code)
	}

	b, _ := json.Marshal(sessions.Send{Prompt: "make it brighter", Picked: []int64{up.ID, typed.ID}, Images: 1, Headlines: 2})
	var turn sessions.Turn
	if code := call(t, h, "POST", "/create/api/sessions/"+id+"/turns", "application/json", bytes.NewReader(b), &turn); code != 202 || turn.State != "making" {
		t.Fatalf("send: %d %+v", code, turn)
	}
	drain()
	var d sessions.Detail
	call(t, h, "GET", "/create/api/sessions/"+id, "", nil, &d)
	if len(d.Turns) != 1 || d.Turns[0].State != "done" || len(d.Items) != 5 {
		t.Fatalf("after the turn: %+v", d)
	}
	var ids []int64
	for _, it := range d.Items {
		ids = append(ids, it.ID)
	}
	b, _ = json.Marshal(map[string]any{"item_ids": ids, "ai_label": "ai"})
	var sv sessions.Save
	if code := call(t, h, "POST", "/create/api/sessions/"+id+"/saves", "application/json", bytes.NewReader(b), &sv); code != 202 {
		t.Fatalf("save: %d", code)
	}
	drain()
	call(t, h, "GET", "/create/api/saves/"+itoa(sv.ID), "", nil, &sv)
	if sv.State != "done" || sv.LibrarySetID == nil || *sv.LibrarySetID != 9 {
		t.Fatalf("saved: %+v", sv)
	}
	var list struct{ Sessions []sessions.Session }
	if code := call(t, h, "GET", "/create/api/sessions?vertical=memory-loss", "", nil, &list); code != 200 || len(list.Sessions) != 1 || list.Sessions[0].Images != 2 {
		t.Fatalf("sessions: %d %+v", code, list)
	}

	// Renaming a saved session renames its library set; a name the library
	// refuses puts the session's name back.
	var renamed sessions.Session
	if code := call(t, h, "PATCH", "/create/api/sessions/"+id, "application/json", strings.NewReader(`{"name":"Colher de sopa"}`), &renamed); code != 200 ||
		renamed.Name != "Colher de sopa" || len(l.renamed) != 1 || l.renamed[0] != "9 Colher de sopa" {
		t.Fatalf("rename: %d %+v %v", code, renamed, l.renamed)
	}
	l.refuse = true
	if code := call(t, h, "PATCH", "/create/api/sessions/"+id, "application/json", strings.NewReader(`{"name":"Taken"}`), nil); code != 400 {
		t.Errorf("a name the library refuses: %d", code)
	}
	call(t, h, "GET", "/create/api/sessions/"+id, "", nil, &d)
	if d.Session.Name != "Colher de sopa" {
		t.Errorf("name after a refused rename: %q", d.Session.Name)
	}

	// A Spy ad opens a session in its vertical with its picture and headline
	// picked; opening it again adds nothing.
	var ad struct {
		Ad           spyad.Ad
		VerticalName string `json:"vertical_name"`
		Name         string
	}
	if code := call(t, h, "GET", "/create/api/spy/77", "", nil, &ad); code != 200 || ad.VerticalName != "Tinnitus" || ad.Name != "Spy 77 · Acme Health" {
		t.Fatalf("spy ad: %d %+v", code, ad)
	}
	if code := call(t, h, "GET", "/create/api/spy/78", "", nil, nil); code != 404 {
		t.Errorf("an ad Spy does not have: %d", code)
	}
	var opened struct {
		Session sessions.Session
		Picked  []int64
		Warning string
	}
	if code := call(t, h, "POST", "/create/api/spy/77/session", "application/json", strings.NewReader(`{}`), &opened); code != 200 ||
		opened.Session.VerticalID != "tinnitus" || len(opened.Picked) != 2 || opened.Warning != "" {
		t.Fatalf("from spy: %d %+v", code, opened)
	}
	var again struct {
		Session sessions.Session
		Picked  []int64
	}
	call(t, h, "POST", "/create/api/spy/77/session", "application/json", strings.NewReader(`{}`), &again)
	if again.Session.ID != opened.Session.ID || len(again.Picked) != 2 || sp.pics != 1 {
		t.Errorf("again: %+v, %d downloads", again, sp.pics)
	}
	var sd sessions.Detail
	call(t, h, "GET", "/create/api/sessions/"+itoa(opened.Session.ID), "", nil, &sd)
	if len(sd.Items) != 2 || sd.Items[0].Origin != "spy" || sd.Items[1].Text != "Doctors Hate This Trick" {
		t.Errorf("spy items: %+v", sd.Items)
	}

	// A change from another site's page is refused.
	req := httptest.NewRequest("POST", "/create/api/sessions/"+id+"/turns", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: %d", rec.Code)
	}
	if code := call(t, h, "GET", "/create/library-api/api/sets", "", nil, nil); code != 200 {
		t.Errorf("library browse: %d", code)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// The feedback's server side: the status lists the headline models, sizes
// and platforms; a session takes its platform; a send takes a size and an
// on model only; a turn is interrupted; a failed picture is tried again.
func TestFeedbackAPI(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := sessions.New(testdb.New(t), &files.Dir{Root: t.TempDir()}, func() {})
	l := &lib{}
	web, err := site.New(st, l, &spy{}, http.NotFoundHandler(), on{}, log, "test")
	if err != nil {
		t.Fatal(err)
	}
	h := web.Handler()
	var status struct {
		HeadlineModels []site.HeadlineModel `json:"headline_models"`
		Sizes          []openai.Size
		Platforms      []map[string]string
	}
	if code := call(t, h, "GET", "/create/api/status", "", nil, &status); code != 200 || len(status.HeadlineModels) != 2 ||
		status.HeadlineModels[0].ID != "openai" || status.HeadlineModels[1].ID != "grok" || len(status.Sizes) != 3 || len(status.Platforms) != 2 {
		t.Fatalf("status: %d %+v", code, status)
	}
	var sess sessions.Session
	if code := call(t, h, "POST", "/create/api/sessions", "application/json",
		strings.NewReader(`{"name":"NB","vertical_id":"tinnitus","platform":"newsbreak"}`), &sess); code != 201 || sess.Platform != "newsbreak" {
		t.Fatalf("session: %d %+v", code, sess)
	}
	id := itoa(sess.ID)
	if code := call(t, h, "POST", "/create/api/sessions/"+id+"/turns", "application/json",
		strings.NewReader(`{"prompt":"x","headlines":1,"model":"kimi"}`), nil); code != 400 {
		t.Errorf("a model that is not on: %d", code)
	}
	var turn sessions.Turn
	if code := call(t, h, "POST", "/create/api/sessions/"+id+"/turns", "application/json",
		strings.NewReader(`{"prompt":"x","images":2,"headlines":1,"model":"grok","size":"vertical"}`), &turn); code != 202 ||
		turn.Size != "vertical" || turn.HeadlineModel != "grok" {
		t.Fatalf("send: %d %+v", code, turn)
	}
	if code := call(t, h, "POST", "/create/api/turns/"+itoa(turn.ID)+"/interrupt", "application/json", nil, &turn); code != 200 || turn.InterruptedAt == nil {
		t.Fatalf("interrupt: %d %+v", code, turn)
	}
	if code := call(t, h, "POST", "/create/api/turns/"+itoa(turn.ID)+"/interrupt", "application/json", nil, nil); code != 400 {
		t.Errorf("interrupt twice: %d", code)
	}
	if code := call(t, h, "POST", "/create/api/turns/999999/interrupt", "application/json", nil, nil); code != 404 {
		t.Errorf("interrupt a turn that is not there: %d", code)
	}
	// A picture that failed (here: made failed by hand) is tried again.
	var item int64
	if err := st.DB().QueryRow(context.Background(), `
		INSERT INTO create_app.item (session_id, turn_id, kind, origin, brief, state, error)
		VALUES ($1, $2, 'image', 'made', 'b', 'failed', 'muitas requisições') RETURNING id`, sess.ID, turn.ID).Scan(&item); err != nil {
		t.Fatal(err)
	}
	var it sessions.Item
	if code := call(t, h, "POST", "/create/api/items/"+itoa(item)+"/retry", "application/json", nil, &it); code != 202 || it.State != "waiting" {
		t.Fatalf("retry: %d %+v", code, it)
	}
	if code := call(t, h, "POST", "/create/api/items/"+itoa(item)+"/retry", "application/json", nil, nil); code != 400 {
		t.Errorf("retry a waiting picture: %d", code)
	}
}
