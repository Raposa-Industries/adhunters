package page

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseDOMKeepsFullBodyText(t *testing.T) {
	long := strings.Repeat("Tinnitus relief ", 200) // 3,200 characters
	tel := ParseDOM([]byte("<html><head><title>T</title><script>var x=1</script></head><body><p>"+long+"</p></body></html>"), "https://a.com/")
	if len(tel.BodyExcerpt) != 1003 {
		t.Fatalf("excerpt length = %d, want 1,000 + ...", len(tel.BodyExcerpt))
	}
	if !strings.Contains(tel.BodyText, strings.TrimSpace(long)) || strings.Contains(tel.BodyText, "var x") {
		t.Fatalf("body text wrong: %q", tel.BodyText[:80])
	}
}

func TestTruncateRunes(t *testing.T) {
	s := strings.Repeat("é", 10)
	if got := truncateRunes(s, 4); got != "éééé" || !utf8.ValidString(got) {
		t.Fatalf("got %q", got)
	}
	if got := truncateRunes("abc", 5); got != "abc" {
		t.Fatalf("got %q", got)
	}
}
