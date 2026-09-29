// Command redtrack-probe reads (and, when asked, writes) a RedTrack account
// through the API, to learn what it gives. Every answer is saved raw under
// -out before anything reads it, with the API key left out.
//
//	REDTRACK_API_KEY=… redtrack-probe [-out DIR] [-days 7] [-tz Zone] probe
//	REDTRACK_API_KEY=… redtrack-probe [-out DIR] burst N [PATH]
//	REDTRACK_API_KEY=… redtrack-probe [-out DIR] get PATH [k=v …]
//	REDTRACK_API_KEY=… redtrack-probe [-out DIR] post|put PATH FILE.json   (FILE "-" reads stdin)
//	REDTRACK_API_KEY=… redtrack-probe [-out DIR] delete PATH
//
// probe only reads. post, put and delete change the account: use them only
// on an account whose owner said so.
//
// It is a developer tool run by hand, not a service: no /healthz, and Ctrl-C
// or SIGTERM stops it between requests.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Raposa-Industries/adhunters/intel/redtrack"
)

func main() {
	out := flag.String("out", "redtrack-probe-"+time.Now().UTC().Format("20060102T150405Z"), "folder for the raw answers and summary.md")
	days := flag.Int("days", 7, "probe: days of reports, ending today")
	tz := flag.String("tz", "", "probe: time zone for reports (default: the account's)")
	flag.Parse()

	key := os.Getenv("REDTRACK_API_KEY")
	if key == "" {
		fatal(errors.New("REDTRACK_API_KEY is not set"))
	}
	c := redtrack.New(key)
	if u := os.Getenv("REDTRACK_API_URL"); u != "" {
		c.BaseURL = u
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}
	p := &prober{c: c, out: *out}
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "probe":
		err = p.probe(ctx, *days, *tz)
	case "burst":
		n, path := 20, redtrack.PathSettings
		if len(args) > 1 {
			fmt.Sscan(args[1], &n)
		}
		if len(args) > 2 {
			path = args[2]
		}
		err = p.burst(ctx, n, path)
	case "get", "delete":
		if len(args) < 2 {
			fatal(errors.New("usage: get|delete PATH [k=v …]"))
		}
		q := url.Values{}
		for _, kv := range args[2:] {
			k, v, _ := strings.Cut(kv, "=")
			q.Add(k, v)
		}
		err = p.raw(ctx, strings.ToUpper(args[0]), args[1], q, nil)
	case "post", "put":
		if len(args) != 3 {
			fatal(errors.New("usage: post|put PATH FILE.json"))
		}
		var b []byte
		if args[2] == "-" {
			b, err = io.ReadAll(os.Stdin)
		} else {
			b, err = os.ReadFile(args[2])
		}
		if err == nil {
			err = p.raw(ctx, strings.ToUpper(args[0]), args[1], nil, json.RawMessage(b))
		}
	default:
		fatal(fmt.Errorf("unknown command %q", args[0]))
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "redtrack-probe:", err)
	os.Exit(1)
}

type prober struct {
	c   *redtrack.Client
	out string
	n   int
	sum []string
}

