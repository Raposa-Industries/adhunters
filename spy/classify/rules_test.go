package classify

import (
	"testing"

	"github.com/Raposa-Industries/adhunters/spy/verticals"
)

func rules(t *testing.T) *Rules {
	t.Helper()
	l, err := verticals.Load()
	if err != nil {
		t.Fatal(err)
	}
	return NewRules(l)
}

func TestRules(t *testing.T) {
	r := rules(t)
	for _, c := range []struct {
		name     string
		text     Text
		vertical string
		sure     bool
	}{
		{"keyword in headline", Text{Ad: "Doctors stunned: this lowers high blood pressure overnight"}, "blood-pressure", true},
		{"prefix keyword", Text{Ad: "Diabetics are rushing to try this"}, "diabetes", false},
		{"hint alone says nothing", Text{Ad: "The heart of the matter"}, "", false},
		{"catch-all from hints only", Text{Ad: "Best vitamin supplements for seniors"}, "other-health", false},
		{"nothing", Text{Ad: "You won't believe what happened next"}, "", false},
		{"brand helps", Text{Ad: "Try this simple trick tonight", Brand: "Tinnitus Relief Daily"}, "tinnitus", false},
		{"page title decides", Text{Ad: "Seniors are loving this", Page: "Ringing in ears? Tinnitus breakthrough"}, "tinnitus", false},
	} {
		a := r.Classify(c.text)
		if a.Vertical != c.vertical {
			t.Errorf("%s: vertical %q, want %q (points %v)", c.name, a.Vertical, c.vertical, a.Points)
		}
		if c.sure && a.Confidence < Sure {
			t.Errorf("%s: confidence %.2f, want sure", c.name, a.Confidence)
		}
		if a.Vertical != "" {
			if v, ok := r.Vertical(a.Vertical); !ok || v.Category != a.Category {
				t.Errorf("%s: category %q does not hold %q", c.name, a.Category, a.Vertical)
			}
		}
	}
}

// Two verticals of different categories split the confidence.
func TestRulesMixed(t *testing.T) {
	r := rules(t)
	one := r.Classify(Text{Ad: "Lower your blood pressure naturally"})
	mixed := r.Classify(Text{Ad: "Lower your blood pressure and stop tinnitus"})
	if mixed.Confidence >= one.Confidence {
		t.Errorf("mixed %.2f should be less sure than one %.2f", mixed.Confidence, one.Confidence)
	}
}
