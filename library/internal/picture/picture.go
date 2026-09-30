// Package picture reads what the library needs from a picture's bytes: its
// kind, its size, its hashes, and a small JPEG to show in lists.
package picture

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// ThumbWidth is the widest a thumbnail gets; lists show them at up to half.
const ThumbWidth = 480

// MaxBytes is the largest picture the library takes (Taboola's own limit is
// 5 MB; this leaves room for masters).
const MaxBytes = 40 << 20

// ErrNotPicture means the bytes are not a JPEG, PNG, WebP or GIF picture.
var ErrNotPicture = errors.New("not a JPEG, PNG, WebP or GIF picture")

// Info is what the library keeps about a picture's bytes.
type Info struct {
	MediaType string
	Width     int
	Height    int
	Bytes     int64
	SHA256    string
	MD5       string
}

// Read checks that b is a picture and describes it.
func Read(b []byte) (Info, error) {
	if len(b) == 0 {
		return Info{}, ErrNotPicture
	}
	if len(b) > MaxBytes {
		return Info{}, fmt.Errorf("picture of %d bytes is over the %d MB limit", len(b), MaxBytes>>20)
	}
	mt := http.DetectContentType(b)
	var cfg image.Config
	var err error
	switch mt {
	case "image/jpeg":
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(b))
	case "image/png":
		cfg, err = png.DecodeConfig(bytes.NewReader(b))
	case "image/gif":
		cfg, err = gif.DecodeConfig(bytes.NewReader(b))
	case "image/webp":
		cfg, err = webp.DecodeConfig(bytes.NewReader(b))
	default:
		return Info{}, ErrNotPicture
	}
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return Info{}, ErrNotPicture
	}
	s := sha256.Sum256(b)
	m := md5.Sum(b)
	return Info{
		MediaType: mt, Width: cfg.Width, Height: cfg.Height, Bytes: int64(len(b)),
		SHA256: hex.EncodeToString(s[:]), MD5: hex.EncodeToString(m[:]),
	}, nil
}

// Thumb makes a JPEG at most ThumbWidth wide, keeping the shape. A picture
// already narrower is re-encoded at its own size. Transparent areas turn
// white, the way a list's card shows them.
func Thumb(b []byte) ([]byte, error) {
	src, err := decode(b)
	if err != nil {
		return nil, err
	}
	sb := src.Bounds()
	w, h := sb.Dx(), sb.Dy()
	if w > ThumbWidth {
		h = max(1, h*ThumbWidth/w)
		w = ThumbWidth
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, xdraw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decode(b []byte) (image.Image, error) {
	var img image.Image
	var err error
	switch http.DetectContentType(b) {
	case "image/jpeg":
		img, err = jpeg.Decode(bytes.NewReader(b))
	case "image/png":
		img, err = png.Decode(bytes.NewReader(b))
	case "image/gif":
		img, err = gif.Decode(bytes.NewReader(b))
	case "image/webp":
		img, err = webp.Decode(bytes.NewReader(b))
	default:
		return nil, ErrNotPicture
	}
	if err != nil {
		return nil, ErrNotPicture
	}
	return img, nil
}

// Ext is the file ending for a media type Read returns.
func Ext(mediaType string) string {
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ".jpg"
}
