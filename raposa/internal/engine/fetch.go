package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"adhunters/collector/internal/funnel"
	"adhunters/collector/internal/model"
	"adhunters/collector/internal/proxy"
	"adhunters/collector/internal/sweeper"
	"golang.org/x/net/html"
)

// Fetcher is the plain HTTP visit. It was enough on 5 of the 7 sites the tests
// of 2026-09-25 and 2026-09-26 looked at, and it costs almost nothing. The
// browser runner is for the stronger cloakers, and for capturing what scripts
// build.
type Fetcher struct {
	timeout time.Duration
}

func NewFetcher() *Fetcher {
	return &Fetcher{timeout: 15 * time.Second}
}

// maxFetchHops is the redirect budget of one step, the same one
// funnel/tracer.go keeps.
const maxFetchHops = 10

// maxFetchBody is how much HTML one step reads.
const maxFetchBody = 2 * 1024 * 1024

// maxFetchAssets caps the files one step downloads when the disguise asks for
// them, so a page full of images cannot run away with the metered budget.
const maxFetchAssets = 40

// Visit loads the landing page and follows the funnel as far as the plan
// allows, in one session. It returns the same steps the browser runner does.
func (f *Fetcher) Visit(ctx context.Context, p visitPlan) (*VisitResult, error) {
	if p.URL == "" {
		return nil, fmt.Errorf("no link to visit")
	}

	transport := proxy.CreateDirectTransport()
	if p.Line != nil && p.Line.Host != "" {
		tr, err := proxy.CreateTunnelTransport(*p.Line)
		if err != nil {
			return nil, fmt.Errorf("proxy line %s: %w", p.Line.Key, err)
		}
		transport = tr
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	client := &http.Client{
		Transport: transport,
		Jar:       jar,
		Timeout:   f.timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // every hop is followed by hand
		},
	}

	start := time.Now()
	res := &VisitResult{OK: true}

	step, bytesRead, err := f.walk(ctx, client, p, p.URL, p.Referer)
	if err != nil {
		return nil, err
	}
	step.StepNo = 1
	step.ReachedBy = "landing"
	if len(step.Hops) > 1 {
		step.ReachedBy = "redirect"
	}
	res.Bytes += bytesRead
	res.Steps = append(res.Steps, *step)

	// The funnel past the landing page, in the same session, so a tracker that
	// set a cookie on the first page still recognises the visit.
	current := *step
	for n := 0; n < p.MaxSteps; n++ {
		cta := funnel.FindCTA([]byte(current.HTML), current.URL)
		if cta == "" {
			break
		}
		clicked := funnel.WithClickID(cta, []byte(current.HTML), current.URL)
		next, read, err := f.walk(ctx, client, p, clicked, current.URL)
		res.Bytes += read
		if err != nil {
			break
		}
		next.StepNo = len(res.Steps) + 1
		next.ReachedBy = "cta"
		next.ClickedURL = clicked
		next.ClickedText = linkText([]byte(current.HTML), cta)
		res.Steps = append(res.Steps, *next)
		current = *next
	}

	res.DurationMs = int(time.Since(start).Milliseconds())
	return res, nil
}

// walk follows the redirects from startURL one hop at a time and reads the
// page it ends on.
func (f *Fetcher) walk(ctx context.Context, client *http.Client, p visitPlan, startURL, referer string) (*VisitStep, int, error) {
	// Cleaned as a browser would: a link or a call to action can carry a line
	// break, which a browser drops and Go's URL parser refuses.
	currURL := sweeper.BrowserURL(startURL)
	activeReferer := referer
	read := 0
	var hops []VisitHop

	for hop := 0; hop < maxFetchHops; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, currURL, nil)
		if err != nil {
			return nil, read, fmt.Errorf("create hop request: %w", err)
		}
		setBrowserHeaders(req, p.Device, activeReferer)

		resp, err := client.Do(req)
		if err != nil {
			return nil, read, fmt.Errorf("hop %d to %s: %w", hop, currURL, err)
		}
		hops = append(hops, VisitHop{URL: currURL, Status: resp.StatusCode})

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			resp.Body.Close()
			if loc == "" {
				break
			}
			next, err := resolveLocation(currURL, loc)
			if err != nil {
				break
			}
			activeReferer = currURL
			currURL = next
			continue
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBody))
		resp.Body.Close()
		read += len(body)
		if err != nil {
			return nil, read, fmt.Errorf("read %s: %w", currURL, err)
		}

		// A page whose only job is to send the reader on: a meta refresh, or a
		// tiny page that sets location in a script. A browser follows it on
		// its own, so the fetch follows it too, as one more hop. One cloaker
		// served real readers exactly this, pointing at its tracker, while the
		// reviewer got the whole white page.
		if next := clientRedirect(body, currURL); next != "" && next != currURL {
			activeReferer = currURL
			currURL = next
			continue
		}

		dom := funnel.ParseDOM(body, currURL)
		step := &VisitStep{
			URL:    currURL,
			Status: resp.StatusCode,
			Title:  dom.Title,
			HTML:   funnel.SanitizeUTF8(string(body)),
			Text:   dom.BodyText,
			Hops:   hops,
		}
		if p.LoadAssets {
			assets, assetBytes := f.loadAssets(ctx, client, p, step)
			step.Assets = assets
			read += assetBytes
		}
		return step, read, nil
	}

	return nil, read, fmt.Errorf("more than %d redirects from %s", maxFetchHops, startURL)
}

