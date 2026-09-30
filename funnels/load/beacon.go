// Package load turns raw files into journeys and counts: it archives the
// edge's sealed files, parses each beacon into events, rebuilds the journeys
// of each dirty hour from their events and computes that hour's counts.
// Parsing reads only the archive, so any range can be loaded again.
package load

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Raposa-Industries/adhunters/funnels/edge"
)

// Event is one event of one beacon, with the beacon's fields it needs.
type Event struct {
	Line       int
	N          int
	ReceivedAt time.Time
	SentAt     *time.Time
	Journey    string
	Site       string
	LP         string
	URL        string
	Kind       string
	ClickID    string
	Subs       map[string]string
	UA         string
	Country    string
	IPHash     string
	Net        string
	Webdriver  bool
	HadInput   bool
	ScreenW    *int
	Data       json.RawMessage
}

// beacon is what the page script sends (schema 1).
type beacon struct {
	V      int               `json:"v"`
	J      string            `json:"j"`
	Site   string            `json:"site"`
	LP     any               `json:"lp"`
	URL    string            `json:"url"`
	C      string            `json:"c"`
	S      map[string]any    `json:"s"`
	In     int               `json:"in"`
	WD     int               `json:"wd"`
	SW     *int              `json:"sw"`
	Events []json.RawMessage `json:"e"`
}

var journeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// ErrBadBeacon wraps why a beacon was not parsed. The raw file keeps it.
var ErrBadBeacon = errors.New("bad beacon")

const maxEvents = 100

// Parse reads one raw line into its events.
func Parse(line []byte, lineNo int) ([]Event, error) {
	var rec edge.Record
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, fmt.Errorf("%w: record: %v", ErrBadBeacon, err)
	}
	body, err := rec.RawBody()
	if err != nil {
		return nil, fmt.Errorf("%w: body: %v", ErrBadBeacon, err)
	}
	if rec.Truncated {
		return nil, fmt.Errorf("%w: cut at %d bytes", ErrBadBeacon, len(body))
	}
	var b beacon
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBeacon, err)
	}
	if b.V != 1 {
		return nil, fmt.Errorf("%w: schema %d", ErrBadBeacon, b.V)
	}
	if !journeyRe.MatchString(b.J) {
		return nil, fmt.Errorf("%w: journey %q", ErrBadBeacon, clip(b.J, 80))
	}
	if len(b.Events) == 0 || len(b.Events) > maxEvents {
		return nil, fmt.Errorf("%w: %d events", ErrBadBeacon, len(b.Events))
	}
	site := strings.ToLower(clip(b.Site, 253))
	if site == "" {
		site = rec.Host
	}
	lp := ""
	switch v := b.LP.(type) {
	case string:
		lp = v
	case float64:
		lp = fmt.Sprint(v)
	}
	lp = clip(lp, 200)
	if lp == "" {
		lp = "/"
	}
	subs := map[string]string{}
	for k, v := range b.S {
		if s, ok := v.(string); ok && len(k) <= 20 && s != "" {
			subs[k] = clip(s, 200)
		}
	}
	out := make([]Event, 0, len(b.Events))
	for i, raw := range b.Events {
		var head struct {
			K string  `json:"k"`
			T float64 `json:"t"`
		}
		if err := json.Unmarshal(raw, &head); err != nil || head.K == "" {
			return nil, fmt.Errorf("%w: event %d", ErrBadBeacon, i)
		}
		e := Event{
			Line: lineNo, N: i, ReceivedAt: rec.At.UTC(), Journey: b.J, Site: site, LP: lp,
			URL: clip(b.URL, 1000), Kind: clip(head.K, 20), ClickID: clip(b.C, 100), Subs: subs,
			UA: rec.UA, Country: rec.Country, IPHash: rec.IPHash, Net: rec.Net,
			Webdriver: b.WD == 1, HadInput: b.In == 1, ScreenW: b.SW, Data: raw,
		}
		if head.T > 0 {
			t := time.UnixMilli(int64(head.T)).UTC()
			e.SentAt = &t
		}
		out = append(out, e)
	}
	return out, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
