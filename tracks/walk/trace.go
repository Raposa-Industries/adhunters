package walk

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"github.com/Raposa-Industries/adhunters/shared/page"
)

// MaxBody is how much of a page is read, as the collector did.
const MaxBody = 500 << 10

const maxHops = 10

// The collector's desktop browsers (internal/proxy/transport.go).
var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:125.0) Gecko/20100101 Firefox/125.0",
}

// Trace walks one link: the landing page, and one step through its main
// button with the same cookies. PageTimeout bounds each page, redirects
// included.
func Trace(ctx context.Context, rt http.RoundTripper, link, referer string, pageTimeout time.Duration) []Page {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{
		Transport: rt,
		Jar:       jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // each hop is read by hand
		},
	}
	ua := userAgents[rand.IntN(len(userAgents))]
	landing := fetch(ctx, c, ua, link, referer, pageTimeout)
	landing.Step = 0
	pages := []Page{landing}
	if landing.Status < 200 || landing.Status >= 300 || landing.FinalURL == "" {
		return pages
	}
	body := landing.BodyBytes()
	cta := page.FindCTA(body, landing.FinalURL)
	if cta == "" {
		return pages
	}
	cta = page.WithClickID(cta, body, landing.FinalURL)
	step := fetch(ctx, c, ua, cta, landing.FinalURL, pageTimeout)
	step.Step = 1
	return append(pages, step)
}

// fetch follows redirects from start, one hop at a time.
func fetch(ctx context.Context, c *http.Client, ua, start, referer string, timeout time.Duration) Page {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p := Page{URL: start, Referer: referer}
	cur, ref := start, referer
	for range maxHops {
		t0 := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cur, nil)
		if err != nil {
			p.Error = err.Error()
			return p
		}
		setHeaders(req, ua, ref)
		res, err := c.Do(req)
		if err != nil {
			p.Error = err.Error()
			return p
		}
		hop := Hop{URL: cur, Status: res.StatusCode, LatencyMS: time.Since(t0).Milliseconds(), Server: res.Header.Get("Server")}
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			loc := res.Header.Get("Location")
			hop.Location = loc
			p.Hops = append(p.Hops, hop)
			_ = res.Body.Close()
			next, err := resolve(cur, loc)
			if loc == "" || err != nil {
				// A redirect nowhere is the end of the walk.
				p.FinalURL, p.Status = cur, res.StatusCode
				p.setHeaders(res.Header)
				return p
			}
			ref, cur = cur, next
			continue
		}
		p.Hops = append(p.Hops, hop)
		b, err := io.ReadAll(io.LimitReader(res.Body, MaxBody+1))
		_ = res.Body.Close()
		p.FinalURL, p.Status = cur, res.StatusCode
		p.setHeaders(res.Header)
		if len(b) > MaxBody {
			b, p.Truncated = b[:MaxBody], true
		}
		p.setBody(b)
		if err != nil {
			p.Error = "reading the page: " + err.Error()
		}
		return p
	}
	p.Error = errors.New("more than 10 redirects").Error()
	return p
}

// The collector's headers: a Chrome 124 on Windows arriving from the
// publisher's page.
func setHeaders(req *http.Request, ua, referer string) {
	h := req.Header
	h.Set("User-Agent", ua)
	h.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Sec-Ch-Ua", `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`)
	h.Set("Sec-Ch-Ua-Mobile", "?0")
	h.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	h.Set("Sec-Fetch-Dest", "document")
	h.Set("Sec-Fetch-Mode", "navigate")
	h.Set("Sec-Fetch-Site", "cross-site")
	h.Set("Sec-Fetch-User", "?1")
	h.Set("Upgrade-Insecure-Requests", "1")
	if referer != "" {
		h.Set("Referer", referer)
	}
}

func resolve(base, loc string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	l, err := url.Parse(loc)
	if err != nil {
		return "", err
	}
	u := b.ResolveReference(l)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("not a web address")
	}
	return u.String(), nil
}
