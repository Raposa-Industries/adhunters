package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Raposa-Industries/adhunters/intel/taboola"
)

type probe struct {
	c       *taboola.Client
	dir     string
	account string
	days    int
	items   int
	now     time.Time

	results []result
}

// result is one read as the summary shows it.
type result struct {
	Name, Path string
	Status     int
	Err        string
	Rows       int
	Fields     []string
	Meta       map[string]string // top-level scalars beside "results"
	Totals     map[string]float64
	Bytes      int
	Elapsed    time.Duration
	Retries    int
	Limits     map[string]string
}

// reports are the report splits read for the account. maxDays caps a
// split's range where Backstage refuses longer ones: by campaign and hour it
// allows 48 hours at most.
var reports = []struct {
	report, dimension string
	maxDays           int
}{
	{"campaign-summary", "day", 0},
	{"campaign-summary", "campaign_breakdown", 0},
	{"campaign-summary", "campaign_day_breakdown", 0},
	{"campaign-summary", "site_breakdown", 0},
	{"campaign-summary", "campaign_site_day_breakdown", 0},
	{"campaign-summary", "country_breakdown", 0},
	{"campaign-summary", "platform_breakdown", 0},
	{"campaign-summary", "by_hour_of_day", 0},
	{"campaign-summary", "campaign_hour_breakdown", 2},
	{"top-campaign-content", "item_breakdown", 0},
}

// summed are the report numbers the summary adds up, when present.
var summed = []string{"impressions", "visible_impressions", "clicks", "spent", "cpa_actions_num", "conversions_value"}

func (p *probe) run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Join(p.dir, "raw"), 0o750); err != nil {
		return err
	}

	acct, err := p.read(ctx, "account", "users/current/account", func() (*taboola.Response, error) {
		return p.c.CurrentAccount(ctx)
	})
	if err != nil && acct == nil {
		// No answer at all (token refused, host blocked): nothing else will work.
		p.write()
		return err
	}
	if p.account == "" && len(acct) > 0 {
		p.account, _ = acct[0]["account_id"].(string)
	}
	if p.account == "" {
		p.write()
		return errors.New("probe: no account id in users/current/account; pass -account")
	}
	allowed, _ := p.read(ctx, "allowed-accounts", "users/current/allowed-accounts", func() (*taboola.Response, error) {
		return p.c.AllowedAccounts(ctx)
	})

	// A network account lists no campaigns of its own; they live in the
	// advertiser accounts under it, so read those too.
	for _, acct := range campaignAccounts(p.account, allowed) {
		camps, _ := p.read(ctx, "campaigns-"+acct, acct+"/campaigns", func() (*taboola.Response, error) {
			return p.c.Campaigns(ctx, acct)
		})
		for _, id := range pickCampaigns(camps, p.items) {
			p.read(ctx, "items-"+acct+"-"+id, acct+"/campaigns/"+id+"/items/", func() (*taboola.Response, error) {
				return p.c.Items(ctx, acct, id)
			})
		}
	}

	to := p.now
	for _, r := range reports {
		days := p.days
		if r.maxDays > 0 && days > r.maxDays {
			days = r.maxDays
		}
		from := to.AddDate(0, 0, -(days - 1))
		path := fmt.Sprintf("%s/reports/%s/dimensions/%s", p.account, r.report, r.dimension)
		p.read(ctx, r.report+"-"+r.dimension, path, func() (*taboola.Response, error) {
			return p.c.Report(ctx, p.account, r.report, r.dimension, from, to, nil)
		})
		if ctx.Err() != nil {
			break
		}
	}
	return p.write()
}

// read makes one read, saves the answer raw before looking at it, and
// records it for the summary. It returns the rows when the answer was read.
func (p *probe) read(ctx context.Context, name, path string, do func() (*taboola.Response, error)) (taboola.Rows, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	res, err := do()
	r := result{Name: name, Path: path}
	if err != nil {
		r.Err = err.Error()
	}
	var rows taboola.Rows
	if res != nil {
		r.Status, r.Bytes, r.Elapsed, r.Retries = res.Status, len(res.Body), res.Elapsed, res.Retries
		r.Limits = limitHeaders(res.Header)
		if serr := p.save(name, res); serr != nil {
			return nil, serr
		}
		if err == nil {
			rows, err = taboola.Results(res.Body)
			if err != nil {
				r.Err = "not JSON rows: " + err.Error()
			}
			r.Rows, r.Fields = len(rows), rows.Fields()
			r.Meta = topScalars(res.Body)
			r.Totals = totals(rows)
		}
	}
	p.results = append(p.results, r)
	fmt.Fprintf(os.Stderr, "%-42s %3d %6d rows %s\n", name, r.Status, r.Rows, r.Err)
	return rows, err
}

