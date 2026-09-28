package engine

import (
	"testing"

	"github.com/google/uuid"
)

// whitePage is the harmless article an ad network reviewer sees. The ray id
// and the cache buster change on every load, and must not make the same page
// look new.
func whitePage(rayID, cacheBuster string) string {
	return `<!DOCTYPE html><html><head>` +
		`<title>7 Morning Habits of Healthy People</title>` +
		`<link rel="stylesheet" href="/assets/app.css?v=` + cacheBuster + `">` +
		`<script>window.__cfRay = {"rayId":"` + rayID + `"};</script>` +
		`</head><body>` +
		`<h1>7 Morning Habits of Healthy People</h1>` +
		`<p>A short piece about breakfast, walking and sleep.</p>` +
		`<!-- rendered ` + cacheBuster + ` -->` +
		`<script defer src="https://static.cloudflareinsights.com/beacon.min.js/vcd15cbe" ` +
		`data-cf-beacon='{"rayId":"` + rayID + `","version":"2024.11.0","token":"` + rayID + `"}'>` +
		`</script></body></html>`
}

// darkPage is the advertorial the operator shows a real user.
const darkPage = `<!DOCTYPE html><html><head><title>She Fixed Her Ringing Ears In 3 Weeks</title></head>` +
	`<body><h1>She Fixed Her Ringing Ears In 3 Weeks</h1>` +
	`<p>Watch the presentation before it is taken down.</p>` +
	`<a href="https://checkout.example.com/order">Watch the presentation</a></body></html>`

func TestNormaliseHTMLIgnoresPerLoadNoise(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		same bool
	}{
		{"a different Cloudflare beacon token", whitePage("8f1a2b", "1001"), whitePage("9c3d4e", "1001"), true},
		{"a different cache buster", whitePage("8f1a2b", "1001"), whitePage("8f1a2b", "2002"), true},
		{"both change at once", whitePage("8f1a2b", "1001"), whitePage("9c3d4e", "2002"), true},
		{"only whitespace changed", whitePage("8f1a2b", "1001"), "  " + whitePage("8f1a2b", "1001") + "\n", true},
		{"a different page", whitePage("8f1a2b", "1001"), darkPage, false},
		{"a different Cloudflare email key",
			`<a href="/cdn-cgi/l/email-protection#493a3c39"><span class="__cf_email__" data-cfemail="abd8dedb">[email protected]</span></a>`,
			`<a href="/cdn-cgi/l/email-protection#61121411"><span class="__cf_email__" data-cfemail="c9babcb9">[email protected]</span></a>`, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := contentHash(c.a) == contentHash(c.b)
			if got != c.same {
				t.Fatalf("same hash = %v, want %v\n a: %s\n b: %s",
					got, c.same, normaliseHTML(c.a), normaliseHTML(c.b))
			}
		})
	}
}

func TestIsErrorPage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		title  string
		want   bool
	}{
		{"a page that loaded", 200, "7 Morning Habits of Healthy People", false},
		{"a bad gateway", 502, "502 Bad Gateway", true},
		{"a 200 that is really an error", 200, "502 Bad Gateway", true},
		{"cloudflare cannot reach the origin", 200, "Origin is unreachable", true},
		{"a not found page is a page", 404, "Page not found", false},
		{"no title at all", 200, "", false},
		{"a server error with no title", 500, "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isErrorPage(c.status, c.title); got != c.want {
				t.Fatalf("isErrorPage(%d, %q) = %v, want %v", c.status, c.title, got, c.want)
			}
		})
	}
}

func TestRegistrableDomain(t *testing.T) {
	cases := []struct {
		host, want string
	}{
		{"heliopulse.site", "heliopulse.site"},
		{"www.heliopulse.site", "heliopulse.site"},
		{"go.track.nourishlifeclub.com", "nourishlifeclub.com"},
		{"shop.example.co.uk", "example.co.uk"},
		{"example.co.uk", "example.co.uk"},
		{"localhost", "localhost"},
		{"", ""},
	}

	for _, c := range cases {
		t.Run(c.host, func(t *testing.T) {
			if got := registrableDomain(c.host); got != c.want {
				t.Fatalf("registrableDomain(%q) = %q, want %q", c.host, got, c.want)
			}
		})
	}
}

