package verticals

import (
	"regexp"
	"strings"
	"testing"
)

var (
	idRe   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	termRe = regexp.MustCompile(`^[a-z0-9][a-z0-9 '&-]*\*?$`)
)

// The user's must-haves (2026-09-29).
var mustHave = map[string]string{
	"blood-pressure":  "Blood Pressure",
	"memory-loss":     "Memory Loss",
	"weight-loss":     "Weight Loss",
	"tinnitus":        "Tinnitus",
	"diabetes":        "Diabetes",
	"neuropathy":      "Neuropathy",
	"prostate-health": "Prostate Health",
	"joint-pain":      "Joint Pain",
	"vision":          "Vision",
}

func TestList(t *testing.T) {
	l, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	names := map[string]string{}
	terms := map[string]string{}
	for _, c := range l.Categories {
		if !idRe.MatchString(c.ID) || ids[c.ID] || c.Name == "" || len(c.Verticals) == 0 {
			t.Errorf("category %q: needs a unique id, a name and verticals", c.ID)
		}
		ids[c.ID] = true
		catchAll := 0
		for _, v := range c.Verticals {
			if !idRe.MatchString(v.ID) || ids[v.ID] {
				t.Errorf("vertical %q: id must be unique, lowercase and dashed", v.ID)
			}
			ids[v.ID] = true
			if v.Name == "" || len(v.Name) > 20 {
				t.Errorf("vertical %s: name %q must be 1 to 20 characters", v.ID, v.Name)
			}
			if other, dup := names[strings.ToLower(v.Name)]; dup {
				t.Errorf("vertical %s: name %q also used by %s", v.ID, v.Name, other)
			}
			names[strings.ToLower(v.Name)] = v.ID
			if v.Covers == "" {
				t.Errorf("vertical %s: says nothing about what it covers", v.ID)
			}
			if v.CatchAll {
				catchAll++
			} else if len(v.Keywords) == 0 {
				t.Errorf("vertical %s: no keywords", v.ID)
			}
			for _, term := range append(append([]string{}, v.Keywords...), v.Hints...) {
				if !termRe.MatchString(term) || len(strings.Fields(term)) > 4 {
					t.Errorf("vertical %s: %q is not a lowercase word or phrase of up to 4 words", v.ID, term)
				}
				if other, dup := terms[term]; dup {
					t.Errorf("%q is in both %s and %s", term, other, v.ID)
				}
				terms[term] = v.ID
			}
		}
		if catchAll > 1 {
			t.Errorf("category %s: %d catch-alls, at most 1", c.ID, catchAll)
		}
	}
	// Plurals in s and es always match, so a term's plural is never listed too.
	for term, id := range terms {
		for _, end := range []string{"s", "es"} {
			if other, dup := terms[term+end]; dup {
				t.Errorf("%q (%s) is %q (%s) plus %q, which matches anyway", term+end, other, term, id, end)
			}
		}
	}
	for _, v := range l.Verticals() {
		for _, n := range v.Not {
			if n.What == "" {
				t.Errorf("vertical %s: a not with no what", v.ID)
			}
			if n.Goes == v.ID {
				t.Errorf("vertical %s: %q goes to itself", v.ID, n.What)
			} else if _, ok := l.Vertical(n.Goes); n.Goes != "" && !ok {
				t.Errorf("vertical %s: %q goes to %q, which is not a vertical", v.ID, n.What, n.Goes)
			}
		}
	}
	for id, name := range mustHave {
		v, ok := l.Vertical(id)
		if !ok || v.Name != name {
			t.Errorf("must-have %s (%q) is missing or renamed", id, name)
		}
	}
}
