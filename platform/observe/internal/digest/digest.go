// Package digest writes the 08:00 message: the last 24 hours in numbers.
// On a quiet day it is three lines saying all green.
package digest

import (
	"context"
	"fmt"
	"html"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Raposa-Industries/adhunters/platform/observe/internal/prom"
	"github.com/Raposa-Industries/adhunters/platform/observe/internal/sentry"
)

// Metrics answers PromQL queries (prom.Client).
type Metrics interface {
	Query(ctx context.Context, q string, t time.Time) ([]prom.Sample, error)
}

// Errors lists new Sentry issues (sentry.Client); nil leaves errors out.
type Errors interface {
	NewSince(ctx context.Context, since time.Time) ([]sentry.Issue, error)
}

// The queries, each over the 24 hours before the digest.
const (
	qScrapes   = `sum(increase(tracks_capture_scrapes_total[24h]))`
	qScrapesOK = `sum(increase(tracks_capture_scrapes_total{outcome="ok"}[24h]))`
	// Minutes in which no box made a successful scrape.
	qGaps      = `sum_over_time(((sum(rate(tracks_capture_scrapes_total{outcome="ok"}[2m])) or vector(0)) == bool 0)[24h:1m])`
	qSightings = `sum(increase(tracks_loader_sightings_total[24h]))`
	qLagMax    = `max_over_time(max(tracks_loader_lag_seconds)[24h:5m])`
	qVisits    = `sum(increase(raposa_visits_total[24h]))`
	qKept      = `sum(increase(raposa_keeps_total{result="complete"}[24h]))`
	// Minutes each alert spent firing (rules are evaluated every minute).
	qAlerts   = `sum by (alertname) (count_over_time(ALERTS{alertstate="firing",alertname!="Heartbeat"}[24h]))`
	qRestarts = `sum by (service) (changes(process_start_time_seconds{job="adhunters"}[24h])) > 0`
	qDisk     = `min by (box) (node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"})`
	// What is left on each prepaid service (observe-bot's own credit checks),
	// with how="estimated" on the ones worked out from our own spending.
	qCredits = `label_replace(adhunters_credit_remaining unless on (credit) adhunters_credit_estimated, "how", "read", "", "")` +
		` or label_replace(adhunters_credit_remaining and on (credit) adhunters_credit_estimated, "how", "estimated", "", "")`
)

