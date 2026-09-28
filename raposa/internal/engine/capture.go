package engine

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"adhunters/collector/internal/funnel"
	"adhunters/collector/internal/model"
	"github.com/google/uuid"
	"golang.org/x/net/html"
)

var (
	// The Cloudflare beacon carries a ray id and a token that change on every
	// load. Left in, the same page would look new every visit.
	rxCFBeacon = regexp.MustCompile(`(?is)<script[^>]*(?:cloudflareinsights\.com|data-cf-beacon)[^>]*>.*?</script>`)
	rxCFToken  = regexp.MustCompile(`(?i)"(?:rayId|token|version)"\s*:\s*"[^"]*"`)
	// A cache buster in a URL: ?v=, ?ver=, ?_=, ?t=, ?cb= and their friends.
	rxCacheBust = regexp.MustCompile(`(?i)([?&](?:v|ver|_|t|ts|cb|cache|cachebuster|rnd|r|nocache|nonce)=)[A-Za-z0-9._%-]+`)
	// A script nonce is minted per response.
	rxNonce = regexp.MustCompile(`(?i)\snonce="[^"]*"`)
	// Comments servers stamp with a time or a hit counter.
	rxHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	// Cloudflare's email obfuscation encodes each address with a new key on
	// every load (everviewjournal.com's white page never hashed the same twice).
	rxCFEmail = regexp.MustCompile(`(?i)(/cdn-cgi/l/email-protection#|data-cfemail=")[0-9a-f]+`)
)

// normaliseHTML drops the parts of a page that change on every load, so the
// same page keeps the same content hash.
func normaliseHTML(raw string) string {
	s := rxCFBeacon.ReplaceAllString(raw, "")
	s = rxCFToken.ReplaceAllString(s, `""`)
	s = rxHTMLComment.ReplaceAllString(s, "")
	s = rxNonce.ReplaceAllString(s, "")
	s = rxCFEmail.ReplaceAllString(s, "${1}x")
	s = rxCacheBust.ReplaceAllString(s, "${1}x")
	return strings.Join(strings.Fields(s), " ")
}

// contentHash is the md5 of the normalised HTML, as a uuid, which is what
// spy.raposa_page.content_hash holds.
func contentHash(raw string) uuid.UUID {
	sum := md5.Sum([]byte(normaliseHTML(raw)))
	return uuid.UUID(sum)
}

// pageKind maps the funnel walker's page type onto the words
// spy.raposa_page.page_kind uses.
func pageKind(status int, title, htmlContent, pageURL string, checkout funnel.CheckoutInfo) string {
	if isErrorPage(status, title) {
		return "error"
	}
	switch funnel.DetectPageType(htmlContent, pageURL, checkout) {
	case "CHECKOUT":
		return "checkout"
	case "VSL":
		return "vsl"
	case "ADVERTORIAL":
		return "advertorial"
	case "QUIZ_SURVEY":
		return "quiz"
	case "LEAD_MAGNET", "DIRECT_LANDER":
		return "article"
	default:
		return "unknown"
	}
}

// maxOutboundLinks caps the links kept per capture. Enough to find the next
// step again later without storing a site map.
const maxOutboundLinks = 200

// capture turns one step of one visit into a spy.raposa_page row and the files
// that page loads.
func capture(step VisitStep, maxPageBytes, maxAssetBytes int) (model.RaposaPage, []model.RaposaAsset) {
	body := []byte(step.HTML)
	dom := funnel.ParseDOM(body, step.URL)
	checkout := funnel.DetectCheckout(step.HTML, step.URL)

	title := step.Title
	if title == "" {
		title = dom.Title
	}
	bodyText := step.Text
	if bodyText == "" {
		bodyText = dom.BodyText
	}

	host, path := "", "/"
	if u, err := url.Parse(step.URL); err == nil {
		host = strings.ToLower(u.Hostname())
		if u.Path != "" {
			path = u.Path
		}
	}

	page := model.RaposaPage{
		ContentHash:        contentHash(step.HTML),
		URL:                step.URL,
		Host:               host,
		Path:               path,
		Title:              title,
		PageKind:           pageKind(step.Status, title, step.HTML, step.URL, checkout),
		WordCount:          dom.WordCount,
		HTML:               truncate(step.HTML, maxPageBytes),
		BodyText:           bodyText,
		HTMLBytes:          len(step.HTML),
		Headings:           dom.Headings,
		MetaTags:           dom.MetaTags,
		Pixels:             funnel.ExtractPixels(step.HTML),
		CheckoutPlatform:   checkout.Platform,
		CheckoutMerchantID: checkout.MerchantID,
		OutboundLinks:      outboundLinks(body, step.URL),
	}

	assets := make([]model.RaposaAsset, 0, len(step.Assets))
	for _, a := range step.Assets {
		assets = append(assets, captureAsset(a, maxAssetBytes))
	}
	return page, assets
}

