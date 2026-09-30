package picture

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// Sample returns a PNG of w by h pixels.
func sample(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, h/2, color.RGBA{255, 0, 0, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReadAndThumb(t *testing.T) {
	b := sample(t, 1200, 674)
	info, err := Read(b)
	if err != nil {
		t.Fatal(err)
	}
	if info.MediaType != "image/png" || info.Width != 1200 || info.Height != 674 || len(info.SHA256) != 64 || len(info.MD5) != 32 {
		t.Fatalf("info = %+v", info)
	}
	th, err := Thumb(b)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(th))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != ThumbWidth || cfg.Height != 269 {
		t.Errorf("thumb %dx%d, want %dx269", cfg.Width, cfg.Height, ThumbWidth)
	}
	small, _ := Thumb(sample(t, 100, 50))
	cfg, _ = jpeg.DecodeConfig(bytes.NewReader(small))
	if cfg.Width != 100 {
		t.Errorf("a small picture is not enlarged: got width %d", cfg.Width)
	}
}

func TestReadRefuses(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("hello"), []byte("\x89PNG\r\n\x1a\nbroken")} {
		if _, err := Read(b); err != ErrNotPicture {
			t.Errorf("Read(%q) err = %v, want ErrNotPicture", b, err)
		}
	}
}
