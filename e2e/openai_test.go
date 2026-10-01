package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeOpenAI answers Create's two calls: the plan (chat completions with a
// JSON schema) and pictures (images generations or edits). It hands back
// more headlines and briefs than asked; Create keeps what it asked for.
type fakeOpenAI struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	calls map[string]int
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	f := &fakeOpenAI{t: t, calls: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenAI) URL() string { return f.srv.URL }

func (f *fakeOpenAI) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *fakeOpenAI) serve(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	if r.Header.Get("Authorization") != "Bearer sk-fake" {
		f.t.Errorf("openai: %s without the key", r.URL.Path)
	}
	f.mu.Lock()
	f.calls[r.URL.Path]++
	f.mu.Unlock()
	switch r.URL.Path {
	case "/v1/chat/completions":
		var plan struct {
			Analysis  []any            `json:"analysis"`
			Headlines []string         `json:"headlines"`
			Briefs    []map[string]any `json:"briefs"`
		}
		plan.Analysis = []any{}
		for i := 1; i <= 12; i++ {
			plan.Headlines = append(plan.Headlines, fmt.Sprintf("Doctors Stunned By This %d-Second Ringing Fix", i*3))
			plan.Briefs = append(plan.Briefs, map[string]any{"angle": "Doctor", "brief": fmt.Sprintf("A calm doctor in a bright clinic holds a small jar, shot %d", i)})
		}
		content, _ := json.Marshal(plan)
		answer(w, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 900, "completion_tokens": 700, "total_tokens": 1600},
		})
	case "/v1/images/generations", "/v1/images/edits":
		answer(w, map[string]any{
			"created": 1,
			"data":    []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(picture(f.t, 1536, 1024))}},
			"usage":   map[string]any{"input_tokens": 100, "output_tokens": 4000, "total_tokens": 4100},
		})
	default:
		f.t.Errorf("openai: no fake answer for %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

// picture is a plain JPEG of that size, a little different each call so
// pictures do not share a SHA-256.
var pictures struct {
	sync.Mutex
	n uint8
}

func picture(t *testing.T, w, h int) []byte {
	pictures.Lock()
	pictures.n++
	n := pictures.n
	pictures.Unlock()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y += 7 {
			img.Set(x, y, color.RGBA{n, uint8(x), uint8(y), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
