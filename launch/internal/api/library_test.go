package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/launch/internal/images"
	"github.com/Raposa-Industries/adhunters/launch/internal/library"
)

// fakeLibrary answers like library/'s API for one set with one creative.
func fakeLibrary(t *testing.T, pic []byte) (*httptest.Server, *[]string) {
	sum := sha256.Sum256(pic)
	sha := hex.EncodeToString(sum[:])
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		if r.Header.Get("Origin") != "" {
			http.Error(w, `{"error":"no pages"}`, 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sets":
			_, _ = w.Write([]byte(`{"sets":[{"id":7,"name":"Memory morning","vertical_id":"memory-loss","creatives":1,"headlines":1}]}`))
		case "/api/sets/7":
			_, _ = w.Write([]byte(`{"set":{"id":7,"name":"Memory morning"},"creatives":[{"id":3,"name":"MMT12","sha256":"` + sha + `","ai_label":"ai"}],"headlines":[{"id":5,"text":"Doctors Surprised By This Habit","ai_label":"unset"}]}`))
		case "/api/creatives/3":
			_, _ = w.Write([]byte(`{"id":3,"name":"MMT12","sha256":"` + sha + `","media_type":"image/png","ai_label":"ai"}`))
		case "/files/3", "/thumbs/3":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			_, _ = w.Write(pic)
		case "/api/creatives/4":
			_, _ = w.Write([]byte(`{"id":4,"name":"MMT13","sha256":"0000"}`))
		case "/files/4":
			_, _ = w.Write(pic)
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func TestLibrary(t *testing.T) {
	r := setup(t)
	pic := pngBytes(t, 90)
	lib, asked := fakeLibrary(t, pic)

	var e map[string]string
	if code := r.call("GET", "library/sets", nil, &e); code != 503 || !strings.Contains(e["error"], "não está ligado") {
		t.Errorf("no library: %d %v", code, e)
	}
	r.api.Library = library.New(lib.URL)

	var sets struct{ Sets []map[string]any }
	if code := r.call("GET", "library/sets?vertical=memory-loss&hidden=1&x=y", nil, &sets); code != 200 || len(sets.Sets) != 1 {
		t.Fatalf("%d %+v", code, sets)
	}
	if (*asked)[0] != "/api/sets?vertical=memory-loss" {
		t.Errorf("filters passed on: %q", (*asked)[0])
	}
	var set struct {
		Creatives []map[string]any
		Headlines []map[string]any
	}
	if code := r.call("GET", "library/set?id=7", nil, &set); code != 200 || len(set.Creatives) != 1 || len(set.Headlines) != 1 {
		t.Fatalf("%d %+v", code, set)
	}

	res, err := http.Get(r.srv.URL + "/launch/api/library/thumb?id=3")
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("thumb: %v %v", err, res)
	}
	res.Body.Close()

	var used struct {
		Image    images.Info
		Creative library.Creative
	}
	if code := r.call("POST", "library/use?id=3", nil, &used); code != 200 || used.Image.SHA != used.Creative.SHA256 || used.Image.Name != "MMT12.png" || used.Creative.AILabel != "ai" {
		t.Fatalf("%d %+v", code, used)
	}
	if _, _, err := r.api.img.Get(used.Image.SHA); err != nil {
		t.Errorf("picture not kept: %v", err)
	}

	for _, c := range []struct {
		method, path string
		code         int
	}{
		{"POST", "library/use?id=4", 502}, // bytes differ from the library's sha256
		{"POST", "library/use?id=9", 404},
		{"POST", "library/use?id=x", 400},
		{"GET", "library/set?id=99", 404},
		{"GET", "library/drive", 404},
	} {
		if code := r.call(c.method, c.path, nil, &e); code != c.code {
			t.Errorf("%s %s: %d %v", c.method, c.path, code, e)
		}
	}

	lib.Close()
	if code := r.call("GET", "library/verticals", nil, &e); code != 503 {
		t.Errorf("library down: %d %v", code, e)
	}
}
