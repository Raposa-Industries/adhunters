package page

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var (
	rxEmail = regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	rxPhone = regexp.MustCompile(`(?:\+?1[-.\s]?)?\(?[0-9]{3}\)?[-.\s]?[0-9]{3}[-.\s]?[0-9]{4}`)
	// A company name: one to five capitalized words, then the legal suffix.
	// Case matters: the old case-blind pattern caught whole sentences.
	rxCorporateLLC = regexp.MustCompile(`\b((?:[A-Z][A-Za-z0-9&'-]*,? ){1,5}(?:LLC|L\.L\.C|Inc|Corp|Corporation|Limited|Ltd)\b\.?)`)
	// Footer words that the name pattern picks up in front of the real name.
	rxEntityLead    = regexp.MustCompile(`^(?:(?:Copyright|All|Rights|Reserved|By|The|And|Of|Operated|Owned|Powered|Site|Website|Product|Products|Sold|Marketed|Distributed)[,.]? )+`)
	rxFDA           = regexp.MustCompile(`(?i)(statements have not been evaluated by the food and drug administration|these statements have not been evaluated|not intended to diagnose, treat, cure|individual results may vary|consult your physician)`)
	rxDisclaimers   = regexp.MustCompile(`(?i)(privacy policy|terms of service|terms & conditions|disclaimer|affiliate disclosure|all rights reserved)`)
	spaceCollapseRx = regexp.MustCompile(`\s+`)
)

type DOMTelemetry struct {
	Title                 string              `json:"title"`
	Headings              map[string][]string `json:"headings"`
	MetaTags              map[string]string   `json:"meta_tags"`
	FaviconURL            string              `json:"favicon_url,omitempty"`
	VslTelemetry          map[string]any      `json:"vsl_telemetry"`
	LegalCorporate        map[string]any      `json:"legal_corporate"`
	ComplianceDisclaimers []string            `json:"compliance_disclaimers"`
	WordCount             int                 `json:"word_count"`
	BodyExcerpt           string              `json:"body_excerpt"`
	BodyText              string              `json:"body_text"` // up to maxBodyText characters
}

// maxBodyText caps the page text kept per landing page version. Enough for the
// longest sales letters; the walker reads at most 500 KB of HTML anyway.
const maxBodyText = 100000

func SanitizeUTF8(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ToValidUTF8(s, "")
	return strings.ReplaceAll(s, "\x00", "")
}

