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

type lib struct{ sets int }

func (l *lib) File(context.Context, string) ([]byte, error) { return nil, library.ErrNotFound }
func (l *lib) AddSet(context.Context, library.NewSet) (library.Set, error) {
	l.sets++
	return library.Set{ID: 9}, nil
}
func (l *lib) AddCreative(context.Context, library.CreativeMeta, string, []byte) (library.Creative, error) {
	return library.Creative{ID: 1}, nil
}
func (l *lib) AddHeadlines(context.Context, []library.NewHeadline) error { return nil }

type on struct{}

func (on) OpenAIWhy() string { return "" }

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
	w := sessions.NewWorker(st, ai{}, &lib{}, log)
	browse := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { _, _ = rw.Write([]byte("lib " + r.URL.Path)) })
	web, err := site.New(st, &lib{}, browse, on{}, log, "test")
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
