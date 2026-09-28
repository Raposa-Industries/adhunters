// Package feed builds the requests capture sends to each ad network's feed.
//
// It only builds requests. Parsing belongs to the loader, which reads the raw
// files capture writes, so a parser change never touches collection.
//
// Ported from adhunters-collector e20148c, internal/sweeper/client.go and
// newsbreak.go. The requests are byte-for-byte what the collector sends, so a
// shadow run measures the same responses.
package feed

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Networks capture knows how to ask.
const (
	Taboola   = "taboola"
	NewsBreak = "newsbreak"
)

// Target is one publisher capture scrapes. The fields and their YAML names
// are the collector's publishers.yaml, so that file loads as it is.
type Target struct {
	Name    string `yaml:"name"`
	Network string `yaml:"network"` // empty means taboola
	Domain  string `yaml:"domain"`
	URL     string `yaml:"url"`
	Enabled bool   `yaml:"enabled"`

	// Taboola.
	TaboolaAccount string `yaml:"taboola_account"`
	Path           string `yaml:"path"`
	Placement      string `yaml:"placement"`
	ItemType       string `yaml:"item_type"`
	BatchSize      int    `yaml:"batch_size"`

	// NewsBreak: slots asked for in one auction, or groups of slots of which
	// each scrape asks for one (slots of two groups in one auction get no ads).
	Placements      []string   `yaml:"placements"`
	PlacementGroups [][]string `yaml:"placement_groups"`

	// Devices to scrape as. Empty means desktop and phone.
	Devices []string `yaml:"devices"`
}

// NetworkName is the target's network, with the empty default spelled out.
func (t Target) NetworkName() string {
	if t.Network == "" {
		return Taboola
	}
	return t.Network
}

// DeviceList is the devices to pick from for this target.
func (t Target) DeviceList() []string {
	if len(t.Devices) == 0 {
		return []string{"desktop", "phone"}
	}
	return t.Devices
}

// LoadTargets reads a targets file and returns its enabled targets. Enabled
// NewsBreak targets need no Taboola account; Taboola ones do.
func LoadTargets(path string) ([]Target, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Publishers []Target `yaml:"publishers"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []Target
	for _, t := range file.Publishers {
		if !t.Enabled {
			continue
		}
		switch t.NetworkName() {
		case Taboola:
			if t.TaboolaAccount == "" {
				return nil, fmt.Errorf("%s: taboola target without taboola_account", t.Name)
			}
		case NewsBreak:
		default:
			return nil, fmt.Errorf("%s: unknown network %q", t.Name, t.Network)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no enabled targets", path)
	}
	return out, nil
}
