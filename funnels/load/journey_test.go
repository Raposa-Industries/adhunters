package load

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Raposa-Industries/adhunters/funnels/edge"
)

var t0 = time.Date(2026, 9, 30, 14, 10, 0, 0, time.UTC)

// line makes one raw line, as the edge writes it, holding body.
func line(t *testing.T, at time.Time, ua string, body any) []byte {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := edge.Record{ID: "x", At: at, Host: "lp.example.com", UA: ua, Country: "US", IPHash: "h", Net: "203.0.113.0/24"}
	rec.SetBody(b)
	out, err := rec.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func beaconOf(j, lp string, in int, events ...map[string]any) map[string]any {
	return map[string]any{"v": 1, "j": j, "site": "lp.example.com", "lp": lp, "url": "/" + lp + "?clickid=c1",
		"c": "c1", "s": map[string]any{"sub1": "50549004", "sub4": "777", "sub8": "1322"}, "in": in, "wd": 0, "sw": 390, "e": events}
}

const iphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148"

func parse(t *testing.T, raw []byte, n int) []Event {
	t.Helper()
	ev, err := Parse(raw, n)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestParseRefusesWhatIsNotABeacon(t *testing.T) {
	for name, body := range map[string]any{
		"schema":     map[string]any{"v": 2, "j": "abcdefgh12", "e": []any{map[string]any{"k": "view"}}},
		"journey":    map[string]any{"v": 1, "j": "../x", "e": []any{map[string]any{"k": "view"}}},
		"no events":  map[string]any{"v": 1, "j": "abcdefgh12", "e": []any{}},
		"event kind": map[string]any{"v": 1, "j": "abcdefgh12", "e": []any{map[string]any{"t": 1}}},
	} {
		if _, err := Parse(line(t, t0, iphone, body), 1); !errors.Is(err, ErrBadBeacon) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := Parse([]byte("not json\n"), 1); !errors.Is(err, ErrBadBeacon) {
		t.Errorf("garbage: %v", err)
	}
	ev := parse(t, line(t, t0, iphone, beaconOf("abcdefgh12", "bp-02", 0, map[string]any{"k": "view", "t": 1790000000000})), 3)
	if len(ev) != 1 || ev[0].Line != 3 || ev[0].LP != "bp-02" || ev[0].Subs["sub4"] != "777" || ev[0].SentAt == nil || ev[0].Country != "US" {
		t.Errorf("event = %+v", ev)
	}
}

func TestBuildFollowsAJourneyAcrossPagesAndTheVideo(t *testing.T) {
	j := "journey0001"
	var evs []Event
	add := func(at time.Time, lp string, in int, events ...map[string]any) {
		evs = append(evs, parse(t, line(t, at, iphone, beaconOf(j, lp, in, events...)), len(evs)+1)...)
	}
	add(t0, "vsl", 0, map[string]any{"k": "view"},
		map[string]any{"k": "video", "id": "bp-02", "ev": "load", "len": 1260, "pitch": 840, "a": "b"},
		map[string]any{"k": "video", "id": "bp-02", "ev": "autoplay"})
	add(t0.Add(5*time.Second), "vsl", 1, map[string]any{"k": "beat", "vis": 5000, "sc": 10},
		map[string]any{"k": "video", "id": "bp-02", "ev": "play"})
	add(t0.Add(10*time.Second), "vsl", 1, map[string]any{"k": "beat", "vis": 5000, "sc": 30},
		map[string]any{"k": "scroll", "m": 25},
		map[string]any{"k": "video", "id": "bp-02", "ev": "beat", "w": [][]int{{0, 300}}})
	// seeking ahead, then back over what was heard: counted once
	add(t0.Add(15*time.Minute), "vsl", 1, map[string]any{"k": "video", "id": "bp-02", "ev": "beat", "w": [][]int{{600, 845}, {200, 310}, {9, 5}}},
		map[string]any{"k": "video", "id": "bp-02", "ev": "pitch"},
		map[string]any{"k": "click", "n": "buy"})
	add(t0.Add(16*time.Minute), "checkout", 1, map[string]any{"k": "view"},
		map[string]any{"k": "form", "n": "order"},
		map[string]any{"k": "exit", "vis": 4000, "sc": 60})

	got := Build(evs)
	if got.ID != j || got.Hour != t0.Truncate(time.Hour) || got.Device != "phone" || got.ClickID != "c1" || got.Subs["sub8"] != "1322" {
		t.Errorf("journey = %+v", got)
	}
	if got.FirstLP != "vsl" || got.LastLP != "checkout" || got.LPs != 2 || got.VisibleMS != 14000 || got.MaxScroll != 60 || !got.HadInput || got.BotSuspect {
		t.Errorf("journey = %+v", got)
	}
	var steps []string
	for _, s := range got.Steps {
		steps = append(steps, s.LP+"/"+s.Step)
	}
	want := "vsl/view vsl/play:bp-02 vsl/stay10s vsl/scroll25 vsl/pitch:bp-02 vsl/click:buy checkout/view checkout/form:order"
	if strings.Join(steps, " ") != want {
		t.Errorf("steps\n got %s\nwant %s", strings.Join(steps, " "), want)
	}
	if got.LastStep != "form:order" {
		t.Errorf("last step = %s", got.LastStep)
	}
	if len(got.Videos) != 1 {
		t.Fatalf("videos = %+v", got.Videos)
	}
	v := got.Videos[0]
	if v.Arm != "b" || v.LenS != 1260 || v.PitchS == nil || *v.PitchS != 840 || !v.Autoplayed || !v.Played || !v.ReachedPitch {
		t.Errorf("video = %+v", v)
	}
	if v.WatchedS != 310+245 || v.LastS != 844 || len(v.Watched) != 2 {
		t.Errorf("watched = %v (%d s, last %d)", v.Watched, v.WatchedS, v.LastS)
	}
}

func TestBuildFlagsLikelyBots(t *testing.T) {
	ev := parse(t, line(t, t0, "python-requests/2.31", beaconOf("journey0002", "vsl", 0, map[string]any{"k": "view"})), 1)
	got := Build(ev)
	if !got.BotSuspect || got.BotReason != "bot user agent, no input and under 1 s in view" || got.LastStep != "view" {
		t.Errorf("bot = %v %q", got.BotSuspect, got.BotReason)
	}
	b := beaconOf("journey0003", "vsl", 1, map[string]any{"k": "view"}, map[string]any{"k": "beat", "vis": 5000})
	b["wd"] = 1
	got = Build(parse(t, line(t, t0, iphone, b), 1))
	if got.BotReason != "automated browser" {
		t.Errorf("webdriver: %q", got.BotReason)
	}
}

func TestDevice(t *testing.T) {
	w := 390
	for ua, want := range map[string]string{
		iphone: "phone",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) Mobile Safari/537.36": "phone",
		"Mozilla/5.0 (Linux; Android 13; SM-X200) Safari/537.36":        "tablet",
		"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X)":                 "tablet",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/129.0":        "desktop",
	} {
		if got := Device(ua, nil); got != want {
			t.Errorf("%s: %s, want %s", ua, got, want)
		}
	}
	if Device("", &w) != "phone" {
		t.Error("narrow screen without user agent")
	}
}
