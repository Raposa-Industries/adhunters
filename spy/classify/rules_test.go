package classify

import (
	"testing"

	"github.com/Raposa-Industries/adhunters/shared/verticals"
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

// The most seen running ads that had no vertical on 2026-10-01 (spy-numbers
// check), headline only. The ones left blank have nothing to go on in the
// headline; their landing page or the model decides.
func TestRulesLiveMisses(t *testing.T) {
	r := rules(t)
	for _, c := range []struct{ headline, vertical string }{
		{"Ultra-Soft Cashmere Sweatshirt Loved by Thousands of Men", "fashion"},
		{"Katherine Heigl: Is This Toxic For Dogs?", "pets"},
		{"Will Smith's $2.5M Rolling Mansion Is Turning Heads", "celebrity-viral"},
		{`Cardiologist Warns Americans: "Pour Out The Bottled Water"`, "home-services"},
		{"Let Your Dog Lick Some Honey Every Day", "pets"},
		{"Stop Yelling At Your Dog, Use This Military K9 Trick Instead", "pets"},
		{"Halloween Lovers Are Obsessed With This 3D Rug", "home-garden"},
		{"Best Halloween Decoration: The Pumpkin Lamp Everyone Wants", "home-garden"},
		{"Every Car Has Scratches, Few Know This 30 Second Trick", ""},
		{"This Trick Removes Any Scratch From Car Paint", "cars"},
		{"Wednesday: IRS Forgives Millions In Tax Debt", "debt-relief"},
		{"Owe the IRS $5,000 or More? This 2026 Program May Help", "debt-relief"},
		{"Owe Taxes to Government? Relief Is Here", "debt-relief"},
		{"The Cozy Cashmere Sweatshirt Every Man Wants!", "fashion"},
		{"Take A Closer Look At The Ingredients In Your Dog's Food", "pets"},
		{"If You Own An iPhone Don't Forget To Do This", "phones-apps"},
		{"Hidden Samsung Feature That Turns Off All Ads", "phones-apps"},
		{"The End Of An Era: A Master Knife Maker Retires", "gadgets"},
		{"Building Options Trades From Scratch?", "investing"},
		{"Most gardens miss the one thing butterflies actually need", "home-garden"},
		{"This Classic Men's Mid-Length Jacket Sells Out Every Fall", "fashion"},
	} {
		if a := r.Classify(Text{Ad: c.headline}); a.Vertical != c.vertical {
			t.Errorf("%q: vertical %q, want %q (points %v)", c.headline, a.Vertical, c.vertical, a.Points)
		}
	}
}
