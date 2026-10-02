package launchuse

import (
	"strings"
	"testing"
)

func TestPrefixes(t *testing.T) {
	got := Prefixes([]string{"0123456789abcdef" + strings.Repeat("0", 48), "0123456789", " ABCDEF0123 ", "short", "zzzzzzzzzz", ""})
	if len(got) != 2 || got[0] != "0123456789" || got[1] != "abcdef0123" {
		t.Fatalf("prefixes = %v", got)
	}
	many := make([]string, MaxPrefixes+5)
	for i := range many {
		many[i] = strings.Repeat("0", 6) + string("0123456789abcdef"[i%16]) + string("0123456789abcdef"[i/16%16]) + string("0123456789abcdef"[i/256%16]) + "0"
	}
	if n := len(Prefixes(many)); n != MaxPrefixes {
		t.Fatalf("capped at %d, got %d", MaxPrefixes, n)
	}
}
