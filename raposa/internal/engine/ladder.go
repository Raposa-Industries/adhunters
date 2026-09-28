package engine

import (
	"crypto/rand"
	"math/big"
	"strings"

	"github.com/Raposa-Industries/adhunters/raposa/internal/lines"
)

// A residential line exiting in New York City got the white page where the
// same line in Ohio got the dark page: operators avoid serving the dark page
// near an ad network office. raposa.setting avoid_places holds that list,
// and every visit leaves from a state that is not on it.

// proxyOptions are the suffixes a previous pinning appended to the password.
// They are cut off before a new set is added.
var proxyOptions = []string{"_country-", "_state-", "_city-", "_region-", "_session-", "_lifetime-", "_streaming-"}

// pickPlace returns the US state the residential line is pinned to for one
// visit: the states in sample_states, round robin, skipping any state an
// operator watches. Returns "" when every state is avoided.
func pickPlace(states, avoidPlaces, avoidRegions []string, n int) string {
	if len(states) == 0 {
		return ""
	}
	for i := 0; i < len(states); i++ {
		state := strings.TrimSpace(states[(n+i)%len(states)])
		if state != "" && placeAllowed(state, avoidPlaces, avoidRegions) {
			return state
		}
	}
	return ""
}

// placeAllowed says a place is not one an operator watches. The comparison
// ignores case and spacing, so "New York City" and "newyorkcity" are the same
// place.
func placeAllowed(place string, avoidLists ...[]string) bool {
	slug := stateSlug(place)
	if slug == "" {
		return false
	}
	for _, list := range avoidLists {
		for _, avoided := range list {
			if a := stateSlug(avoided); a != "" && a == slug {
				return false
			}
		}
	}
	return true
}

// proxyState rewrites the residential line's password so the visit exits in
// one state, under a session id of its own. The proxy reads the options
// appended to the password: _country-us_state-<state>_session-<id>_lifetime-10m.
func proxyState(line lines.Line, state string) lines.Line {
	slug := stateSlug(state)
	pinned := line
	pinned.Password = basePassword(line.Password) + "_country-us"
	if slug != "" {
		pinned.Password += "_state-" + slug
	}
	pinned.Password += "_session-" + sessionID() + "_lifetime-10m"
	return pinned
}

// basePassword drops the options a previous pinning appended.
func basePassword(pw string) string {
	cut := len(pw)
	for _, opt := range proxyOptions {
		if i := strings.Index(pw, opt); i >= 0 && i < cut {
			cut = i
		}
	}
	return pw[:cut]
}

// stateSlug lowercases a place name and drops its spaces, which is the form
// the proxy takes.
func stateSlug(name string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", ""))
}

const sessionAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// sessionID is a fresh session value, so every visit leaves from a different
// home address.
func sessionID() string {
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(sessionAlphabet))))
		if err != nil {
			// A session id that repeats only costs one repeated address.
			b[i] = sessionAlphabet[i]
			continue
		}
		b[i] = sessionAlphabet[n.Int64()]
	}
	return string(b)
}

// deviceFor resolves a disguise's device rule against the device the ad runs
// on. One operator family serves the dark page to phones only, another to
// desktops only, so the ladder has a rung for each.
func deviceFor(rule, campaign string) string {
	if campaign != "phone" {
		campaign = "desktop"
	}
	switch rule {
	case "desktop", "phone":
		return rule
	case "other":
		if campaign == "desktop" {
			return "phone"
		}
		return "desktop"
	default:
		return campaign
	}
}

// stateZones is the time zone a visit from one state claims. A state not
// listed falls back to Central time, which is where most of sample_states sit.
var stateZones = map[string]string{
	"maine": "America/New_York", "southcarolina": "America/New_York",
	"westvirginia": "America/New_York", "indiana": "America/New_York",
	"ohio": "America/New_York", "kentucky": "America/New_York",
	"tennessee": "America/Chicago", "alabama": "America/Chicago",
	"mississippi": "America/Chicago", "arkansas": "America/Chicago",
	"iowa": "America/Chicago", "missouri": "America/Chicago",
	"oklahoma": "America/Chicago", "kansas": "America/Chicago",
	"nebraska": "America/Chicago",
	"idaho":    "America/Boise", "montana": "America/Denver",
	"wyoming": "America/Denver", "newmexico": "America/Denver",
	"utah": "America/Denver",
}

