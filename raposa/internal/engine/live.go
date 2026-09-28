package engine

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// The saved link never works. Tracks strips the ad network's click values
// (tblci, ref_id, click_id) before storing a link, so a saved link no longer
// looks like an ad click and every cloaker serves the white page. A live
// link, which capture saw in a feed minutes before the visit, is what gets
// through: tracks_api.take_live_link_v1 hands each one out once.

// Target is one publisher in the targets file capture reads. Raposa reads the
// fields it needs to build a Taboola click referer; the rest of the file is
// capture's.
type Target struct {
	Name           string `yaml:"name"`
	Network        string `yaml:"network"` // empty means taboola
	Domain         string `yaml:"domain"`
	URL            string `yaml:"url"`
	TaboolaAccount string `yaml:"taboola_account"`
	Path           string `yaml:"path"`
	Placement      string `yaml:"placement"`
	ItemType       string `yaml:"item_type"`
}

// LoadTargets reads a targets file (publishers: [...]). Disabled targets are
// kept: an ad seen on one before it was switched off still needs its
// referer.
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
	return file.Publishers, nil
}

// findTarget picks the target by name first and domain second.
func findTarget(targets []Target, name, domain string) *Target {
	for i := range targets {
		if name != "" && strings.EqualFold(targets[i].Name, name) {
			return &targets[i]
		}
	}
	want := bareHost(domain)
	for i := range targets {
		if want != "" && bareHost(targets[i].Domain) == want {
			return &targets[i]
		}
	}
	return nil
}

// taboolaClickReferer is the Referer a reader's browser sends when it lands
// from a Taboola card. The card goes through Taboola's click endpoint
// (trc.taboola.com/<account>/log/3/click?...&redir=<link>), whose page sets the
// referrer policy to unsafe-url, so the landing page sees that whole click
// address, never the publisher's page. Measured on 2026-09-26 with a real click
// on okmagazine.com: everviewjournal.com served its dark page only to phones
// whose Referer was Taboola's, and the white page to the same phone with the
// publisher's page as Referer or none. Returns "" for a publisher that is not
// on Taboola.
func taboolaClickReferer(pub Target, link string) string {
	if pub.TaboolaAccount == "" || (pub.Network != "" && pub.Network != "taboola") {
		return ""
	}
	path := orDefault(pub.Path, "/")
	placement := orDefault(pub.Placement, "rbox-t2m")
	itemType := orDefault(pub.ItemType, "text")
	return "https://trc.taboola.com/" + pub.TaboolaAccount + "/log/3/click?pi=" + url.QueryEscape(path) +
		"&it=" + url.QueryEscape(itemType) + "&pt=" + url.QueryEscape(itemType) +
		"&li=" + url.QueryEscape(placement) + "&redir=" + url.QueryEscape(link)
}

// linkHost is the host a click link sends a reader to.
func linkHost(rawURL string) string {
	u, err := url.Parse(browserURL(rawURL))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// bareHost drops the prefixes that do not tell two hosts apart, as
// take_live_link_v1 does.
func bareHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "www.")
	return strings.TrimPrefix(host, "secure.")
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
