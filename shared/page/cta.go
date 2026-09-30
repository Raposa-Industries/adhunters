package page

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Tracker click paths: /click, /click/1, /click.php.
var rxClickPath = regexp.MustCompile(`(?i)/click(/\d*|\.php)?/?$`)

// Links that are never the offer button.
var ctaSkip = []string{"privacy", "terms", "contact", "disclaimer", "policies", "policy", "refund", "about", "cookie", "unsubscribe", "facebook.com", "twitter.com", "instagram.com", "ncbi.nlm.nih.gov"}

// FindCTA returns the link a landing page most wants clicked: the http(s)
// link that repeats most (advertorials repeat their offer link), or a
// tracker click link (".../click"). Returns "" when no link repeats.
func FindCTA(body []byte, pageURL string) string {
	base, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	counts := map[string]int{}
	var order []string
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		if string(name) != "a" || !hasAttr {
			continue
		}
		for {
			k, v, more := z.TagAttr()
			if string(k) == "href" {
				if link := ctaLink(base, string(v)); link != "" {
					if counts[link] == 0 {
						order = append(order, link)
					}
					counts[link]++
				}
			}
			if !more {
				break
			}
		}
	}
	best, bestN := "", 0
	for _, l := range order {
		n := counts[l]
		if u, err := url.Parse(l); err == nil && rxClickPath.MatchString(u.Path) {
			n += 2 // tracker click links (RedTrack, Voluum, ...) are the offer button
		}
		if n > bestN {
			best, bestN = l, n
		}
	}
	if bestN < 2 {
		return ""
	}
	return best
}

// RedTrack's click id: 24 hex digits the tracker gives each visit, carried on
// the lander as rtkcid.
var rxRedTrackClickID = regexp.MustCompile(`rtkcid=([0-9a-fA-F]{24})\b`)

// RedTrack click links: /click, /click/2, /preclick.
var rxRedTrackClickPath = regexp.MustCompile(`(?i)/(pre)?click(/\d+)?/?$`)

// WithClickID completes a tracker click link the way the page's scripts do in
// a browser. A RedTrack lander adds ?clickid=<rtkcid> to its click links from
// a script; a fetch runs no scripts, and RedTrack answers the bare link with
// {"status":0,"message":"empty clickid value"} instead of the next page
// (everviewjournal.com, 2026-09-26). The click id comes from the page address,
// else from the page itself (a <base href> or a script). Any other link comes
// back unchanged.
func WithClickID(link string, body []byte, pageURL string) string {
	u, err := url.Parse(link)
	if err != nil || !rxRedTrackClickPath.MatchString(u.Path) {
		return link
	}
	q := u.Query()
	if v := q.Get("clickid"); v != "" && !strings.HasPrefix(v, "{") {
		return link
	}
	id := ""
	if m := rxRedTrackClickID.FindStringSubmatch(pageURL); m != nil {
		id = m[1]
	} else if m := rxRedTrackClickID.FindSubmatch(body); m != nil {
		id = string(m[1])
	}
	if id == "" {
		return link
	}
	q.Set("clickid", id)
	u.RawQuery = q.Encode()
	return u.String()
}

// ctaLink resolves one href, or returns "" when it cannot be the offer button.
func ctaLink(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	u := base.ResolveReference(ref)
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	u.Fragment = ""
	// The page linking to itself (anchors, "read more") is not a step.
	if u.Host == base.Host && u.Path == base.Path {
		return ""
	}
	lower := strings.ToLower(u.Host + u.Path)
	for _, s := range ctaSkip {
		if strings.Contains(lower, s) {
			return ""
		}
	}
	return u.String()
}