func (p *probe) save(name string, res *taboola.Response) error {
	base := filepath.Join(p.dir, "raw", safe(name))
	if err := os.WriteFile(base+".json", res.Body, 0o640); err != nil {
		return err
	}
	var h strings.Builder
	fmt.Fprintf(&h, "HTTP %d\n", res.Status)
	keys := make([]string, 0, len(res.Header))
	for k := range res.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&h, "%s: %s\n", k, strings.Join(res.Header[k], ", "))
	}
	return os.WriteFile(base+".headers.txt", []byte(h.String()), 0o640)
}

// write renders summary.md from what was read so far.
func (p *probe) write() error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Taboola Backstage read probe\n\nRun %s. Account `%s`. Reports cover the last %d days.\n\n",
		p.now.UTC().Format(time.RFC3339), p.account, p.days)
	b.WriteString("| Read | HTTP | Rows | Bytes | ms | Retries | Error |\n|---|---|---|---|---|---|---|\n")
	for _, r := range p.results {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %s |\n", r.Name, r.Status, r.Rows, r.Bytes,
			r.Elapsed.Milliseconds(), r.Retries, strings.ReplaceAll(r.Err, "|", "/"))
	}
	limits := map[string]string{}
	for _, r := range p.results {
		for k, v := range r.Limits {
			limits[k] = v
		}
	}
	b.WriteString("\n## Rate limit headers\n\n")
	if len(limits) == 0 {
		b.WriteString("None seen.\n")
	}
	for _, k := range sortedKeys(limits) {
		fmt.Fprintf(&b, "- `%s`: %s (last value seen)\n", k, limits[k])
	}
	b.WriteString("\n## Fields per read\n")
	for _, r := range p.results {
		if len(r.Fields) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n`GET /backstage/api/1.0/%s`\n\n", r.Name, r.Path)
		for _, k := range sortedKeys(r.Meta) {
			fmt.Fprintf(&b, "- %s: %s\n", k, r.Meta[k])
		}
		if len(r.Totals) > 0 {
			var parts []string
			for _, k := range summed {
				if v, ok := r.Totals[k]; ok {
					parts = append(parts, fmt.Sprintf("%s %.2f", k, v))
				}
			}
			fmt.Fprintf(&b, "- totals: %s\n", strings.Join(parts, ", "))
		}
		fmt.Fprintf(&b, "\n%s\n", "`"+strings.Join(r.Fields, "`, `")+"`")
	}
	return os.WriteFile(filepath.Join(p.dir, "summary.md"), []byte(b.String()), 0o640)
}

// campaignAccounts is the probed account, then every other allowed account
// with an id.
func campaignAccounts(main string, allowed taboola.Rows) []string {
	out := []string{main}
	for _, row := range allowed {
		if id, _ := row["account_id"].(string); id != "" && id != main {
			out = append(out, id)
		}
	}
	return out
}

// pickCampaigns prefers running campaigns, then the rest, up to n ids.
func pickCampaigns(rows taboola.Rows, n int) []string {
	var running, other []string
	for _, row := range rows {
		id := fmt.Sprint(row["id"])
		if id == "" || id == "<nil>" {
			continue
		}
		if row["status"] == "RUNNING" {
			running = append(running, id)
		} else {
			other = append(other, id)
		}
	}
	ids := append(running, other...)
	if len(ids) > n {
		ids = ids[:n]
	}
	return ids
}

func limitHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		l := strings.ToLower(k)
		if strings.Contains(l, "rate") || strings.Contains(l, "limit") || strings.Contains(l, "retry") || strings.Contains(l, "quota") {
			out[k] = strings.Join(v, ", ")
		}
	}
	return out
}

// topScalars are an answer's top-level strings, numbers and booleans, such
// as a report's time zone and last data update.
func topScalars(body []byte) map[string]string {
	var top map[string]any
	if json.Unmarshal(body, &top) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range top {
		switch v.(type) {
		case string, float64, bool:
			out[k] = fmt.Sprint(v)
		}
	}
	return out
}

func totals(rows taboola.Rows) map[string]float64 {
	out := map[string]float64{}
	for _, row := range rows {
		for _, k := range summed {
			if v, ok := row[k].(float64); ok {
				out[k] += v
			}
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}
