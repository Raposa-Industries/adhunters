package web_test

import (
	"bytes"
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

	"github.com/Raposa-Industries/adhunters/library/internal/store"
	"github.com/Raposa-Industries/adhunters/library/internal/testdb"
	"github.com/Raposa-Industries/adhunters/library/internal/web"
)

type offDrive struct{ kicks int }

func (d *offDrive) Kick()          { d.kicks++ }
func (d *offDrive) Folder() string { return "" }
func (d *offDrive) Why() string    { return "not signed in" }

func do(t *testing.T, h http.Handler, method, path, contentType string, body io.Reader) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func upload(t *testing.T, h http.Handler, meta string, data []byte) (int, map[string]any) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.WriteField("meta", meta)
	p, _ := w.CreateFormFile("file", "x.png")
	_, _ = p.Write(data)
	_ = w.Close()
	return do(t, h, http.MethodPost, "/api/creatives", w.FormDataContentType(), &b)
}

func TestAPI(t *testing.T) {
	d := &offDrive{}
	h := web.New(store.New(testdb.New(t)), d, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()

	code, set := do(t, h, http.MethodPost, "/api/sets", "application/json",
		strings.NewReader(`{"name":"Tinnitus test","vertical_id":"tinnitus","origin":"create","made_by":"vini"}`))
	if code != http.StatusCreated {
		t.Fatalf("set: %d %v", code, set)
	}
	setID := int(set["id"].(float64))

	var pic bytes.Buffer
	_ = png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 40, 30)))
	meta := `{"vertical_id":"tinnitus","set_id":` + itoa(setID) + `,"origin":"create","ai_label":"ai","angle":"Garrafa"}`
	code, c := upload(t, h, meta, pic.Bytes())
	if code != http.StatusCreated || c["name"] != "TINT1" {
		t.Fatalf("creative: %d %v", code, c)
	}
	if code, _ := upload(t, h, meta, pic.Bytes()); code != http.StatusOK {
		t.Errorf("same bytes: %d", code)
	}
	if code, e := upload(t, h, meta, []byte("text")); code != http.StatusBadRequest {
		t.Errorf("not a picture: %d %v", code, e)
	}
	if d.kicks != 1 {
		t.Errorf("drive kicked %d times for one new creative", d.kicks)
	}

	code, hs := do(t, h, http.MethodPost, "/api/headlines", "application/json",
		strings.NewReader(`{"headlines":[{"text":"Ringing After 60? Read This","set_id":`+itoa(setID)+`,"origin":"create"}]}`))
	if code != http.StatusOK || len(hs["headlines"].([]any)) != 1 {
		t.Fatalf("headlines: %d %v", code, hs)
	}

	code, got := do(t, h, http.MethodGet, "/api/sets/"+itoa(setID), "", nil)
	if code != http.StatusOK || len(got["creatives"].([]any)) != 1 || len(got["headlines"].([]any)) != 1 {
		t.Fatalf("set: %d %v", code, got)
	}

	id := itoa(int(c["id"].(float64)))
	req := httptest.NewRequest(http.MethodGet, "/thumbs/"+id, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("thumb: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if code, _ := do(t, h, http.MethodGet, "/files/999", "", nil); code != http.StatusNotFound {
		t.Errorf("missing file: %d", code)
	}

	code, ch := do(t, h, http.MethodPatch, "/api/creatives/"+id, "application/json", strings.NewReader(`{"hidden":true}`))
	if code != http.StatusOK || ch["hidden"] != true {
		t.Errorf("hide: %d %v", code, ch)
	}
	if code, _ := do(t, h, http.MethodPatch, "/api/creatives/"+id, "application/json", strings.NewReader(`{"colour":"red"}`)); code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", code)
	}

	code, st := do(t, h, http.MethodGet, "/api/status", "", nil)
	if code != http.StatusOK || st["drive"].(map[string]any)["why_off"] != "not signed in" {
		t.Errorf("status: %d %v", code, st)
	}
	if code, _ := do(t, h, http.MethodPost, "/api/drive/sync", "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("sync while off: %d", code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/verticals", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a page's request: %d", rec.Code)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
