package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// demoLibrary answers like library/'s API with two sets, for the demo.
func demoLibrary(t *testing.T) *httptest.Server {
	t.Helper()
	pics := map[int64][]byte{}
	type cr struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		SHA     string `json:"sha256"`
		Width   int    `json:"width"`
		Height  int    `json:"height"`
		Angle   string `json:"angle"`
		AILabel string `json:"ai_label"`
	}
	type hl struct {
		ID      int64  `json:"id"`
		Text    string `json:"text"`
		AILabel string `json:"ai_label"`
	}
	mk := func(id int64, name string, c color.RGBA, ai string) cr {
		m := image.NewRGBA(image.Rect(0, 0, 1200, 674))
		for y := 0; y < 674; y++ {
			for x := 0; x < 1200; x++ {
				m.Set(x, y, color.RGBA{c.R + uint8(x/40), c.G + uint8(y/40), c.B, 255})
			}
		}
		var b bytes.Buffer
		_ = png.Encode(&b, m)
		pics[id] = b.Bytes()
		sum := sha256.Sum256(b.Bytes())
		return cr{ID: id, Name: name, SHA: hex.EncodeToString(sum[:]), Width: 1200, Height: 674, Angle: "doctor", AILabel: ai}
	}
	sets := map[string]any{
		"1": map[string]any{"set": map[string]any{"id": 1, "name": "Memory morning habit"},
			"creatives": []cr{mk(11, "MMT12", color.RGBA{40, 60, 120, 255}, "ai"), mk(12, "MMT13", color.RGBA{120, 50, 40, 255}, "ai")},
			"headlines": []hl{{21, "Doctors Surprised By This Morning Habit", "unset"}, {22, "The 10-Second Trick For Sharper Memory", "unset"}}},
		"2": map[string]any{"set": map[string]any{"id": 2, "name": "BP seniors kitchen"},
			"creatives": []cr{mk(13, "BPT43", color.RGBA{30, 110, 60, 255}, "not_ai")},
			"headlines": []hl{{23, "Seniors Are Ditching Their Pills For This", "unset"}}},
	}
	byID := map[string]cr{}
	for _, s := range sets {
		for _, c := range s.(map[string]any)["creatives"].([]cr) {
			byID[fmt.Sprint(c.ID)] = c
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		p := r.URL.Path
		switch {
		case p == "/api/verticals":
			send(map[string]any{"verticals": []map[string]string{{"id": "memory-loss", "name": "Memory Loss"}, {"id": "blood-pressure", "name": "Blood Pressure"}}})
		case p == "/api/sets":
			all := []map[string]any{
				{"id": 1, "name": "Memory morning habit", "vertical_id": "memory-loss", "creatives": 2, "headlines": 2, "created_at": "2026-09-30T18:02:00Z"},
				{"id": 2, "name": "BP seniors kitchen", "vertical_id": "blood-pressure", "creatives": 1, "headlines": 1, "created_at": "2026-09-29T14:40:00Z"},
			}
			var out []map[string]any
			for _, s := range all {
				if v := r.URL.Query().Get("vertical"); v == "" || v == s["vertical_id"] {
					out = append(out, s)
				}
			}
			send(map[string]any{"sets": out})
		case strings.HasPrefix(p, "/api/sets/") && sets[strings.TrimPrefix(p, "/api/sets/")] != nil:
			send(sets[strings.TrimPrefix(p, "/api/sets/")])
		case strings.HasPrefix(p, "/api/creatives/") && byID[strings.TrimPrefix(p, "/api/creatives/")].ID != 0:
			send(byID[strings.TrimPrefix(p, "/api/creatives/")])
		case strings.HasPrefix(p, "/files/") || strings.HasPrefix(p, "/thumbs/"):
			c := byID[p[strings.LastIndex(p, "/")+1:]]
			if c.ID == 0 {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pics[c.ID])
		default:
			w.WriteHeader(http.StatusNotFound)
			send(map[string]string{"error": "not found"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
