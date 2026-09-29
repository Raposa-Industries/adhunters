// Package credit reads how much is left on the prepaid services the platform
// runs on (proxy traffic, AI credit) and when the subscriptions renew, from
// one small file (credits.conf) that names each check.
//
//	[credit iproyal]
//	url    = https://resi-api.iproyal.com/v1/me
//	header = Authorization: Bearer ${IPROYAL_API_TOKEN}
//	field  = available_traffic
//	unit   = GB
//	warn   = 2
//
//	[renewal proxies-datacenter]
//	due    = 2026-10-25
//	every  = month
//	remind = 7d
//
// ${NAME} is read from the environment. A check whose URL or headers name an
// unset or FILL_ME variable, or a renewal whose due date is FILL_ME, is off:
// the example file lists every service, and each starts working once its key
// is filled in.
package credit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Check reads one balance from a JSON API.
type Check struct {
	Name    string
	URL     string
	Headers [][2]string // name, value
	Field   string      // dotted path in the answer, e.g. credits.current_balance
	Scale   float64     // the answer times Scale is in Unit (default 1)
	Unit    string
	Warn    float64 // CreditLow fires below this
	Off     string  // why the check is off; empty when it runs
}

// Renewal is a subscription that has to be renewed (or paid) on a date.
type Renewal struct {
	Name   string
	Due    time.Time // a renewal day; with Every, any one of them
	Every  string    // week, month, year or none
	Remind time.Duration
	Off    string
}

// Config is the whole file.
type Config struct {
	Checks   []Check
	Renewals []Renewal
}

// Load reads a credits file; getenv resolves ${NAME}.
func Load(path string, getenv func(string) string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return Parse(f, getenv)
}

// Parse reads a credits file from r.
func Parse(r io.Reader, getenv func(string) string) (*Config, error) {
	c := &Config{}
	var kind, name string
	keys := map[string][]string{}
	flush := func() error {
		if kind == "" {
			return nil
		}
		var err error
		switch kind {
		case "credit":
			var ch Check
			ch, err = check(name, keys, getenv)
			c.Checks = append(c.Checks, ch)
		case "renewal":
			var rn Renewal
			rn, err = renewal(name, keys)
			c.Renewals = append(c.Renewals, rn)
		}
		if err != nil {
			return fmt.Errorf("[%s %s]: %w", kind, name, err)
		}
		return nil
	}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if err := flush(); err != nil {
				return nil, err
			}
			f := strings.Fields(strings.Trim(line, "[]"))
			if len(f) != 2 || (f[0] != "credit" && f[0] != "renewal") {
				return nil, fmt.Errorf("line %d: want [credit NAME] or [renewal NAME]", n)
			}
			kind, name, keys = f[0], f[1], map[string][]string{}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || kind == "" {
			return nil, fmt.Errorf("line %d: want key = value inside a section", n)
		}
		k = strings.TrimSpace(k)
		keys[k] = append(keys[k], strings.TrimSpace(v))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, ch := range c.Checks {
		if seen["credit "+ch.Name] {
			return nil, fmt.Errorf("[credit %s] twice", ch.Name)
		}
		seen["credit "+ch.Name] = true
	}
	for _, rn := range c.Renewals {
		if seen["renewal "+rn.Name] {
			return nil, fmt.Errorf("[renewal %s] twice", rn.Name)
		}
		seen["renewal "+rn.Name] = true
	}
	return c, nil
}

