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

	"github.com/Raposa-Industries/adhunters/create/internal/briefs"
	"github.com/Raposa-Industries/adhunters/create/internal/library"
	"github.com/Raposa-Industries/adhunters/create/internal/openai"
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
	var w *briefs.Worker
	st := briefs.New(testdb.New(t), &files.Dir{Root: t.TempDir()}, func() {})
	w = briefs.NewWorker(st, ai{}, &lib{}, log)
	browse := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { _, _ = rw.Write([]byte("lib " + r.URL.Path)) })
	h := site.New(st, browse, on{}, log, "test").Handler()
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

	for _, p := range []string{"/create/", "/create/new", "/create/briefs/3", "/create/library", "/create/static/app.js", "/create/_frame/frame.js"} {
		if code := call(t, h, "GET", p, "", nil, nil); code != 200 {
			t.Errorf("%s: %d", p, code)
		}
	}

	var d briefs.Detail
	if code := call(t, h, "POST", "/create/api/briefs", "application/json", strings.NewReader(`{"vertical_id":"memory-loss","images":1,"headlines":2,"refs":["nope:1"]}`), &d); code != 400 {
		t.Errorf("bad ref: %d", code)
	}
	if code := call(t, h, "POST", "/create/api/briefs", "application/json", strings.NewReader(`{"vertical_id":"memory-loss","vertical_name":"Memory Loss","images":1,"headlines":2}`), &d); code != 201 || d.Brief.RequestedBy != "mari@example.com" {
		t.Fatalf("new brief: %d %+v", code, d.Brief)
	}
	id := itoa(d.Brief.ID)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "ad.png")
	_ = png.Encode(fw, image.NewRGBA(image.Rect(0, 0, 60, 40)))
	_ = mw.Close()
	var ref briefs.Reference
	if code := call(t, h, "POST", "/create/api/briefs/"+id+"/references", mw.FormDataContentType(), &body, &ref); code != 201 || ref.URL == "" {
		t.Fatalf("upload: %d %+v", code, ref)
	}
	if code := call(t, h, "GET", ref.URL, "", nil, nil); code != 200 {
		t.Errorf("reference file: %d", code)
	}

	if code := call(t, h, "POST", "/create/api/briefs/"+id+"/make", "", nil, nil); code != 202 {
		t.Fatalf("make: %d", code)
	}
	drain()
	call(t, h, "GET", "/create/api/briefs/"+id, "", nil, &d)
	if d.Brief.State != "ready" || len(d.Options) != 3 {
		t.Fatalf("after making: %+v", d)
	}
	var head briefs.Option
	for _, o := range d.Options {
		if o.Kind == "headline" && strings.Contains(o.Text, "SHOCKING") {
			head = o
		}
	}
	if len(head.Warnings) < 2 {
		t.Errorf("warnings: %+v", head.Warnings)
	}
	var ids []int64
	for _, o := range d.Options {
		var got briefs.Option
		if code := call(t, h, "PATCH", "/create/api/options/"+itoa(o.ID), "application/json", strings.NewReader(`{"chosen":true}`), &got); code != 200 || !got.Chosen {
			t.Fatalf("choose: %d %+v", code, got)
		}
		ids = append(ids, o.ID)
	}
	b, _ := json.Marshal(map[string]any{"option_ids": ids, "name": "ML test", "ai_label": "ai"})
	var sv briefs.Save
	if code := call(t, h, "POST", "/create/api/briefs/"+id+"/save", "application/json", bytes.NewReader(b), &sv); code != 202 {
		t.Fatalf("save: %d", code)
	}
	drain()
	call(t, h, "GET", "/create/api/saves/"+itoa(sv.ID), "", nil, &sv)
	if sv.State != "done" || sv.LibrarySetID == nil || *sv.LibrarySetID != 9 {
		t.Fatalf("saved: %+v", sv)
	}

	// A change from another site's page is refused.
	req := httptest.NewRequest("POST", "/create/api/briefs/"+id+"/make", nil)
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

func TestParseRef(t *testing.T) {
	for in, want := range map[string]string{"spy:ad:12": "spy_ad 12", "library:creative:4": "library_creative 4", "spy:ad:x": "", "drive:file:1": ""} {
		k, id, ok := site.ParseRef(in)
		got := ""
		if ok {
			got = k + " " + id
		}
		if got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