func ParseDOM(rawHTML []byte, baseURL string) *DOMTelemetry {
	cleanHTML := strings.ToValidUTF8(string(rawHTML), "")
	cleanHTML = strings.ReplaceAll(cleanHTML, "\x00", "")

	doc, err := html.Parse(strings.NewReader(cleanHTML))
	if err != nil {
		return &DOMTelemetry{
			Headings:       make(map[string][]string),
			MetaTags:       make(map[string]string),
			VslTelemetry:   make(map[string]any),
			LegalCorporate: make(map[string]any),
		}
	}

	tel := &DOMTelemetry{
		Headings:       make(map[string][]string),
		MetaTags:       make(map[string]string),
		VslTelemetry:   make(map[string]any),
		LegalCorporate: make(map[string]any),
	}

	var textBuilder strings.Builder
	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)

			switch tag {
			case "title":
				if n.FirstChild != nil && tel.Title == "" {
					tel.Title = SanitizeUTF8(strings.TrimSpace(n.FirstChild.Data))
				}
			case "h1", "h2":
				var hText string
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						hText += c.Data
					}
				}
				hText = strings.TrimSpace(spaceCollapseRx.ReplaceAllString(hText, " "))
				hText = SanitizeUTF8(hText)
				if hText != "" && len(hText) < 300 {
					tel.Headings[tag] = append(tel.Headings[tag], hText)
				}
			case "meta":
				var name, property, content string
				for _, attr := range n.Attr {
					k := strings.ToLower(attr.Key)
					if k == "name" {
						name = strings.ToLower(attr.Val)
					} else if k == "property" {
						property = strings.ToLower(attr.Val)
					} else if k == "content" {
						content = strings.TrimSpace(attr.Val)
					}
				}
				metaKey := name
				if metaKey == "" {
					metaKey = property
				}
				if metaKey != "" && content != "" {
					if strings.HasPrefix(metaKey, "og:") || strings.HasPrefix(metaKey, "twitter:") ||
						metaKey == "description" || metaKey == "keywords" || metaKey == "author" {
						tel.MetaTags[metaKey] = content
					}
				}
			case "link":
				isIcon := false
				var href string
				for _, attr := range n.Attr {
					k := strings.ToLower(attr.Key)
					if (k == "rel") && (strings.Contains(strings.ToLower(attr.Val), "icon")) {
						isIcon = true
					}
					if k == "href" {
						href = attr.Val
					}
				}
				if isIcon && href != "" && tel.FaviconURL == "" {
					resolved := resolveURL(baseURL, href)
					// Only store crawlable http(s) favicons; data URIs can exceed btree
					// index entry limits and blow up landing page inserts.
					if strings.HasPrefix(resolved, "http") && len(resolved) <= 512 {
						tel.FaviconURL = resolved
					}
				}
			case "iframe", "video":
				for _, attr := range n.Attr {
					val := strings.ToLower(attr.Val)
					if strings.Contains(val, "vidalytics") {
						tel.VslTelemetry["vidalytics"] = true
					} else if strings.Contains(val, "wistia") {
						tel.VslTelemetry["wistia"] = true
					} else if strings.Contains(val, "vimeo.com") {
						tel.VslTelemetry["vimeo"] = true
					} else if strings.Contains(val, "youtube.com") || strings.Contains(val, "youtu.be") {
						tel.VslTelemetry["youtube"] = true
					} else if strings.Contains(val, "b-cdn.net") || strings.Contains(val, "bunny") {
						tel.VslTelemetry["bunny_stream"] = true
					}
				}
			}
		}

		if n.Type == html.TextNode {
			// Skip script and style tags
			if n.Parent == nil || (n.Parent.Data != "script" && n.Parent.Data != "style" && n.Parent.Data != "noscript") {
				textBuilder.WriteString(n.Data)
				textBuilder.WriteString(" ")
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(doc)

	// Clean body text
	fullText := spaceCollapseRx.ReplaceAllString(textBuilder.String(), " ")
	fullText = SanitizeUTF8(strings.TrimSpace(fullText))

	// Word count
	words := strings.Fields(fullText)
	tel.WordCount = len(words)

	// Body excerpt (first ~1000 characters)
	if len(fullText) > 1000 {
		tel.BodyExcerpt = SanitizeUTF8(fullText[:1000]) + "..."
	} else {
		tel.BodyExcerpt = SanitizeUTF8(fullText)
	}
	tel.BodyText = truncateRunes(fullText, maxBodyText)

	// Extract legal & corporate footer signals
	lowerText := strings.ToLower(fullText)
	var emails []string
	for _, m := range rxEmail.FindAllString(fullText, 5) {
		if !strings.HasSuffix(m, ".png") && !strings.HasSuffix(m, ".jpg") {
			emails = append(emails, m)
		}
	}
	if len(emails) > 0 {
		tel.LegalCorporate["emails"] = emails
	}

	var phones []string
	for _, p := range rxPhone.FindAllString(fullText, 3) {
		phones = append(phones, strings.TrimSpace(p))
	}
	if len(phones) > 0 {
		tel.LegalCorporate["phones"] = phones
	}

	var llcs []string
	seenLLC := map[string]bool{}
	for _, m := range rxCorporateLLC.FindAllString(fullText, 10) {
		cleaned := strings.TrimSpace(rxEntityLead.ReplaceAllString(m, ""))
		if strings.Count(cleaned, " ") >= 1 && len(cleaned) < 80 && !seenLLC[cleaned] {
			seenLLC[cleaned] = true
			llcs = append(llcs, cleaned)
		}
		if len(llcs) == 5 {
			break
		}
	}
	if len(llcs) > 0 {
		tel.LegalCorporate["entities"] = llcs
	}

	// Compliance Disclaimers
	if rxFDA.MatchString(lowerText) {
		tel.ComplianceDisclaimers = append(tel.ComplianceDisclaimers, "FDA_DISCLAIMER_FOUND")
	}
	if strings.Contains(lowerText, "affiliate disclosure") || strings.Contains(lowerText, "compensation disclosure") {
		tel.ComplianceDisclaimers = append(tel.ComplianceDisclaimers, "AFFILIATE_DISCLOSURE")
	}
	if strings.Contains(lowerText, "results may vary") {
		tel.ComplianceDisclaimers = append(tel.ComplianceDisclaimers, "RESULTS_MAY_VARY")
	}

	return tel
}

// truncateRunes keeps the first n characters of s.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

func resolveURL(base, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if strings.HasPrefix(ref, "//") {
		return "https:" + ref
	}
	u, err := url.Parse(base)
	if err != nil {
		return ref
	}
	rel, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return u.ResolveReference(rel).String()
}