func TestJudge(t *testing.T) {
	white := landing{
		URL:    "https://heliopulse.site/article/morning-habits",
		Status: 200,
		Title:  "7 Morning Habits of Healthy People",
		Hash:   contentHash(whitePage("8f1a2b", "1001")),
	}

	cases := []struct {
		name string
		got  landing
		want string
	}{
		{
			name: "the same page, a fresh beacon token and cache buster",
			got: landing{URL: white.URL, Status: 200, Title: white.Title,
				Hash: contentHash(whitePage("9c3d4e", "2002"))},
			want: "white",
		},
		{
			name: "a different page on the same domain",
			got: landing{URL: "https://heliopulse.site/offer/tinnitus", Status: 200,
				Title: "She Fixed Her Ringing Ears In 3 Weeks", Hash: contentHash(darkPage)},
			want: "dark",
		},
		{
			name: "a redirect to another domain",
			got: landing{URL: "https://nourishlifeclub.com/vsl", Status: 200,
				Title: "She Fixed Her Ringing Ears In 3 Weeks", Hash: contentHash(darkPage)},
			want: "dark",
		},
		{
			name: "another domain serving the very same page is still dark",
			got: landing{URL: "https://nourishlifeclub.com/article/morning-habits", Status: 200,
				Title: white.Title, Hash: white.Hash},
			want: "dark",
		},
		{
			name: "a subdomain of the same registrable domain is not a domain change",
			got: landing{URL: "https://go.heliopulse.site/article/morning-habits", Status: 200,
				Title: white.Title, Hash: white.Hash},
			want: "white",
		},
		{
			name: "the origin was down",
			got: landing{URL: "https://heliopulse.site/article/morning-habits", Status: 502,
				Title: "502 Bad Gateway", Hash: contentHash("<html><title>502 Bad Gateway</title></html>")},
			want: "error",
		},
		{
			name: "a 200 that only says the origin is unreachable",
			got: landing{URL: "https://heliopulse.site/article/morning-habits", Status: 200,
				Title: "heliopulse.site | Origin is unreachable",
				Hash:  contentHash("<html><title>Origin is unreachable</title></html>")},
			want: "error",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := judge(white, c.got, true); got.Outcome != c.want {
				t.Fatalf("judge outcome = %q (%s), want %q", got.Outcome, got.Reason, c.want)
			}
		})
	}
}

func TestJudgeWithoutWhitePage(t *testing.T) {
	got := judge(landing{}, landing{URL: "https://heliopulse.site/", Status: 200, Hash: uuid.New()}, true)
	if got.Outcome != "error" {
		t.Fatalf("outcome = %q, want error when there is no white page", got.Outcome)
	}
}

// A white page that does not hash the same twice in a row says nothing through
// its HTML, so only the domain and the title may call a visit dark.
func TestJudgeWhenTheWhitePageIsNotStable(t *testing.T) {
	white := landing{
		URL:    "https://heliopulse.site/article/morning-habits",
		Status: 200,
		Title:  "7 Morning Habits of Healthy People",
		Hash:   contentHash(whitePage("8f1a2b", "1001")),
	}

	cases := []struct {
		name string
		got  landing
		want string
	}{
		{
			name: "the same page with HTML we cannot compare",
			got: landing{URL: white.URL, Status: 200, Title: "7 Morning  Habits of HEALTHY People",
				Hash: contentHash(darkPage)},
			want: "white",
		},
		{
			name: "a different title on the same domain",
			got: landing{URL: "https://heliopulse.site/offer/tinnitus", Status: 200,
				Title: "She Fixed Her Ringing Ears In 3 Weeks", Hash: contentHash(darkPage)},
			want: "dark",
		},
		{
			name: "another domain is still dark whatever the title",
			got: landing{URL: "https://nourishlifeclub.com/vsl", Status: 200,
				Title: white.Title, Hash: white.Hash},
			want: "dark",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := judge(white, c.got, false); got.Outcome != c.want {
				t.Fatalf("judge outcome = %q (%s), want %q", got.Outcome, got.Reason, c.want)
			}
		})
	}
}

// The white page is fetched; a browser rung hands back the DOM after its
// scripts ran, which never hashes like the fetched body. Before the engine was
// compared, every browser rung read as dark on that difference alone.
func TestJudgeAcrossEngines(t *testing.T) {
	white := landing{
		URL:    "https://nourishlifeclub.com/nourish-life-insights/",
		Status: 200,
		Title:  "Common Medications and Cognitive Wellness",
		Hash:   contentHash(whitePage("8f1a2b", "1001")),
		Engine: "fetch",
	}

	cases := []struct {
		name string
		got  landing
		want string
	}{
		{
			name: "the same page rendered by a browser is white",
			got: landing{URL: white.URL, Status: 200, Title: white.Title,
				Hash: contentHash(darkPage), Engine: "browser"},
			want: "white",
		},
		{
			name: "the same page with index.html and no trailing slash is white",
			got: landing{URL: "https://nourishlifeclub.com/nourish-life-insights/index.html", Status: 200,
				Title: white.Title, Hash: contentHash(darkPage), Engine: "browser"},
			want: "white",
		},
		{
			name: "a different title in the browser is dark",
			got: landing{URL: white.URL, Status: 200, Title: "Limited-time presentation",
				Hash: contentHash(darkPage), Engine: "browser"},
			want: "dark",
		},
		{
			name: "a redirect to the operator's funnel domain is dark",
			got: landing{URL: "https://healthyhorizonclub.com/06/adv-ml-clntvck-int-v01/", Status: 200,
				Title: "Limited-time presentation", Hash: contentHash(darkPage), Engine: "browser"},
			want: "dark",
		},
		{
			name: "a different path on the same domain is dark",
			got: landing{URL: "https://nourishlifeclub.com/06/vsl/", Status: 200,
				Title: white.Title, Hash: contentHash(darkPage), Engine: "browser"},
			want: "dark",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := judge(white, c.got, true); got.Outcome != c.want {
				t.Fatalf("judge outcome = %q (%s), want %q", got.Outcome, got.Reason, c.want)
			}
		})
	}
}