// captureAsset turns one file the page loaded into a spy.raposa_asset row. A
// file that was too large keeps its row with no bytes and a skipped reason.
func captureAsset(a VisitAsset, maxAssetBytes int) model.RaposaAsset {
	raw := a.Body
	if maxAssetBytes > 0 && len(raw) > maxAssetBytes {
		raw = nil
		a.SkippedReason = fmt.Sprintf("larger than the %d byte limit", maxAssetBytes)
	}
	asset := model.RaposaAsset{
		MediaType:     a.MediaType,
		Role:          assetRole(a.Role, a.URL, a.MediaType),
		SourceURL:     a.URL,
		SkippedReason: a.SkippedReason,
	}
	if asset.MediaType == "" {
		asset.MediaType = "application/octet-stream"
	}
	if asset.SkippedReason != "" || len(raw) == 0 {
		// No bytes to hash, so the URL identifies the row.
		sum := md5.Sum([]byte(a.URL))
		asset.ContentHash = uuid.UUID(sum)
		if asset.SkippedReason == "" {
			asset.SkippedReason = "no bytes returned"
		}
		return asset
	}
	sum := md5.Sum(raw)
	asset.ContentHash = uuid.UUID(sum)
	asset.SizeBytes = len(raw)
	asset.Bytes = raw
	return asset
}

// assetRole settles on one of the words spy.raposa_page_asset.role takes.
func assetRole(role, rawURL, mediaType string) string {
	switch role {
	case "image", "video", "stylesheet", "script", "font":
		return role
	}
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return "image"
	case strings.HasPrefix(mediaType, "video/"), strings.HasPrefix(mediaType, "audio/"):
		return "video"
	case strings.HasPrefix(mediaType, "font/"), strings.Contains(mediaType, "font"):
		return "font"
	case strings.Contains(mediaType, "css"):
		return "stylesheet"
	case strings.Contains(mediaType, "javascript"):
		return "script"
	}
	switch strings.ToLower(pathExt(rawURL)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".avif", ".ico":
		return "image"
	case ".mp4", ".webm", ".m3u8", ".mov", ".mp3":
		return "video"
	case ".css":
		return "stylesheet"
	case ".js", ".mjs":
		return "script"
	case ".woff", ".woff2", ".ttf", ".otf", ".eot":
		return "font"
	}
	return "other"
}

func pathExt(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if i := strings.LastIndex(u.Path, "."); i >= 0 {
		return u.Path[i:]
	}
	return ""
}

// outboundLinks is where the page can send a reader, so the next step of a
// funnel can be found again later.
func outboundLinks(body []byte, pageURL string) []string {
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var links []string
	z := html.NewTokenizer(bytes.NewReader(body))
	for len(links) < maxOutboundLinks {
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
				if link := absoluteLink(base, string(v)); link != "" && !seen[link] {
					seen[link] = true
					links = append(links, link)
				}
			}
			if !more {
				break
			}
		}
	}
	return links
}

// absoluteLink resolves one href against the page, or returns "" when it is
// not a link a reader can follow.
func absoluteLink(base *url.URL, href string) string {
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
	return u.String()
}

// truncate cuts a string at n bytes, on a rune boundary.
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
