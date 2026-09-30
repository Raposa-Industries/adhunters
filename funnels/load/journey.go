package load

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Journey is one journey rebuilt from its events.
type Journey struct {
	ID         string
	Hour       time.Time
	StartedAt  time.Time
	LastAt     time.Time
	Site       string
	FirstLP    string
	LastLP     string
	LastStep   string
	LPs        int
	ClickID    string
	Subs       map[string]string
	Device     string
	Country    string
	VisibleMS  int64
	MaxScroll  int
	HadInput   bool
	BotSuspect bool
	BotReason  string
	Steps      []Step
	Videos     []Video
}

// Step is one step a journey reached, the first time.
type Step struct {
	Seq  int
	LP   string
	Step string
	At   time.Time
}

// Video is what a journey did with one video.
type Video struct {
	Video        string
	Arm          string
	LP           string
	LenS         int
	PitchS       *int
	Autoplayed   bool
	Played       bool
	Watched      [][2]int // merged, half-open [from, to)
	WatchedS     int
	LastS        int
	ReachedPitch bool
}

// Step names the loader gives on its own; the page names the others with
// data-ah-step. Clicks and forms are "click:<name>" and "form:<name>", and
// a video's are "play:<video>", "pitch:<video>" and "end:<video>".
const (
	StepView   = "view"    // the landing page loaded
	StepStay10 = "stay10s" // 10 s with this landing page in view
)

// maxVideoS caps a video's length and seconds: 6 hours.
const maxVideoS = 6 * 3600

var (
	botUA    = regexp.MustCompile(`(?i)bot|crawl|spider|slurp|headless|lighthouse|preview|facebookexternalhit|python|curl|wget|httpclient|java/|go-http`)
	tabletUA = regexp.MustCompile(`(?i)ipad|tablet|kindle|silk/`)
	phoneUA  = regexp.MustCompile(`(?i)mobi|iphone|ipod|android|windows phone`)
)

// Device is phone, tablet or desktop, from the user agent.
func Device(ua string, screenW *int) string {
	switch {
	case tabletUA.MatchString(ua):
		return "tablet"
	case strings.Contains(strings.ToLower(ua), "android") && !strings.Contains(strings.ToLower(ua), "mobile"):
		return "tablet"
	case phoneUA.MatchString(ua):
		return "phone"
	case ua == "" && screenW != nil && *screenW > 0 && *screenW < 768:
		return "phone"
	}
	return "desktop"
}

type evFields struct {
	N     string          `json:"n"`
	M     int             `json:"m"`
	Vis   int64           `json:"vis"`
	Sc    int             `json:"sc"`
	ID    string          `json:"id"`
	Ev    string          `json:"ev"`
	Pos   float64         `json:"pos"`
	Len   float64         `json:"len"`
	Pitch *float64        `json:"pitch"`
	A     string          `json:"a"`
	W     [][]json.Number `json:"w"`
}

