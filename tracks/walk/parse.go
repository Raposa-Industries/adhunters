package walk

import (
	"crypto/md5"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/Raposa-Industries/adhunters/shared/page"
)

// maxText is how much of a page's visible text a version keeps: enough for
// the classifier and people, where the collector kept up to 100,000.
const maxText = 20000

// Parsed is what one page of a walk said.
type Parsed struct {
	Step     int
	URL      string
	FinalURL string
	Host     string
	Status   int
	Hops     int
	PageType string
	Checkout string
	Seller   string
	Version  *Version // nil when the page did not answer 2xx with a body
}

// Version is a page's content: the collector's landing page version.
type Version struct {
	Hash        uuid.UUID
	Title       string
	WordCount   int
	Headings    map[string][]string
	Meta        map[string]string
	FaviconURL  string
	Pixels      map[string][]string // kind -> ids; a kind seen without an id has none
	Emails      []string
	Phones      []string
	Companies   []string
	Disclaimers []string
	VSL         map[string]any
	Text        string
}

// Parse reads every page of a walk.
func Parse(rec *Record) []Parsed {
	out := make([]Parsed, 0, len(rec.Pages))
	for i := range rec.Pages {
		out = append(out, parsePage(&rec.Pages[i]))
	}
	return out
}

func parsePage(p *Page) Parsed {
	out := Parsed{Step: p.Step, URL: p.URL, FinalURL: p.FinalURL, Status: p.Status, Hops: max(0, len(p.Hops)-1)}
	if p.FinalURL != "" {
		out.Host = page.ExtractCanonicalDomain(p.FinalURL)
	}
	body := p.BodyBytes()
	if p.Status < 200 || p.Status >= 300 || len(body) == 0 {
		return out
	}
	html := string(body)
	dom := page.ParseDOM(body, p.FinalURL)
	checkout := page.DetectCheckout(html, p.FinalURL)
	out.Checkout, out.Seller = checkout.Platform, checkout.MerchantID
	out.PageType = page.DetectPageType(html, p.FinalURL, checkout)

	v := &Version{
		Title:       dom.Title,
		WordCount:   dom.WordCount,
		Headings:    dom.Headings,
		Meta:        dom.MetaTags,
		FaviconURL:  dom.FaviconURL,
		Pixels:      pixels(page.ExtractPixels(html)),
		Emails:      strs(dom.LegalCorporate["emails"]),
		Phones:      strs(dom.LegalCorporate["phones"]),
		Companies:   strs(dom.LegalCorporate["entities"]),
		Disclaimers: dom.ComplianceDisclaimers,
		VSL:         dom.VslTelemetry,
		Text:        clip(dom.BodyText, maxText),
	}
	if v.Disclaimers == nil {
		v.Disclaimers = []string{}
	}
	v.Hash = v.hash()
	out.Version = v
	return out
}

// hash is over everything a version keeps, so the same content is one row.
func (v *Version) hash() uuid.UUID {
	b, _ := json.Marshal([]any{v.Title, v.WordCount, v.Headings, v.Meta, v.FaviconURL, v.Pixels, v.Emails,
		v.Phones, v.Companies, v.Disclaimers, v.VSL, v.Text})
	sum := md5.Sum(b)
	u, _ := uuid.FromBytes(sum[:])
	return u
}

// pixels turns ExtractPixels' map into kind -> sorted ids.
func pixels(m map[string]any) map[string][]string {
	out := map[string][]string{}
	for kind, v := range m {
		ids := []string{}
		if p, ok := v.(map[string]any); ok {
			ids = append(ids, strs(p["ids"])...)
		}
		sort.Strings(ids)
		out[kind] = slices.Compact(ids)
	}
	return out
}

func strs(v any) []string {
	switch x := v.(type) {
	case []string:
		return append([]string{}, x...)
	case []any:
		out := []string{}
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return []string{}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