// Write returns the digest for the 24 hours ending at t, in Telegram HTML.
// A query that fails reads "unknown" rather than failing the digest.
func Write(ctx context.Context, m Metrics, e Errors, t time.Time, loc *time.Location) string {
	r := reader{ctx: ctx, m: m, t: t}
	scrapes, okScrapes := r.one(qScrapes), r.one(qScrapesOK)
	gaps, sightings, lag := r.one(qGaps), r.one(qSightings), r.one(qLagMax)
	visits, kept := r.one(qVisits), r.one(qKept)
	alerts, restarts, disk := r.many(qAlerts, "alertname"), r.many(qRestarts, "service"), r.many(qDisk, "box")
	credits := r.credits()

	var issues []sentry.Issue
	issuesKnown := e != nil
	if e != nil {
		var err error
		if issues, err = e.NewSince(ctx, t.Add(-24*time.Hour)); err != nil {
			issuesKnown = false
			r.failed = true
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "☀️ <b>AdHunters, the last 24 hours</b> (to %s)\n", t.In(loc).Format("Mon 2 Jan 15:04"))

	quiet := !r.failed && len(alerts) == 0 && len(issues) == 0 && len(restarts) == 0 &&
		gaps.ok && gaps.v == 0 && scrapes.ok && scrapes.v > 0
	if quiet {
		fmt.Fprintf(&b, "All green: %s scrapes, %s sightings, no alerts, no new errors.\n", count(scrapes), count(sightings))
		fmt.Fprintf(&b, "Disk free: %s", diskLine(disk))
		if len(credits) > 0 {
			fmt.Fprintf(&b, "\nCredit left: %s", strings.Join(credits, ", "))
		}
		return b.String()
	}

	b.WriteString("\n<b>Collection</b>\n")
	fmt.Fprintf(&b, "Scrapes: %s, %s successful\n", count(scrapes), share(okScrapes, scrapes))
	fmt.Fprintf(&b, "Minutes without a successful scrape: %s\n", count(gaps))
	fmt.Fprintf(&b, "Sightings loaded: %s; loader at most %s behind\n", count(sightings), dur(lag))
	if visits.ok && visits.v > 0 {
		fmt.Fprintf(&b, "Raposa: %s visits, %s pages kept whole\n", count(visits), count(kept))
	}

	b.WriteString("\n<b>Alerts</b>\n")
	switch {
	case alerts == nil:
		b.WriteString("unknown\n")
	case len(alerts) == 0:
		b.WriteString("none fired\n")
	default:
		for _, a := range alerts {
			fmt.Fprintf(&b, "%s: firing %s\n", html.EscapeString(a.key), dur(val{a.v * 60, true}))
		}
	}

	b.WriteString("\n<b>New errors</b>\n")
	switch {
	case !issuesKnown && e != nil:
		b.WriteString("unknown (Sentry did not answer)\n")
	case e == nil:
		b.WriteString("Sentry not connected\n")
	case len(issues) == 0:
		b.WriteString("none\n")
	default:
		for i, is := range issues {
			if i == 5 {
				fmt.Fprintf(&b, "and %d more\n", len(issues)-5)
				break
			}
			fmt.Fprintf(&b, "<a href=\"%s\">%s</a> %s (%d times)\n", html.EscapeString(is.Permalink),
				html.EscapeString(is.ShortID), html.EscapeString(is.Title), is.Count)
		}
	}

	b.WriteString("\n<b>Boxes</b>\n")
	if len(restarts) > 0 {
		var parts []string
		for _, s := range restarts {
			parts = append(parts, fmt.Sprintf("%s %.0f", html.EscapeString(s.key), s.v))
		}
		fmt.Fprintf(&b, "Restarts: %s\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "Disk free: %s", diskLine(disk))
	if len(credits) > 0 {
		fmt.Fprintf(&b, "\nCredit left: %s", strings.Join(credits, ", "))
	}
	if r.failed {
		b.WriteString("\n\nSome numbers could not be read; see observe-bot's log.")
	}
	return b.String()
}

type val struct {
	v  float64
	ok bool
}

type keyed struct {
	key string
	v   float64
}

type reader struct {
	ctx    context.Context
	m      Metrics
	t      time.Time
	failed bool
}

func (r *reader) one(q string) val {
	s, err := r.m.Query(r.ctx, q, r.t)
	if err != nil {
		r.failed = true
		return val{}
	}
	if len(s) == 0 || math.IsNaN(s[0].Value) {
		return val{}
	}
	return val{s[0].Value, true}
}

// many returns the samples by label, sorted by value (largest first), or
// nil when the query failed.
func (r *reader) many(q, label string) []keyed {
	s, err := r.m.Query(r.ctx, q, r.t)
	if err != nil {
		r.failed = true
		return nil
	}
	out := []keyed{}
	for _, x := range s {
		out = append(out, keyed{x.Labels[label], x.Value})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].v != out[j].v {
			return out[i].v > out[j].v
		}
		return out[i].key < out[j].key
	})
	return out
}

// credits reads what is left on each prepaid service, "iproyal 4.2 GB", or
// "openai ~17.5 USD" for an estimate, sorted by name; nil when there are none
// or the query failed.
func (r *reader) credits() []string {
	s, err := r.m.Query(r.ctx, qCredits, r.t)
	if err != nil {
		r.failed = true
		return nil
	}
	var out []string
	for _, x := range s {
		about := ""
		if x.Labels["how"] == "estimated" {
			about = "~"
		}
		out = append(out, fmt.Sprintf("%s %s%s %s", html.EscapeString(x.Labels["credit"]), about,
			strconv.FormatFloat(math.Round(x.Value*10)/10, 'f', -1, 64), html.EscapeString(x.Labels["unit"])))
	}
	sort.Strings(out)
	return out
}

func count(v val) string {
	if !v.ok {
		return "no data"
	}
	n := int64(math.Round(v.v))
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func share(part, whole val) string {
	if !part.ok || !whole.ok || whole.v == 0 {
		return "no data"
	}
	return fmt.Sprintf("%.1f%%", 100*part.v/whole.v)
}

func dur(v val) string {
	if !v.ok {
		return "an unknown time"
	}
	d := time.Duration(v.v) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func diskLine(disk []keyed) string {
	if disk == nil {
		return "unknown"
	}
	if len(disk) == 0 {
		return "no data"
	}
	sort.Slice(disk, func(i, j int) bool { return disk[i].key < disk[j].key })
	var parts []string
	for _, d := range disk {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", html.EscapeString(d.key), 100*d.v))
	}
	return strings.Join(parts, ", ")
}
