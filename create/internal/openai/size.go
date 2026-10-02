package openai

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"math"

	"golang.org/x/image/draw"
)

// Size is a picture size a turn can ask for (GLOSSARY: picture size).
type Size struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Sizes are the picture sizes the composer offers, the default first.
// The picture model makes any canvas whose edges are multiples of 16, so
// landscape and vertical are made as they are; NewsBreak's 1504x786 is not
// (786 is not a multiple of 16), so it is made at 1504x784 and cut to size
// here.
var Sizes = []Size{
	{ID: "landscape", Label: "16:9 horizontal", Width: 1600, Height: 896},
	{ID: "vertical", Label: "9:16 vertical", Width: 896, Height: 1600},
	{ID: "newsbreak", Label: "NewsBreak 1504×786", Width: 1504, Height: 786},
}

// SizeByID returns a size by its id; "" is the default (landscape).
func SizeByID(id string) (Size, bool) {
	if id == "" {
		return Sizes[0], true
	}
	for _, s := range Sizes {
		if s.ID == id {
			return s, true
		}
	}
	return Size{}, false
}

// String is the size as the images endpoints write it.
func (s Size) String() string { return fmt.Sprintf("%dx%d", s.Width, s.Height) }

// Native is the canvas asked of the picture model for s: s itself when both
// edges are multiples of 16, else the closest shape that is, keeping the
// longer edge.
func (s Size) Native() Size {
	if s.Width%16 == 0 && s.Height%16 == 0 {
		return s
	}
	n := s
	round := func(v float64) int { return max(16, int(math.Round(v/16))*16) }
	if s.Width >= s.Height {
		n.Width = round(float64(s.Width))
		n.Height = round(float64(s.Height) * float64(n.Width) / float64(s.Width))
	} else {
		n.Height = round(float64(s.Height))
		n.Width = round(float64(s.Width) * float64(n.Height) / float64(s.Height))
	}
	return n
}

// fit scales a picture to cover w x h and cuts the middle out, as a JPEG.
// It is used only when the model could not make the size itself; the
// model's own picture is kept before.
func fit(data []byte, w, h int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	scale := math.Max(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	sw, sh := int(math.Ceil(float64(b.Dx())*scale)), int(math.Ceil(float64(b.Dy())*scale))
	scaled := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), src, b, draw.Src, nil)
	x0, y0 := (sw-w)/2, (sh-h)/2
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(out, out.Bounds(), scaled, image.Pt(x0, y0), draw.Src)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 92}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