// lineZones is where each fixed line exits, measured with ipinfo.io from
// bigworker on 2026-09-25. A browser whose clock disagrees with its address is
// one of the first things a cloaker checks, so an unpinned visit reports the
// zone its line actually sits in rather than a guess.
var lineZones = map[string]string{
	"dc-us-1": "America/Los_Angeles", // Los Angeles
	"dc-us-2": "America/New_York",    // Floris, Virginia
	"dc-us-3": "America/Chicago",     // Dallas
	"dc-us-4": "America/Chicago",     // Dallas
	"dc-us-5": "America/Chicago",     // Dallas
	"isp-6":   "America/New_York",    // Ashburn, Virginia
	"isp-7":   "America/Chicago",     // McGehee, Arkansas
	"isp-8":   "America/Chicago",     // Helena, Mississippi
	"isp-9":   "Pacific/Honolulu",    // Honalo, Hawaii
	"isp-10":  "America/New_York",    // New York City
}

// lineExits is the city each fixed line exits in, measured with ipinfo.io from
// bigworker on 2026-09-25. Operators serve the white page to visitors near an
// ad network office (New York City did, on both families tested), so which
// line a rung uses is decided by where it exits, not only by its role.
var lineExits = map[string]string{
	"dc-us-1": "Los Angeles",
	"dc-us-2": "Floris",
	"dc-us-3": "Dallas",
	"dc-us-4": "Dallas",
	"dc-us-5": "Dallas",
	"isp-6":   "Ashburn",
	"isp-7":   "McGehee",
	"isp-8":   "Helena",
	"isp-9":   "Honalo",
	"isp-10":  "New York City",
}

// inAvoidedPlace says a fixed line exits in a city an operator watches.
func inAvoidedPlace(lineKey string, avoid []string) bool {
	city := strings.ToLower(lineExits[lineKey])
	if city == "" {
		return false
	}
	for _, a := range avoid {
		if strings.ToLower(strings.TrimSpace(a)) == city {
			return true
		}
	}
	return false
}

// chooseLine picks the fixed line for one visit. The baseline wants what an
// ad network reviewer sees, so it prefers a line exiting in an avoided place,
// New York City first; any other rung skips those lines, since a visit from
// there gets the white page whatever else the disguise does. The residential
// line is pinned per visit instead, so it is handed back as it is. n rotates
// through the candidates so consecutive visits use different lines.
func chooseLine(all []lines.Line, role string, avoid []string, baseline bool, n int) *lines.Line {
	if role == "residential" {
		for _, l := range all {
			if l.Role == role {
				cpy := l
				return &cpy
			}
		}
		return nil
	}
	var first, second, third []lines.Line
	for _, l := range all {
		if l.Role != "dc" && l.Role != "isp" {
			continue
		}
		avoided := inAvoidedPlace(l.Key, avoid)
		switch {
		case baseline && lineExits[l.Key] == "New York City":
			first = append(first, l)
		case baseline && avoided:
			second = append(second, l)
		case baseline && l.Role == "dc":
			third = append(third, l)
		case !baseline && l.Role == role && !avoided:
			first = append(first, l)
		case !baseline && l.Role == role:
			// Only when nothing else is left: the visit is recorded with its
			// line, so the screen shows why it saw the white page.
			third = append(third, l)
		}
	}
	for _, group := range [][]lines.Line{first, second, third} {
		if len(group) > 0 {
			cpy := group[n%len(group)]
			return &cpy
		}
	}
	return nil
}

// timezoneFor is the time zone a browser visit reports: the pinned state's
// when the residential line was pinned, else the zone the line exits in.
func timezoneFor(state, lineKey string) string {
	if zone, ok := stateZones[stateSlug(state)]; ok {
		return zone
	}
	if zone, ok := lineZones[lineKey]; ok {
		return zone
	}
	return "America/Chicago"
}