// loadAssets downloads the files a page references, up to maxFetchAssets. A
// file over the limit keeps its row with no bytes and a skipped reason.
func (f *Fetcher) loadAssets(ctx context.Context, client *http.Client, p visitPlan, step *VisitStep) ([]VisitAsset, int) {
	refs := assetRefs([]byte(step.HTML), step.URL)
	if len(refs) > maxFetchAssets {
		refs = refs[:maxFetchAssets]
	}

	read := 0
	out := make([]VisitAsset, 0, len(refs))
	for _, ref := range refs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.URL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", proxy.GetUserAgent(p.Device))
		req.Header.Set("Referer", step.URL)

		resp, err := client.Do(req)
		if err != nil {
			out = append(out, VisitAsset{URL: ref.URL, Role: ref.Role, SkippedReason: err.Error()})
			continue
		}
		limit := int64(p.MaxAssetBytes) + 1
		raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		mediaType := resp.Header.Get("Content-Type")
		resp.Body.Close()
		read += len(raw)
		if err != nil {
			out = append(out, VisitAsset{URL: ref.URL, Role: ref.Role, MediaType: mediaType, SkippedReason: err.Error()})
			continue
		}
		if len(raw) > p.MaxAssetBytes {
			out = append(out, VisitAsset{URL: ref.URL, Role: ref.Role, MediaType: mediaType,
				SkippedReason: fmt.Sprintf("larger than the %d byte limit", p.MaxAssetBytes)})
			continue
		}
		out = append(out, VisitAsset{URL: ref.URL, Role: ref.Role, MediaType: mediaType, Body: raw})
	}
	return out, read
}

// FetchAssets downloads the files one already captured page references,
// through the given line, for the asset pass at the end of an investigation.
func (f *Fetcher) FetchAssets(ctx context.Context, line *model.ProxyLine, device, pageURL, pageHTML string, maxAssetBytes int) ([]VisitAsset, int) {
	transport := proxy.CreateDirectTransport()
	if line != nil && line.Host != "" {
		tr, err := proxy.CreateTunnelTransport(*line)
		if err != nil {
			return nil, 0
		}
		transport = tr
	}
	client := &http.Client{Transport: transport, Timeout: f.timeout}
	p := visitPlan{Device: device, MaxAssetBytes: maxAssetBytes}
	return f.loadAssets(ctx, client, p, &VisitStep{URL: pageURL, HTML: pageHTML})
}

// assetRef is one file a page points at.
type assetRef struct {
	URL  string
	Role string
}

// assetRefs reads the files a page loads out of its HTML: images, stylesheets,
// scripts and video.
func assetRefs(body []byte, pageURL string) []assetRef {
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var refs []assetRef

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
		if !hasAttr {
			continue
		}

		tag := string(name)
		role := ""
		switch tag {
		case "img":
			role = "image"
		case "script":
			role = "script"
		case "video", "source", "audio":
			role = "video"
		case "link":
			role = "stylesheet"
		default:
			continue
		}

		var href, rel string
		for {
			k, v, more := z.TagAttr()
			switch string(k) {
			case "src", "href":
				href = string(v)
			case "rel":
				rel = strings.ToLower(string(v))
			}
			if !more {
				break
			}
		}
		if tag == "link" && !strings.Contains(rel, "stylesheet") && !strings.Contains(rel, "icon") {
			continue
		}
		if tag == "link" && strings.Contains(rel, "icon") {
			role = "image"
		}

		link := absoluteLink(base, href)
		if link == "" || seen[link] {
			continue
		}
		seen[link] = true
		refs = append(refs, assetRef{URL: link, Role: role})
	}
	return refs
}

// linkText is the words on the link the visit clicked, so a funnel step says
// what a reader would have pressed.
func linkText(body []byte, target string) string {
	z := html.NewTokenizer(bytes.NewReader(body))
	inLink := false
	var text strings.Builder
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return ""
		}
		switch tt {
		case html.StartTagToken:
			name, hasAttr := z.TagName()
			if string(name) != "a" || !hasAttr {
				continue
			}
			for {
				k, v, more := z.TagAttr()
				if string(k) == "href" && sameLink(string(v), target) {
					inLink = true
					text.Reset()
				}
				if !more {
					break
				}
			}
		case html.TextToken:
			if inLink {
				text.Write(z.Text())
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == "a" && inLink {
				inLink = false
				if s := strings.Join(strings.Fields(text.String()), " "); s != "" {
					return funnel.SanitizeUTF8(s)
				}
			}
		}
	}
}

