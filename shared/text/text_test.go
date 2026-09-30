package text

import "testing"

func TestCleanLine(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"  Doctors\u200b Reveal  4 Drinks \n", "Doctors Reveal 4 Drinks"},
		{"Mem\u00adory\u2066 Tip\u2069", "Memory Tip"},
		{"\ufeff", ""},
		{"Plain", "Plain"},
	} {
		if got := CleanLine(c.in); got != c.want {
			t.Errorf("CleanLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if n := Hidden("a\u200bb\u200cc d"); n != 2 {
		t.Errorf("Hidden = %d, want 2", n)
	}
}