// saved is the file written for each answer.
type saved struct {
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Query      url.Values          `json:"query"`
	Status     int                 `json:"status"`
	At         time.Time           `json:"at"`
	DurationMS int64               `json:"duration_ms"`
	Headers    map[string][]string `json:"headers"`
	Body       json.RawMessage     `json:"body,omitempty"`
	BodyText   string              `json:"body_text,omitempty"`
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func (p *prober) save(r *redtrack.Response, label string) {
	p.n++
	s := saved{Method: r.Method, Path: r.Path, Query: r.Query, Status: r.Status, At: r.At,
		DurationMS: r.Duration.Milliseconds(), Headers: r.Header}
	if json.Valid(r.Body) {
		s.Body = r.Body
	} else {
		s.BodyText = string(r.Body)
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	name := fmt.Sprintf("%03d-%s.json", p.n, strings.Trim(unsafeName.ReplaceAllString(label, "_"), "_"))
	if err := os.WriteFile(filepath.Join(p.out, name), b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
	}
}

// call runs one GET, saves it, and adds a summary line.
func (p *prober) call(ctx context.Context, label, path string, q url.Values) (*redtrack.Response, []json.RawMessage) {
	r, err := p.c.Get(ctx, path, q)
	if r == nil {
		p.line("| %s | error | | %s |", label, err)
		return nil, nil
	}
	p.save(r, label)
	if err != nil {
		p.line("| %s | %d | | %s |", label, r.Status, oneLine(string(r.Body), 160))
		return r, nil
	}
	items, total, ierr := redtrack.Items(r.Body)
	if ierr != nil { // a single object
		p.line("| %s | %d | object, %d ms | %s |", label, r.Status, r.Duration.Milliseconds(), fields(r.Body))
		return r, nil
	}
	rows := fmt.Sprintf("%d rows", len(items))
	if total >= 0 {
		rows += fmt.Sprintf(" of %d", total)
	}
	f := ""
	if len(items) > 0 {
		f = fields(items[0])
	}
	p.line("| %s | %d | %s, %d ms | %s |", label, r.Status, rows, r.Duration.Milliseconds(), f)
	return r, items
}

func (p *prober) line(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	p.sum = append(p.sum, s)
	fmt.Println(s)
}

func (p *prober) probe(ctx context.Context, days int, tz string) error {
	to := time.Now().UTC()
	q := redtrack.ReportQuery{From: redtrack.DayOf(to.AddDate(0, 0, -days+1)), To: redtrack.DayOf(to), Timezone: tz}
	p.line("# RedTrack probe %s\n\nReports cover %s to %s. Raw answers are the numbered files next to this one.\n", time.Now().UTC().Format(time.RFC3339), q.From, q.To)
	p.line("| call | status | answer | fields of the first row |\n|---|---|---|---|")

	p.call(ctx, "settings", redtrack.PathSettings, nil)
	_, camps := p.call(ctx, "campaigns", redtrack.PathCampaigns, url.Values{"per": {"100"}})
	p.call(ctx, "campaigns-v2", redtrack.PathCampaigns+"/v2", url.Values{"per": {"100"}})
	p.call(ctx, "campaigns-total_stat", redtrack.PathCampaigns, url.Values{"per": {"100"}, "total_stat": {"true"},
		"date_from": {q.From.String()}, "date_to": {q.To.String()}})
	_, sources := p.call(ctx, "sources", redtrack.PathSources, nil)
	p.call(ctx, "offers", redtrack.PathOffers, url.Values{"per": {"100"}})
	p.call(ctx, "networks", redtrack.PathNetworks, nil)
	p.call(ctx, "landings", redtrack.PathLandings, nil)
	for _, it := range first(sources, 5) {
		if id := idOf(it); id != "" {
			p.call(ctx, "source-"+id, redtrack.PathSources+"/"+id, nil)
		}
	}
	for _, it := range first(camps, 3) {
		if id := idOf(it); id != "" {
			p.call(ctx, "campaign-"+id, redtrack.PathCampaigns+"/"+id, nil)
		}
	}

	groups := append([]string{}, redtrack.Groups...)
	for i := 1; i <= 10; i++ {
		groups = append(groups, fmt.Sprintf("sub%d", i))
	}
	groups = append(groups, "campaign,date", "campaign,sub1", "campaign,sub2,sub3", "date,hour_of_day", "campaign,rt_ad,rt_placement")
	for _, g := range groups {
		rq := q
		rq.Group = strings.Split(g, ",")
		v := rq.Values()
		v.Set("per", "1000")
		p.call(ctx, "report-"+g, redtrack.PathReport, v)
	}
	lq := url.Values{"date_from": {q.From.String()}, "date_to": {q.To.String()}, "per": {"100"}, "page": {"1"}}
	p.call(ctx, "conversions", redtrack.PathConversions, lq)
	p.call(ctx, "clicks", redtrack.PathClicks, lq)
	return p.writeSummary()
}

// burst sends n quick requests to path with no spacing and no retries, to
// see where RedTrack starts answering 429 and what headers it sends. Limits
// differ by endpoint: /me/settings has none, /report allows about 10 a minute.
func (p *prober) burst(ctx context.Context, n int, path string) error {
	p.c.MinGap, p.c.MaxRetries = 0, 0
	p.c.Limits = nil
	q := url.Values{}
	d := redtrack.DayOf(time.Now().UTC())
	switch path {
	case redtrack.PathReport:
		q = redtrack.ReportQuery{Group: []string{"campaign"}, From: d, To: d}.Values()
	case redtrack.PathConversions, redtrack.PathClicks: // both need dates
		q.Set("date_from", d.String())
		q.Set("date_to", d.String())
	}
	p.line("# RedTrack burst of %d on %s\n\n| # | status | ms | rate headers |\n|---|---|---|---|", n, path)
	for i := 1; i <= n && ctx.Err() == nil; i++ {
		r, err := p.c.Get(ctx, path, q)
		if r == nil {
			p.line("| %d | error | | %v |", i, err)
			continue
		}
		if r.Status != http.StatusOK {
			p.save(r, fmt.Sprintf("burst-%d", i))
		}
		p.line("| %d | %d | %d | %s |", i, r.Status, r.Duration.Milliseconds(), rateHeaders(r.Header))
	}
	return p.writeSummary()
}

func (p *prober) raw(ctx context.Context, method, path string, q url.Values, body any) error {
	r, err := p.c.Do(ctx, method, path, q, body)
	if r != nil {
		p.save(r, strings.ToLower(method)+"-"+path)
		fmt.Printf("HTTP %d (%d ms)\n%s\n", r.Status, r.Duration.Milliseconds(), r.Body)
	}
	return err
}

func (p *prober) writeSummary() error {
	return os.WriteFile(filepath.Join(p.out, "summary.md"), []byte(strings.Join(p.sum, "\n")+"\n"), 0o644)
}

func first(items []json.RawMessage, n int) []json.RawMessage {
	return items[:min(n, len(items))]
}

func idOf(item json.RawMessage) string {
	var o map[string]any
	if json.Unmarshal(item, &o) != nil {
		return ""
	}
	for _, k := range []string{"id", "_id"} {
		if v, ok := o[k]; ok && v != nil {
			return url.PathEscape(fmt.Sprint(v))
		}
	}
	return ""
}

// fields lists an object's keys with a short sample of each value.
func fields(obj json.RawMessage) string {
	var o map[string]json.RawMessage
	if json.Unmarshal(obj, &o) != nil {
		return oneLine(string(obj), 160)
	}
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+oneLine(string(o[k]), 24))
	}
	return strings.ReplaceAll(strings.Join(parts, ", "), "|", "\\|")
}

func rateHeaders(h http.Header) string {
	var parts []string
	for k, v := range h {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "rate") || strings.Contains(lk, "limit") || lk == "retry-after" {
			parts = append(parts, k+"="+strings.Join(v, ","))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}