// sameLink says an href points at the same place as the link that was clicked.
func sameLink(href, target string) bool {
	href = strings.TrimSpace(href)
	return href != "" && (href == target || strings.HasSuffix(target, href))
}

// The two browsers a fetch visit claims to be, one per device, the same on
// every hop of the visit. The phone is Safari on an iPhone: everviewjournal.com
// served its dark page to iPhone Safari and Chrome on iOS, never to Android
// Chrome or a desktop (tested 2026-09-26), and Safari sends no client hints.
const (
	fetchDesktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	fetchPhoneUA   = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Mobile/15E148 Safari/604.1"
)

// setBrowserHeaders sends what the browser the visit claims to be sends on a
// page load: desktop Chrome with its client hints, or iPhone Safari without.
//
// A landing reached from a Taboola card is loaded by the script on Taboola's
// click page (location.replace), not by the reader's own click, so a browser
// sends no Sec-Fetch-User on it. everviewjournal.com served the white page to
// every request that carried Sec-Fetch-User: ?1 with Taboola's referer, and
// the dark page to the same request without it.
func setBrowserHeaders(req *http.Request, device, referer string) {
	scripted := isTaboolaClick(referer)
	if device == "phone" {
		req.Header.Set("User-Agent", fetchPhoneUA)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	} else {
		req.Header.Set("User-Agent", fetchDesktopUA)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
		req.Header.Set("Sec-Ch-Ua", `"Chromium";v="140", "Not=A?Brand";v="24", "Google Chrome";v="140"`)
		req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
		req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	}
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if !scripted {
		req.Header.Set("Sec-Fetch-User", "?1")
	}
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
}

// isTaboolaClick says a referer is Taboola's click page.
func isTaboolaClick(referer string) bool {
	u, err := url.Parse(referer)
	return err == nil && strings.EqualFold(u.Hostname(), "trc.taboola.com") && strings.Contains(u.Path, "/log/3/click")
}

func resolveLocation(currentURL, location string) (string, error) {
	u, err := url.Parse(currentURL)
	if err != nil {
		return location, err
	}
	loc, err := url.Parse(location)
	if err != nil {
		return location, err
	}
	return u.ResolveReference(loc).String(), nil
}

var (
	refreshURLRE = regexp.MustCompile(`(?i)^\s*(\d+(?:\.\d+)?)\s*[;,]\s*url\s*=\s*['"]?([^'"]+)['"]?\s*$`)
	jsLocationRE = regexp.MustCompile(`(?is)(?:window\.|document\.|top\.|self\.)?location(?:\.href)?\s*=\s*["']([^"']+)["']|location\.(?:replace|assign)\(\s*["']([^"']+)["']\s*\)`)
)

// maxRefreshDelay is the longest meta refresh still treated as a redirect.
// A page that refreshes after longer is a page a reader reads first.
const maxRefreshDelay = 5.0

// maxScriptRedirectPage is how small a page must be for a location set in a
// script to count as a redirect. A full page that sets location somewhere in
// its scripts is a page, not a hop.
const maxScriptRedirectPage = 6 * 1024

// clientRedirect returns where a page sends the reader without a click, or
// "" when it does not. The meta refresh is read with the HTML tokenizer, so a
// refresh tag quoted inside a script or a comment does not count: one
// tracker's page carries exactly such a comment in its URL rewriting script.
func clientRedirect(body []byte, pageURL string) string {
	if content := metaRefreshContent(body); content != "" {
		if r := refreshURLRE.FindStringSubmatch(content); r != nil {
			var delay float64
			fmt.Sscanf(r[1], "%g", &delay)
			if delay <= maxRefreshDelay {
				if next, err := resolveLocation(pageURL, strings.TrimSpace(r[2])); err == nil {
					return next
				}
			}
		}
	}
	if len(body) <= maxScriptRedirectPage {
		if m := jsLocationRE.FindSubmatch(body); m != nil {
			target := string(m[1])
			if target == "" {
				target = string(m[2])
			}
			// Only a real address: a script that builds its URL leaves a
			// placeholder or a fragment here, and following it lands on a 404.
			if !strings.HasPrefix(target, "http") && !strings.HasPrefix(target, "/") {
				return ""
			}
			if strings.Contains(target, "...") || strings.Contains(target, "${") {
				return ""
			}
			if next, err := resolveLocation(pageURL, target); err == nil {
				return next
			}
		}
	}
	return ""
}

// metaRefreshContent is the content of the page's first real
// <meta http-equiv="refresh">, or "".
func metaRefreshContent(body []byte) string {
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			if string(name) != "meta" || !hasAttr {
				continue
			}
			var equiv, content string
			for {
				k, v, more := z.TagAttr()
				switch strings.ToLower(string(k)) {
				case "http-equiv":
					equiv = strings.ToLower(strings.TrimSpace(string(v)))
				case "content":
					content = string(v)
				}
				if !more {
					break
				}
			}
			if equiv == "refresh" {
				return content
			}
		}
	}
}
