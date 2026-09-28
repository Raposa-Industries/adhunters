package pagever

import "testing"

func TestKey(t *testing.T) {
	cases := map[string]string{
		"https://HealthyHorizonClub.com/02/vsl-x/?rtkcid=abc": "healthyhorizonclub.com/02/vsl-x",
		"https://orders.clickbank.net/?cbfid=1":               "orders.clickbank.net/",
		"https://a.com":                                       "a.com/",
		"https://a.com//":                                     "a.com/",
	}
	for in, want := range cases {
		if got := KeyOf(in); got != want {
			t.Errorf("KeyOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatch(t *testing.T) {
	base := "Only 12 people are viewing this offer right now. Order today and save on the six bottle package with free shipping to your door, backed by a sixty day guarantee."
	counter := "Only 13 people are viewing this offer right now. Order today and save on the six bottle package with free shipping to your door, backed by a sixty day guarantee."
	other := "A completely different advertorial about joint pain and a doctor who found a kitchen trick that nobody talks about anymore."
	vs := []Version{{ID: 7, TextDigest: TextDigest(base), Text: base}}
	if id, ok := Match(vs, "  ONLY 12 people are   viewing this offer right now. Order today and save on the six bottle package with free shipping to your door, backed by a sixty day guarantee."); !ok || id != 7 {
		t.Fatalf("same words, other spacing: got %d %v", id, ok)
	}
	if id, ok := Match(vs, counter); !ok || id != 7 {
		t.Fatalf("a counter moved: got %d %v", id, ok)
	}
	if _, ok := Match(vs, other); ok {
		t.Fatalf("new words matched an old version")
	}
	if d := Distance(nil, nil); d != 0 {
		t.Fatalf("two empty sets: %v", d)
	}
}
