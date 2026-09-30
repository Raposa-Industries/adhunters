package classify

import (
	"math"
	"sort"
	"strings"

	"github.com/Raposa-Industries/adhunters/spy/verticals"
)

// Part weights: a headline names the product more surely than a landing
// page title, a brand only sometimes says what it sells, and a page's body
// text mentions much besides its product. Ported from the collector's rules
// (migration 030), which weighed headlines 3 and page titles 2; the brand and
// the body are new.
const (
	weightAd    = 3
	weightBrand = 2
	weightPage  = 2
	weightBody  = 1
	// partCap caps a vertical's points in one part before its weight, so one
	// long page repeating a word does not outvote the headline.
	partCap = 6
	// Sure is the confidence an answer needs to count as sure: below it the
	// creative is unsure and the model is asked.
	Sure = 0.6
)

// Text is what the rules and the model read about one creative.
type Text struct {
	Ad    string // headlines and descriptions of its newest ads
	Brand string // the brands those ads show
	Page  string // the landing page's title, headings and description, and the titles of the pages Raposa reached
	Body  string // the start of the landing page's visible text
}

// Doc is the model's view of the text: the brand counts as ad text.
func (t Text) Doc() Doc { return Doc{Ad: t.Ad + " . " + t.Brand, Page: t.Page + " . " + t.Body} }

// Answer is what the rules (or the model) say about one creative.
type Answer struct {
	Category   string             // empty when nothing matched
	Vertical   string             // empty when nothing matched
	Confidence float64            // 0 to 1, two decimals
	Source     string             // the part that gave most points: ad, brand, page (or model)
	Points     map[string]float64 // points per vertical, for evidence
	Hits       []string           // the keywords and hints found, for evidence
}

// Rules score text against the vertical list.
type Rules struct {
	list      verticals.List
	byID      map[string]verticals.Vertical
	byFirst   map[string][]rule
	prefix    []rule
	all       *keywords // every keyword and hint, for masking in the model
	catOrder  map[string]int
	vertOrder map[string]int
}

type rule struct {
	kw       keyword
	text     string
	vertical string
	strong   bool // a keyword; false: a hint
}

// NewRules builds the rules from the list.
func NewRules(l verticals.List) *Rules {
	r := &Rules{list: l, byID: map[string]verticals.Vertical{}, byFirst: map[string][]rule{},
		catOrder: map[string]int{}, vertOrder: map[string]int{}}
	var every []string
	for ci, c := range l.Categories {
		r.catOrder[c.ID] = ci
	}
	for vi, v := range l.Verticals() {
		r.byID[v.ID] = v
		r.vertOrder[v.ID] = vi
		add := func(list []string, strong bool) {
			for _, k := range list {
				every = append(every, k)
				prefix := strings.HasSuffix(k, "*")
				w := tokens(strings.TrimSuffix(k, "*"))
				if len(w) == 0 {
					continue
				}
				ru := rule{kw: keyword{words: w, prefix: prefix}, text: k, vertical: v.ID, strong: strong}
				if prefix && len(w) == 1 {
					r.prefix = append(r.prefix, ru)
				} else {
					r.byFirst[w[0]] = append(r.byFirst[w[0]], ru)
				}
			}
		}
		add(v.Keywords, true)
		add(v.Hints, false)
	}
	r.all = parseKeywords(every)
	return r
}

// Keywords are every keyword and hint, for masking.
func (r *Rules) Keywords() *keywords { return r.all }

// CategoryOf maps each vertical to its category.
func (r *Rules) CategoryOf() map[string]string {
	m := map[string]string{}
	for id, v := range r.byID {
		m[id] = v.Category
	}
	return m
}

// Vertical returns a vertical of the list.
func (r *Rules) Vertical(id string) (verticals.Vertical, bool) {
	v, ok := r.byID[id]
	return v, ok
}

