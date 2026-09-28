package engine

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// landing is the part of a landing page the comparison looks at.
type landing struct {
	URL    string
	Status int
	Title  string
	// Hash of the normalised HTML, so a different Cloudflare beacon token or
	// cache buster does not make the same page look new.
	Hash uuid.UUID
	// Which engine loaded it, fetch or browser. A plain fetch keeps the body
	// the server sent; a browser hands back the DOM after its scripts ran, so
	// the same page hashes differently on the two. The hash is only read when
	// both sides came from the same engine.
	Engine string
}

// verdict is what one visit saw.
type verdict struct {
	Outcome string // white, dark, error
	Reason  string
}

// errorTitles are the pages a server puts up when it cannot answer. An
// operator never serves one on purpose, so a visit that gets one saw nothing:
// it is an error, not a dark page.
var errorTitles = []string{
	"502 bad gateway",
	"503 service unavailable",
	"504 gateway time-out",
	"504 gateway timeout",
	"500 internal server error",
	"origin is unreachable",
	"web server is down",
	"connection timed out",
	"this site can't be reached",
	"error 1016",
}

// isErrorPage says a page is only an error: the server said 5xx, or the title
// is one of the pages a server puts up when it cannot answer.
func isErrorPage(status int, title string) bool {
	if status >= 500 {
		return true
	}
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return false
	}
	for _, e := range errorTitles {
		if strings.Contains(t, e) {
			return true
		}
	}
	return false
}

// twoLevelSuffixes are the public suffixes that take two labels, so
// example.co.uk is one registrable domain and not two.
var twoLevelSuffixes = map[string]bool{
	"co.uk": true, "org.uk": true, "me.uk": true, "ac.uk": true, "gov.uk": true,
	"com.au": true, "net.au": true, "org.au": true,
	"com.br": true, "com.mx": true, "com.ar": true,
	"co.jp": true, "co.nz": true, "co.za": true, "co.in": true, "com.sg": true,
}

// registrableDomain is the part of a host an operator buys: the last label,
// or the last two when the suffix takes two.
func registrableDomain(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return ""
	}
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	last2 := strings.Join(parts[len(parts)-2:], ".")
	if twoLevelSuffixes[last2] && len(parts) >= 3 {
		return strings.Join(parts[len(parts)-3:], ".")
	}
	return last2
}

// domainOf reads the registrable domain out of one URL.
func domainOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return registrableDomain(u.Hostname())
}

// sameTitle says two page titles name the same page, ignoring case and spacing.
func sameTitle(a, b string) bool {
	norm := func(s string) string {
		return strings.Join(strings.Fields(strings.ToLower(s)), " ")
	}
	return norm(a) == norm(b)
}

// orSlash names the site root when a path reads empty.
func orSlash(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

// pathOf reads the path of one URL, without a trailing slash or a trailing
// index file, so /offer/, /offer and /offer/index.html are the same page.
func pathOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	p := strings.ToLower(u.Path)
	for _, idx := range []string{"/index.html", "/index.htm", "/index.php"} {
		p = strings.TrimSuffix(p, idx)
	}
	return strings.TrimSuffix(p, "/")
}

// judge compares one visit's landing page against the white page. A visit is
// dark when it landed somewhere the reviewer never sees and the difference is
// real: a page that is only a server error says nothing, and a beacon token or
// a cache buster is already out of the hash.
//
// whiteStable says the reviewer baseline hashed the same twice in a row. When
// it did not, the page carries per-load noise the normaliser does not know
// about, so its HTML says nothing and only the domain and the title are read.
// Calling such a page dark on its HTML alone would be a verdict nobody
// measured.
func judge(white, got landing, whiteStable bool) verdict {
	if isErrorPage(got.Status, got.Title) {
		return verdict{Outcome: "error", Reason: fmt.Sprintf("the server answered %d (%s)", got.Status, got.Title)}
	}
	if white.Hash == uuid.Nil {
		return verdict{Outcome: "error", Reason: "no white page to compare against"}
	}

	whiteDomain, gotDomain := domainOf(white.URL), domainOf(got.URL)
	if whiteDomain != "" && gotDomain != "" && whiteDomain != gotDomain {
		return verdict{Outcome: "dark", Reason: fmt.Sprintf("landed on %s where the reviewer lands on %s", gotDomain, whiteDomain)}
	}
	if wp, gp := pathOf(white.URL), pathOf(got.URL); wp != gp {
		return verdict{Outcome: "dark", Reason: fmt.Sprintf("landed on %s where the reviewer lands on %s", orSlash(gp), orSlash(wp))}
	}
	// Across engines the HTML cannot be compared: the same page hashes
	// differently as a fetched body and as a browser's DOM. Only what both
	// engines agree on is read, and the reason says so.
	if white.Engine != "" && got.Engine != "" && white.Engine != got.Engine {
		if !sameTitle(white.Title, got.Title) {
			return verdict{Outcome: "dark", Reason: fmt.Sprintf("the page is titled %q where the reviewer sees %q", got.Title, white.Title)}
		}
		return verdict{Outcome: "white", Reason: fmt.Sprintf("the same domain, path and title the reviewer sees; the HTML is not compared because the reviewer page came from a %s and this visit from a %s", white.Engine, got.Engine)}
	}
	if !whiteStable {
		if !sameTitle(white.Title, got.Title) {
			return verdict{Outcome: "dark", Reason: fmt.Sprintf("the page is titled %q where the reviewer sees %q", got.Title, white.Title)}
		}
		return verdict{Outcome: "white", Reason: "the same domain and title the reviewer sees, and the white page changes on every load"}
	}
	if got.Hash != white.Hash {
		return verdict{Outcome: "dark", Reason: "the page differs from the white page"}
	}
	return verdict{Outcome: "white", Reason: "the same page the reviewer sees"}
}