// Build rebuilds one journey from its events, in the order received.
func Build(events []Event) Journey {
	sort.SliceStable(events, func(i, k int) bool { return events[i].ReceivedAt.Before(events[k].ReceivedAt) })
	first := events[0]
	j := Journey{
		ID: first.Journey, StartedAt: first.ReceivedAt, Hour: first.ReceivedAt.Truncate(time.Hour),
		Site: first.Site, FirstLP: first.LP, LastLP: first.LP, Subs: map[string]string{},
		Device: Device(first.UA, first.ScreenW),
	}
	reached := map[[2]string]bool{}
	step := func(lp, name string, at time.Time) {
		if name == "" || reached[[2]string{lp, name}] {
			return
		}
		reached[[2]string{lp, name}] = true
		j.Steps = append(j.Steps, Step{Seq: len(j.Steps) + 1, LP: lp, Step: name, At: at})
	}
	videos := map[string]*Video{}
	var order []string
	webdriver, ua := false, first.UA
	visibleOn := map[string]int64{}
	for _, e := range events {
		j.LastAt = e.ReceivedAt
		if j.ClickID == "" {
			j.ClickID = e.ClickID
		}
		for k, v := range e.Subs {
			if _, ok := j.Subs[k]; !ok {
				j.Subs[k] = v
			}
		}
		if j.Country == "" {
			j.Country = e.Country
		}
		j.HadInput = j.HadInput || e.HadInput
		webdriver = webdriver || e.Webdriver
		var f evFields
		_ = json.Unmarshal(e.Data, &f)
		switch e.Kind {
		case "view":
			j.LPs++
			j.LastLP = e.LP
			step(e.LP, StepView, e.ReceivedAt)
		case "scroll":
			if f.M == 25 || f.M == 50 || f.M == 75 || f.M == 100 {
				step(e.LP, "scroll"+strconv.Itoa(f.M), e.ReceivedAt)
				j.MaxScroll = max(j.MaxScroll, f.M)
			}
		case "beat", "exit":
			if f.Vis > 0 && f.Vis < 3_600_000 {
				j.VisibleMS += f.Vis
				visibleOn[e.LP] += f.Vis
			}
			if f.Sc >= 0 && f.Sc <= 100 {
				j.MaxScroll = max(j.MaxScroll, f.Sc)
			}
			if visibleOn[e.LP] >= 10_000 {
				step(e.LP, StepStay10, e.ReceivedAt)
			}
		case "step":
			step(e.LP, clip(f.N, 80), e.ReceivedAt)
		case "click":
			step(e.LP, "click:"+clip(f.N, 80), e.ReceivedAt)
		case "form":
			step(e.LP, "form:"+clip(f.N, 80), e.ReceivedAt)
		case "video":
			id := clip(f.ID, 80)
			if id == "" {
				continue
			}
			v := videos[id]
			if v == nil {
				v = &Video{Video: id, LP: e.LP, LastS: -1}
				videos[id] = v
				order = append(order, id)
			}
			if v.Arm == "" {
				v.Arm = clip(f.A, 40)
			}
			if f.Len > 0 && f.Len <= maxVideoS {
				v.LenS = max(v.LenS, int(f.Len))
			}
			if f.Pitch != nil && *f.Pitch >= 0 && *f.Pitch <= maxVideoS {
				p := int(*f.Pitch)
				v.PitchS = &p
			}
			switch f.Ev {
			case "autoplay":
				v.Autoplayed = true
			case "play", "resume":
				v.Played = true
				step(e.LP, "play:"+id, e.ReceivedAt)
			case "pitch":
				v.ReachedPitch = true
				step(e.LP, "pitch:"+id, e.ReceivedAt)
			case "end":
				step(e.LP, "end:"+id, e.ReceivedAt)
			case "beat":
				v.Watched = addRanges(v.Watched, f.W)
			}
		}
	}
	for _, id := range order {
		v := videos[id]
		v.WatchedS, v.LastS = 0, -1
		for _, r := range v.Watched {
			v.WatchedS += r[1] - r[0]
			v.LastS = max(v.LastS, r[1]-1)
		}
		if v.PitchS != nil && covers(v.Watched, *v.PitchS) {
			v.ReachedPitch = true
		}
		j.Videos = append(j.Videos, *v)
	}
	if n := len(j.Steps); n > 0 {
		j.LastStep = j.Steps[n-1].Step
	}
	var why []string
	if webdriver {
		why = append(why, "automated browser")
	}
	if ua == "" {
		why = append(why, "no user agent")
	} else if botUA.MatchString(ua) {
		why = append(why, "bot user agent")
	}
	if !j.HadInput && j.VisibleMS < 1000 {
		why = append(why, "no input and under 1 s in view")
	}
	j.BotSuspect, j.BotReason = len(why) > 0, strings.Join(why, ", ")
	return j
}

// addRanges merges the page script's watched ranges into rs, dropping any
// that are not whole seconds inside 0 to 6 hours.
func addRanges(rs [][2]int, in [][]json.Number) [][2]int {
	for _, r := range in {
		if len(r) != 2 {
			continue
		}
		a, err1 := r[0].Int64()
		b, err2 := r[1].Int64()
		if err1 != nil || err2 != nil || a < 0 || b <= a || b > maxVideoS {
			continue
		}
		rs = append(rs, [2]int{int(a), int(b)})
	}
	sort.Slice(rs, func(i, k int) bool { return rs[i][0] < rs[k][0] })
	var out [][2]int
	for _, r := range rs {
		if n := len(out); n > 0 && r[0] <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], r[1])
			continue
		}
		out = append(out, r)
	}
	return out
}

func covers(rs [][2]int, s int) bool {
	for _, r := range rs {
		if s >= r[0] && s < r[1] {
			return true
		}
	}
	return false
}
