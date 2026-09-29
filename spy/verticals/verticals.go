// Package verticals is Spy's fixed list of verticals, grouped by category
// (verticals.yaml). A classifier may only answer with one of these ids, or
// with nothing.
package verticals

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed verticals.yaml
var file []byte

// Category groups verticals for filtering: pick a category, then verticals.
type Category struct {
	ID        string     `yaml:"id"`
	Name      string     `yaml:"name"`
	Verticals []Vertical `yaml:"verticals"`
}

// Vertical is one market a creative can be in.
type Vertical struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Category string `yaml:"-"` // the category's id, filled on load
	Covers   string `yaml:"covers"`
	Not      []Not  `yaml:"not"`
	// Keywords name the vertical on their own; hints only help.
	Keywords []string `yaml:"keywords"`
	Hints    []string `yaml:"hints"`
	// CatchAll is used only when no other vertical of its category matches.
	CatchAll bool `yaml:"catch_all"`
}

// Not is a near neighbour that does not belong, and the vertical it goes to
// instead (empty when it depends).
type Not struct {
	What string `yaml:"what"`
	Goes string `yaml:"goes"`
}

// List is the whole list, in the file's order.
type List struct {
	Categories []Category `yaml:"categories"`
}

// Load reads the embedded list.
func Load() (List, error) {
	var l List
	if err := yaml.Unmarshal(file, &l); err != nil {
		return List{}, fmt.Errorf("verticals.yaml: %w", err)
	}
	for i := range l.Categories {
		for j := range l.Categories[i].Verticals {
			l.Categories[i].Verticals[j].Category = l.Categories[i].ID
		}
	}
	return l, nil
}

// Verticals returns every vertical, in the file's order.
func (l List) Verticals() []Vertical {
	var out []Vertical
	for _, c := range l.Categories {
		out = append(out, c.Verticals...)
	}
	return out
}

// Vertical returns the vertical with this id.
func (l List) Vertical(id string) (Vertical, bool) {
	for _, v := range l.Verticals() {
		if v.ID == id {
			return v, true
		}
	}
	return Vertical{}, false
}
