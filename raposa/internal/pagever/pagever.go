// Package pagever decides which version of a page one capture is.
//
// A page is one site address and path, ignoring the query string, which
// carries the click values (the glossary's landing page). A new version of a
// page is stored only when its words change. Ported from adplatform-v2's
// MatchVersion (internal/raposa/versions.go there):
//
//  1. the same normalised text is the same version, whatever else changed;
//  2. failing that, a shingle distance at or under Trigger is the same version,
//     so a date, a visitor counter, a countdown or a city name does not make a
//     new one.
//
// The words decide, not the HTML: a checkout carries a csrf token, a request
// id and the visitor's IP in its HTML, and a VSL carries the tracker's click
// id in every link, so the HTML of one page differs on every visit (up to 80
// captures of one page before this package existed).
package pagever

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"unicode"
)

// ShingleWidth is how many words one shingle holds.
const ShingleWidth = 5

// Trigger is the largest shingle distance still read as the same version. The
// same number adplatform-v2 used, where it was never measured either.
const Trigger = 0.30

// Key is the page one URL belongs to: the host in lower case and the path
// with its trailing slashes dropped. The same rule as pageKey in the web
// app's site/src/lib/raposa.ts.
func Key(host, path string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if path == "" {
		path = "/"
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}
	}
	return host + path
}

// KeyOf is Key for a whole URL.
func KeyOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return Key(u.Hostname(), u.Path)
}

// normalise is the text lowercased with every run of spaces made one.
func normalise(text string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(text) {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// TextDigest is the sha256 of the normalised text, in hex.
func TextDigest(text string) string {
	sum := sha256.Sum256([]byte(normalise(text)))
	return hex.EncodeToString(sum[:])
}

// Shingles is every overlapping run of ShingleWidth words, lowercased, once.
// A text shorter than that is one shingle, so two short pages still compare.
func Shingles(text string) []string {
	words := strings.Fields(strings.ToLower(text))
	if len(words) == 0 {
		return nil
	}
	if len(words) < ShingleWidth {
		return []string{strings.Join(words, " ")}
	}
	seen := make(map[string]bool, len(words))
	out := make([]string, 0, len(words)-ShingleWidth+1)
	for i := 0; i+ShingleWidth <= len(words); i++ {
		s := strings.Join(words[i:i+ShingleWidth], " ")
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Distance is 1 - Jaccard of two shingle sets. Two empty sets are 0 apart.
func Distance(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	shared := 0
	seenB := make(map[string]bool, len(b))
	for _, s := range b {
		if seenB[s] {
			continue
		}
		seenB[s] = true
		if set[s] {
			shared++
		}
	}
	union := len(set) + len(seenB) - shared
	if union == 0 {
		return 0
	}
	return 1 - float64(shared)/float64(union)
}

// Version is one stored version of a page, as the match needs it.
type Version struct {
	ID         int32
	TextDigest string
	Text       string
}

// Match says which of a page's versions a capture with this text is. versions
// come newest first: an operator who rewrote a page yesterday serves
// yesterday's words today. ok is false when the words are new.
func Match(versions []Version, text string) (id int32, ok bool) {
	digest := TextDigest(text)
	for _, v := range versions {
		if v.TextDigest == digest {
			return v.ID, true
		}
	}
	mine := Shingles(text)
	for _, v := range versions {
		if Distance(Shingles(v.Text), mine) <= Trigger {
			return v.ID, true
		}
	}
	return 0, false
}
