package engine

import (
	"strings"
	"testing"

	"adhunters/collector/internal/model"
)

// The states and places as spy.raposa_setting seeds them.
var (
	sampleStates = []string{"Ohio", "Iowa", "New York", "South Carolina"}
	avoidPlaces  = []string{"New York", "New York City", "Los Angeles"}
)

func TestPickPlaceSkipsWatchedPlaces(t *testing.T) {
	cases := []struct {
		name   string
		states []string
		avoid  []string
		n      int
		want   string
	}{
		{"round robin, first visit", sampleStates, avoidPlaces, 0, "Ohio"},
		{"round robin, second visit", sampleStates, avoidPlaces, 1, "Iowa"},
		{"a watched state is skipped", sampleStates, avoidPlaces, 2, "South Carolina"},
		{"the cursor wraps", sampleStates, avoidPlaces, 4, "Ohio"},
		{"no states at all", nil, avoidPlaces, 0, ""},
		{"every state is watched", []string{"New York"}, avoidPlaces, 0, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickPlace(c.states, c.avoid, nil, c.n); got != c.want {
				t.Fatalf("pickPlace(%v, %d) = %q, want %q", c.states, c.n, got, c.want)
			}
		})
	}
}

func TestPlaceAllowedIgnoresCaseAndSpacing(t *testing.T) {
	cases := []struct {
		place string
		want  bool
	}{
		{"Ohio", true},
		{"New York", false},
		{"new york", false},
		{"NewYork", false},
		{"", false},
	}

	for _, c := range cases {
		t.Run(c.place, func(t *testing.T) {
			if got := placeAllowed(c.place, avoidPlaces, nil); got != c.want {
				t.Fatalf("placeAllowed(%q) = %v, want %v", c.place, got, c.want)
			}
		})
	}
}

func TestBasePasswordDropsEarlierOptions(t *testing.T) {
	cases := []struct {
		name, pw, want string
	}{
		{"a plain password", "f0Rvfpmb7PFdRhIo", "f0Rvfpmb7PFdRhIo"},
		{"the pool file's country and session", "f0Rvfpmb7PFdRhIo_country-us_session-WSY55KHY_lifetime-59m", "f0Rvfpmb7PFdRhIo"},
		{"a state pinned earlier", "f0Rvfpmb7PFdRhIo_state-ohio_session-ABCD1234", "f0Rvfpmb7PFdRhIo"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := basePassword(c.pw); got != c.want {
				t.Fatalf("basePassword(%q) = %q, want %q", c.pw, got, c.want)
			}
		})
	}
}

func TestProxyStatePinsOneStatePerVisit(t *testing.T) {
	line := model.ProxyLine{
		Key: "res-1", Role: "residential", Host: "geo.iproyal.com", Port: 12321,
		Username: "BWvhKby7WwPZtBfY",
		Password: "f0Rvfpmb7PFdRhIo_country-us_session-WSY55KHY_lifetime-59m",
	}

	first := proxyState(line, "South Carolina")
	if !strings.HasPrefix(first.Password, "f0Rvfpmb7PFdRhIo_country-us_state-southcarolina_session-") {
		t.Fatalf("password = %q, want the state pinned after the base password", first.Password)
	}
	if !strings.HasSuffix(first.Password, "_lifetime-10m") {
		t.Fatalf("password = %q, want a 10 minute lifetime", first.Password)
	}

	// Every visit gets its own session id, so each one leaves from a
	// different home address.
	second := proxyState(line, "South Carolina")
	if first.Password == second.Password {
		t.Fatalf("two visits shared the session %q", first.Password)
	}
	if first.Host != line.Host || first.Username != line.Username {
		t.Fatalf("pinning changed the line itself: %+v", first)
	}
}

func TestDeviceFor(t *testing.T) {
	cases := []struct {
		rule, campaign, want string
	}{
		{"campaign", "phone", "phone"},
		{"campaign", "desktop", "desktop"},
		{"campaign", "", "desktop"},
		{"other", "phone", "desktop"},
		{"other", "desktop", "phone"},
		{"desktop", "phone", "desktop"},
		{"phone", "desktop", "phone"},
	}

	for _, c := range cases {
		t.Run(c.rule+"/"+c.campaign, func(t *testing.T) {
			if got := deviceFor(c.rule, c.campaign); got != c.want {
				t.Fatalf("deviceFor(%q, %q) = %q, want %q", c.rule, c.campaign, got, c.want)
			}
		})
	}
}

