package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestSizesNative(t *testing.T) {
	for _, c := range []struct {
		id         string
		native     string
		w, h       int
		vertical   bool
		needsCut   bool
		sameAsSize bool
	}{
		{"", "1600x896", 1600, 896, false, false, true},
		{"landscape", "1600x896", 1600, 896, false, false, true},
		{"vertical", "896x1600", 896, 1600, true, false, true},
		{"newsbreak", "1504x784", 1504, 786, false, true, false},
	} {
		s, ok := SizeByID(c.id)
		if !ok || s.Width != c.w || s.Height != c.h {
			t.Fatalf("%q: %+v %v", c.id, s, ok)
		}
		if n := s.Native(); n.String() != c.native || (n == s) != c.sameAsSize {
			t.Errorf("%q native %s, want %s", c.id, n, c.native)
		}
	}
	if _, ok := SizeByID("square"); ok {
		t.Error("an unknown size was found")
	}
}

// A size the model cannot make is asked at its closest native shape, the
// model's picture is kept as it came, and the picture handed back is cut to
// the exact size.
func TestImageFitsNewsBreakSize(t *testing.T) {
	var asked map[string]any
	c, _, dir := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&asked)
		io.WriteString(w, imageReplyJSON(testJPEG(t, 1504, 784), usage))
	})
	size, _ := SizeByID("newsbreak")
	img, err := c.Image(context.Background(), ImageRequest{Brief: "A man at a table", Size: size})
	if err != nil {
		t.Fatal(err)
	}
	if asked["size"] != "1504x784" {
		t.Errorf("asked size %v", asked["size"])
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(img.Data))
	if err != nil || cfg.Width != 1504 || cfg.Height != 786 || img.Width != 1504 || img.Height != 786 || img.MIME != "image/jpeg" {
		t.Fatalf("handed back %dx%d (%dx%d) %s %v", cfg.Width, cfg.Height, img.Width, img.Height, img.MIME, err)
	}
	raw, err := os.ReadFile(img.Kept)
	if err != nil {
		t.Fatal(err)
	}
	if rc, _, _ := image.DecodeConfig(bytes.NewReader(raw)); rc.Width != 1504 || rc.Height != 784 {
		t.Errorf("kept %dx%d, want the model's own 1504x784", rc.Width, rc.Height)
	}
	side, _ := os.ReadFile(kept(t, dir, ".json")[0])
	if !strings.Contains(string(side), `"fit_to": "1504x786"`) || !strings.Contains(string(side), `"size": "1504x784"`) {
		t.Errorf("sidecar %s", side)
	}
}

// A vertical picture is asked as it is and told its shape; nothing is cut.
func TestImageVertical(t *testing.T) {
	var asked map[string]any
	pic := testJPEG(t, 896, 1600)
	c, _, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&asked)
		io.WriteString(w, imageReplyJSON(pic, usage))
	})
	size, _ := SizeByID("vertical")
	img, err := c.Image(context.Background(), ImageRequest{Brief: "A woman", Size: size})
	if err != nil {
		t.Fatal(err)
	}
	if asked["size"] != "896x1600" || !strings.Contains(asked["prompt"].(string), "tall vertical frame") {
		t.Errorf("asked %v", asked)
	}
	if !bytes.Equal(img.Data, pic) {
		t.Error("a native size was changed")
	}
}