// hits finds the keywords and hints in text: each one counts once.
func (r *Rules) hits(text string) map[*rule]bool {
	toks := tokens(text)
	found := map[*rule]bool{}
	for i, t := range toks {
		for _, first := range []string{t, strings.TrimSuffix(t, "s"), strings.TrimSuffix(t, "es")} {
			list := r.byFirst[first]
			for j := range list {
				if list[j].kw.matchAt(toks, i) > 0 {
					found[&list[j]] = true
				}
			}
			if !strings.HasSuffix(t, "s") {
				break
			}
		}
		for j := range r.prefix {
			if r.prefix[j].kw.matchAt(toks, i) > 0 {
				found[&r.prefix[j]] = true
			}
		}
	}
	return found
}

// Classify scores one creative's text.
//
// Each part gives each vertical 2 points per keyword and 1 per hint, capped
// at 6, times the part's weight. A hint counts only when a keyword of the
// same category was found somewhere, or for a catch-all vertical (which has
// only hints): hints only help when something points the same way. The
// category with the most points wins, then its vertical with the most
// points; a catch-all only when no other vertical of its category scored.
//
// Confidence = (category's share of all points) × min(1, category points /
// 6) × (vertical's share of its category's points, or 0.5 for a catch-all).
// Ported from the collector's refresh_creative_vertical (migration 030), with
// our categories in place of its broad verticals.
func (r *Rules) Classify(t Text) Answer {
	type part struct {
		name   string
		text   string
		weight float64
	}
	parts := []part{{"ad", t.Ad, weightAd}, {"brand", t.Brand, weightBrand}, {"page", t.Page, weightPage}, {"body", t.Body, weightBody}}
	found := make([]map[*rule]bool, len(parts))
	strongCat := map[string]bool{}
	for i, p := range parts {
		found[i] = r.hits(p.text)
		for ru := range found[i] {
			if ru.strong {
				strongCat[r.byID[ru.vertical].Category] = true
			}
		}
	}
	points := map[string]float64{}
	bySource := map[string]map[string]float64{}
	var hitList []string
	seen := map[string]bool{}
	for i, p := range parts {
		raw := map[string]float64{}
		for ru := range found[i] {
			v := r.byID[ru.vertical]
			if ru.strong {
				raw[ru.vertical] += 2
			} else if strongCat[v.Category] || v.CatchAll {
				raw[ru.vertical]++
			} else {
				continue
			}
			if !seen[ru.text] {
				seen[ru.text] = true
				hitList = append(hitList, ru.text)
			}
		}
		for v, n := range raw {
			pts := math.Min(n, partCap) * p.weight
			points[v] += pts
			if bySource[v] == nil {
				bySource[v] = map[string]float64{}
			}
			bySource[v][p.name] += pts
		}
	}
	sort.Strings(hitList)
	a := Answer{Points: points, Hits: hitList}
	if len(points) == 0 {
		return a
	}

	var all float64
	catPts := map[string]float64{}
	specific := map[string]float64{} // points of a category's verticals that are not catch-alls
	for v, p := range points {
		all += p
		vv := r.byID[v]
		catPts[vv.Category] += p
		if !vv.CatchAll {
			specific[vv.Category] += p
		}
	}
	best := ""
	for c, p := range catPts {
		if best == "" || p > catPts[best] || (p == catPts[best] && r.catOrder[c] < r.catOrder[best]) {
			best = c
		}
	}
	vert := ""
	for v, p := range points {
		vv := r.byID[v]
		if vv.Category != best || (vv.CatchAll && specific[best] > 0) {
			continue
		}
		if vert == "" || p > points[vert] || (p == points[vert] && r.vertOrder[v] < r.vertOrder[vert]) {
			vert = v
		}
	}
	share := 0.5
	if !r.byID[vert].CatchAll {
		share = points[vert] / specific[best]
	}
	conf := catPts[best] / all * math.Min(1, catPts[best]/partCap) * share
	a.Category, a.Vertical = best, vert
	a.Confidence = math.Round(conf*100) / 100
	src := ""
	for s, p := range bySource[vert] {
		if src == "" || p > bySource[vert][src] || (p == bySource[vert][src] && s < src) {
			src = s
		}
	}
	a.Source = src
	return a
}