func TestTimezoneFor(t *testing.T) {
	cases := []struct{ state, line, want string }{
		{"Ohio", "res-1", "America/New_York"},
		{"South Carolina", "res-1", "America/New_York"},
		{"Iowa", "res-1", "America/Chicago"},
		{"Montana", "res-1", "America/Denver"},
		// Unpinned visits report the zone their line exits in.
		{"", "isp-9", "Pacific/Honolulu"},
		{"", "dc-us-1", "America/Los_Angeles"},
		{"", "isp-10", "America/New_York"},
		// A pinned state wins over the line.
		{"Iowa", "isp-10", "America/Chicago"},
		{"", "unknown-line", "America/Chicago"},
	}

	for _, c := range cases {
		t.Run(c.state+"/"+c.line, func(t *testing.T) {
			if got := timezoneFor(c.state, c.line); got != c.want {
				t.Fatalf("timezoneFor(%q, %q) = %q, want %q", c.state, c.line, got, c.want)
			}
		})
	}
}

// The baseline takes the New York City line, because a visit from an ad
// network office city is what a reviewer sees; every other rung stays off the
// lines exiting in an avoided place.
func TestChooseLine(t *testing.T) {
	lines := []model.ProxyLine{
		{Key: "dc-us-1", Role: "dc"}, {Key: "dc-us-2", Role: "dc"}, {Key: "dc-us-3", Role: "dc"},
		{Key: "isp-6", Role: "isp"}, {Key: "isp-7", Role: "isp"}, {Key: "isp-8", Role: "isp"},
		{Key: "isp-10", Role: "isp"}, {Key: "res-1", Role: "residential"},
	}
	avoid := []string{"New York City", "Los Angeles", "Dallas", "Ashburn"}

	cases := []struct {
		name     string
		role     string
		baseline bool
		n        int
		want     string
	}{
		{"baseline goes to New York City", "dc", true, 0, "isp-10"},
		{"baseline goes to New York City whatever the cursor", "dc", true, 7, "isp-10"},
		{"an isp rung skips Ashburn and New York City", "isp", false, 0, "isp-7"},
		{"an isp rung rotates over the lines left", "isp", false, 1, "isp-8"},
		{"a dc rung skips Los Angeles and Dallas", "dc", false, 0, "dc-us-2"},
		{"a dc rung with one line left keeps using it", "dc", false, 5, "dc-us-2"},
		{"the residential line is handed back as it is", "residential", false, 3, "res-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := chooseLine(lines, c.role, avoid, c.baseline, c.n)
			if got == nil || got.Key != c.want {
				key := "<nil>"
				if got != nil {
					key = got.Key
				}
				t.Fatalf("chooseLine(%s, baseline=%v, n=%d) = %s, want %s", c.role, c.baseline, c.n, key, c.want)
			}
		})
	}

	// New York City is the reviewer city whatever the avoid list says. Without
	// that line, the baseline falls back to a datacenter line.
	if got := chooseLine(lines, "dc", nil, true, 0); got == nil || got.Key != "isp-10" {
		t.Fatalf("baseline with nothing avoided should still pick New York City, got %v", got)
	}
	if got := chooseLine(lines[:6], "dc", nil, true, 0); got == nil || got.Role != "dc" {
		t.Fatalf("baseline without the New York City line should fall back to a dc line, got %v", got)
	}
}

func TestClimbDeviceTakesTurnsOverTheAdsDevices(t *testing.T) {
	r := &run{ad: model.RaposaAdContext{Device: "desktop", Devices: []string{"desktop", "phone"}}}
	campaign := model.RaposaDisguise{DeviceRule: "campaign"}
	if got := []string{r.climbDevice(campaign, 1), r.climbDevice(campaign, 2), r.climbDevice(campaign, 3)}; got[0] != "desktop" || got[1] != "phone" || got[2] != "desktop" {
		t.Fatalf("campaign rule over two devices = %v", got)
	}
	if got := r.climbDevice(model.RaposaDisguise{DeviceRule: "other"}, 2); got != "phone" {
		t.Fatalf("other rule = %s, want phone", got)
	}
	one := &run{ad: model.RaposaAdContext{Device: "phone", Devices: []string{"phone"}}}
	if got := one.climbDevice(campaign, 2); got != "phone" {
		t.Fatalf("one device = %s, want phone", got)
	}
}

func TestTaboolaClickReferer(t *testing.T) {
	pub := model.PublisherTarget{Name: "OK Magazine", TaboolaAccount: "mystifyent-okmagazine", Path: "/", Placement: "rbox-t2m"}
	got := taboolaClickReferer(pub, "https://everviewjournal.com/?sub1=1&tblci=tblX")
	want := "https://trc.taboola.com/mystifyent-okmagazine/log/3/click?pi=%2F&it=text&pt=text&li=rbox-t2m&redir=https%3A%2F%2Feverviewjournal.com%2F%3Fsub1%3D1%26tblci%3DtblX"
	if got != want {
		t.Fatalf("referer = %s", got)
	}
	if ref := taboolaClickReferer(model.PublisherTarget{Name: "nb", Network: "newsbreak", TaboolaAccount: "x"}, "https://a.com/"); ref != "" {
		t.Fatalf("a NewsBreak publisher got a Taboola referer: %s", ref)
	}
}
