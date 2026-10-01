package pages

import (
	"fmt"
	"html/template"
	"strings"
)

// retentionChart draws the retention curves: for each second of the video,
// the share of plays still listening, one line per arm. It is drawn here,
// as SVG, so the page needs no chart code.
func retentionChart(curves []Curve, length int) template.HTML {
	const w, h, left, right, top, bottom = 640, 220, 40, 12, 10, 26
	n := length
	for _, c := range curves {
		n = max(n, len(c.At))
	}
	if n < 2 || len(curves) == 0 {
		return ""
	}
	pw, ph := float64(w-left-right), float64(h-top-bottom)
	x := func(s int) float64 { return float64(left) + pw*float64(s)/float64(n-1) }
	y := func(v float64) float64 { return float64(top) + ph*(1-v) }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="curve" viewBox="0 0 %d %d" role="img" aria-label="Retenção: parte dos plays ouvindo em cada segundo">`, w, h)
	for _, v := range []float64{0, .25, .5, .75, 1} {
		fmt.Fprintf(&b, `<line class="grid" x1="%d" x2="%d" y1="%.1f" y2="%.1f"/>`, left, w-right, y(v), y(v))
		fmt.Fprintf(&b, `<text class="tick" x="%d" y="%.1f" text-anchor="end">%d%%</text>`, left-6, y(v)+3.5, int(v*100))
	}
	step := tickStep(n)
	for s := 0; s < n; s += step {
		fmt.Fprintf(&b, `<text class="tick" x="%.1f" y="%d" text-anchor="middle">%s</text>`, x(s), h-8, secs(float64(s)))
	}
	for i, c := range curves {
		if c.Plays == 0 || len(c.At) == 0 {
			continue
		}
		var pts strings.Builder
		for s, v := range c.At {
			fmt.Fprintf(&pts, "%.1f,%.1f ", x(s), y(min(1, float64(v)/float64(c.Plays))))
		}
		fmt.Fprintf(&b, `<polyline class="line c%d" points="%s"><title>%s</title></polyline>`, i%4,
			strings.TrimSpace(pts.String()), template.HTMLEscapeString("Braço "+armLabel(c.Arm)))
	}
	b.WriteString(`</svg>`)
	if len(curves) > 1 {
		b.WriteString(`<div class="legend">`)
		for i, c := range curves {
			fmt.Fprintf(&b, `<span><i class="c%d"></i>%s · %s plays</span>`, i%4, template.HTMLEscapeString(armLabel(c.Arm)), thousands(c.Plays))
		}
		b.WriteString(`</div>`)
	}
	return template.HTML(b.String()) //nolint:gosec // built from numbers; the arm names are escaped
}

func armLabel(a string) string {
	if a == "" {
		return "único"
	}
	return a
}

// tickStep picks seconds between x labels: about six of them, on round
// numbers.
func tickStep(n int) int {
	for _, s := range []int{5, 10, 15, 30, 60, 120, 300, 600, 900, 1800} {
		if n/s <= 6 {
			return s
		}
	}
	return 3600
}
