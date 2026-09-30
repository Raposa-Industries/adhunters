package checks

import (
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/create/internal/openai"
)

func kinds(ws []Warning) string {
	var k []string
	for _, w := range ws {
		k = append(k, w.Kind)
	}
	return strings.Join(k, ",")
}

func TestHeadline(t *testing.T) {
	cases := []struct{ text, want string }{
		{"Ringing In Your Ears After 60? Try This", ""},
		{"Ring​ing in your ears", "hidden"},
		{"This SHOCKING trick for the FDA", "caps,rule"},
		{"A Simple Morning Habit That Stops the Ringing Right Away Tonight", "length,rule"},
		{"Lose 20 lbs with this", "rule"},
	}
	for _, c := range cases {
		if got := kinds(Headline(c.text)); got != c.want {
			t.Errorf("%q: %s, want %s (%v)", c.text, got, c.want, Headline(c.text))
		}
	}
}

func TestBlocked(t *testing.T) {
	list := []openai.Blocked{{Text: "memory"}, {Text: "memory loss"}, {Text: "feet"}, {Text: "Drink 1 Cup In The Morning"}}
	hits := BlockedHits("Memory Loss after 60? drink 1 cup in the morning", list)
	var names []string
	for _, h := range hits {
		names = append(names, h.Text)
	}
	if strings.Join(names, "|") != "memory loss|Drink 1 Cup In The Morning" {
		t.Errorf("hits: %v", names)
	}
	if len(BlockedHits("Memorable feetless day", list)) != 0 {
		t.Error("matched inside a longer word")
	}
	if len(BlockedHits("Memories", []openai.Blocked{{Text: "memory"}})) != 0 {
		t.Error("memories is not memory plus an ending")
	}
	if len(BlockedHits("Cold feets", list)) != 1 {
		t.Error("missed a plural")
	}
	for _, c := range []struct{ in, want string }{
		{"Memory tricks for memory", "Focus tricks for focus"},
		{"MEMORY TRICKS", "FOCUS TRICKS"},
		{"Better memory: a guide", "Better focus: a guide"},
		{"Memorable memorys", "Memorable focus"},
	} {
		if got := Swap(c.in, "memory", "focus"); got != c.want {
			t.Errorf("swap %q: %q, want %q", c.in, got, c.want)
		}
	}
	if w := Headline("Sharper memory after 60"); !strings.Contains(kinds(w), "blocked") || len(w[len(w)-1].Alternatives) == 0 {
		t.Errorf("blocked warning with alternatives: %+v", w)
	}
}
