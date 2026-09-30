package images

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"testing"
)

func TestPutGet(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1000, 600)), nil)
	info, err := s.Put("../../photo one.jpg", b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 1000 || info.Height != 600 || info.Type != "image/jpeg" || len(info.SHA) != 64 {
		t.Fatalf("%+v", info)
	}
	again, err := s.Put("other.jpg", b.Bytes())
	if err != nil || again.SHA != info.SHA {
		t.Fatalf("same picture twice: %+v %v", again, err)
	}
	data, name, err := s.Get(info.SHA)
	if err != nil || !bytes.Equal(data, b.Bytes()) || name != "other.jpg" {
		t.Fatalf("get: %q %v", name, err)
	}
	if _, _, err := s.Get("../" + info.SHA); err == nil {
		t.Error("a path outside the store was read")
	}
	if _, err := s.Put("x.txt", []byte("hello, not a picture")); !errors.Is(err, ErrNotImage) {
		t.Errorf("text kept as a picture: %v", err)
	}
}
