// Package text holds the rules for ad text that several services apply the
// same way.
package text

import (
	"strings"
	"unicode"
)

// CleanLine removes the characters a headline must never carry because
// Taboola rejects them and nobody can see them to fix them: zero-width
// spaces and joiners (U+200B to U+200D, U+2060, U+FEFF), the soft hyphen
// (U+00AD) and the bidi controls (U+202A to U+202E, U+2066 to U+2069). Runs
// of whitespace, including line breaks, become one space, and the ends are
// trimmed.
func CleanLine(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if Invisible(r) {
			continue
		}
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

// Invisible reports whether r is one of the characters CleanLine removes.
func Invisible(r rune) bool {
	switch {
	case r >= 0x200B && r <= 0x200D, r == 0x2060, r == 0xFEFF, r == 0x00AD,
		r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// Hidden counts the characters CleanLine would remove (not the spaces it
// folds), so a page can say how many it took out.
func Hidden(s string) int {
	n := 0
	for _, r := range s {
		if Invisible(r) {
			n++
		}
	}
	return n
}