func one(keys map[string][]string, k string) string {
	if v := keys[k]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

func check(name string, keys map[string][]string, getenv func(string) string) (Check, error) {
	ch := Check{Name: name, Field: one(keys, "field"), Unit: one(keys, "unit"), Scale: 1}
	for _, k := range []string{"url", "field", "unit", "warn"} {
		if one(keys, k) == "" {
			return ch, fmt.Errorf("%s is missing", k)
		}
	}
	var err error
	if ch.Warn, err = strconv.ParseFloat(one(keys, "warn"), 64); err != nil {
		return ch, fmt.Errorf("warn: %w", err)
	}
	if s := one(keys, "scale"); s != "" {
		if ch.Scale, err = strconv.ParseFloat(s, 64); err != nil {
			return ch, fmt.Errorf("scale: %w", err)
		}
	}
	var unset []string
	expand := func(s string) string {
		return os.Expand(s, func(v string) string {
			x := getenv(v)
			if x == "" || x == "FILL_ME" {
				unset = append(unset, v)
			}
			return x
		})
	}
	ch.URL = expand(one(keys, "url"))
	for _, h := range keys["header"] {
		hk, hv, ok := strings.Cut(h, ":")
		if !ok {
			return ch, fmt.Errorf("header %q: want Name: value", hk)
		}
		ch.Headers = append(ch.Headers, [2]string{strings.TrimSpace(hk), expand(strings.TrimSpace(hv))})
	}
	if len(unset) > 0 {
		sort.Strings(unset)
		ch.Off = strings.Join(unset, ", ") + " not set"
	}
	return ch, nil
}

func renewal(name string, keys map[string][]string) (Renewal, error) {
	rn := Renewal{Name: name, Every: one(keys, "every"), Remind: 7 * 24 * time.Hour}
	switch rn.Every {
	case "":
		rn.Every = "none"
	case "none", "week", "month", "year":
	default:
		return rn, fmt.Errorf("every %q: want week, month, year or none", rn.Every)
	}
	if s := one(keys, "remind"); s != "" {
		d, err := days(s)
		if err != nil {
			return rn, fmt.Errorf("remind: %w", err)
		}
		rn.Remind = d
	}
	due := one(keys, "due")
	switch due {
	case "":
		return rn, errors.New("due is missing")
	case "FILL_ME":
		rn.Off = "due not set"
		return rn, nil
	}
	t, err := time.Parse("2006-01-02", due)
	if err != nil {
		return rn, fmt.Errorf("due: want YYYY-MM-DD: %w", err)
	}
	rn.Due = t
	return rn, nil
}

// days reads "7d" or a Go duration ("36h").
func days(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		d, err := strconv.Atoi(n)
		if err != nil {
			return 0, err
		}
		return time.Duration(d) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// Next is the first renewal day at or after the start of now's day (UTC).
// A one-off renewal keeps its date, so once past it reads as overdue.
func (r Renewal) Next(now time.Time) time.Time {
	today := now.UTC().Truncate(24 * time.Hour)
	t := r.Due
	for i := 0; t.Before(today) && i < 10000; i++ {
		switch r.Every {
		case "week":
			t = t.AddDate(0, 0, 7)
		case "month":
			t = addMonth(r.Due, i+1)
		case "year":
			t = r.Due.AddDate(i+1, 0, 0)
		default:
			return t
		}
	}
	return t
}

// addMonth adds n months to t, keeping the day where the month allows (a
// renewal on the 31st falls on the 30th in April, not on 1 May).
func addMonth(t time.Time, n int) time.Time {
	first := time.Date(t.Year(), t.Month()+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	d := t.Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, time.UTC)
}

// Read asks the API and returns the balance in c.Unit. Errors carry the HTTP
// status, never the answer's body or the request's headers.
func (c Check) Read(ctx context.Context, hc *http.Client) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return 0, errors.New("bad url")
	}
	req.Header.Set("Accept", "application/json")
	for _, h := range c.Headers {
		req.Header.Set(h[0], h[1])
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue interface{ Unwrap() error }
		if errors.As(err, &ue) && ue.Unwrap() != nil {
			// *url.Error repeats the URL, which may carry a key.
			return 0, fmt.Errorf("request: %w", ue.Unwrap())
		}
		return 0, errors.New("request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, fmt.Errorf("read answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return 0, errors.New("answer is not JSON")
	}
	x, err := field(v, c.Field)
	if err != nil {
		return 0, err
	}
	return x * c.Scale, nil
}

// field follows a dotted path; a number or a numeric string at the end is
// the value. When a step is missing, the error lists the keys that are there
// (never their values), so the path can be fixed from the log.
func field(v any, path string) (float64, error) {
	for _, step := range strings.Split(path, ".") {
		switch m := v.(type) {
		case map[string]any:
			next, ok := m[step]
			if !ok {
				have := make([]string, 0, len(m))
				for k := range m {
					have = append(have, k)
				}
				sort.Strings(have)
				return 0, fmt.Errorf("no %q in the answer; it has: %s", step, strings.Join(have, ", "))
			}
			v = next
		case []any:
			i, err := strconv.Atoi(step)
			if err != nil || i < 0 || i >= len(m) {
				return 0, fmt.Errorf("%q: the answer has a list of %d here", step, len(m))
			}
			v = m[i]
		default:
			return 0, fmt.Errorf("%q: the answer has no object here", step)
		}
	}
	switch x := v.(type) {
	case float64:
		return x, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, fmt.Errorf("%s is not a number", path)
		}
		return f, nil
	}
	return 0, fmt.Errorf("%s is not a number", path)
}
