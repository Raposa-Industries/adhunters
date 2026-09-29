package redtrack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Paths of RedTrack's endpoints, as used by the mcp-redtrack client and
// RedTrack's API reference.
const (
	PathReport      = "/report"      // grouped numbers: clicks, conversions, cost, revenue…
	PathConversions = "/conversions" // one row per conversion, max 10 000 per page
	PathClicks      = "/tracks"      // one row per click, max 10 000 per page
	PathCampaigns   = "/campaigns"
	PathOffers      = "/offers"
	PathSources     = "/sources" // traffic sources (Taboola, NewsBreak…)
	PathNetworks    = "/networks"
	PathLandings    = "/landings"
	PathSettings    = "/me/settings" // time zone, currency, conversion types
)

// Groups RedTrack reports can be grouped by, all accepted live on
// 2026-09-29, also combined (campaign,date,hour_of_day). sub1…sub20 carry
// what the tracking code put in them (for Taboola: the campaign, item and
// site ids). The hour is hour_of_day: an unknown group, "hour" included,
// answers HTTP 500 {"error":"Problem with loading report"}.
var Groups = []string{
	"campaign", "offer", "source", "landing", "network",
	"country", "region", "city", "os", "browser", "device", "device_brand", "connection_type", "isp",
	"date", "hour_of_day", "day_of_week",
	"rt_source", "rt_medium", "rt_campaign", "rt_adgroup", "rt_ad", "rt_placement", "rt_keyword",
}

// Day is a calendar day in the account's (or the query's) time zone.
type Day struct{ Y, M, D int }

// DayOf returns t's calendar day.
func DayOf(t time.Time) Day { return Day{t.Year(), int(t.Month()), t.Day()} }

func (d Day) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Y, d.M, d.D) }

// ReportQuery asks /report for numbers over whole days, grouped by one or
// more groups (comma-joined, as RedTrack takes them).
type ReportQuery struct {
	Group    []string
	From, To Day
	// Timezone overrides the account's, e.g. "America/Sao_Paulo".
	Timezone string
	// Filters narrow the rows: campaign_id, source_id, offer_id, sub1…
	Filters url.Values
}

// Values is the query string for q, without paging.
func (q ReportQuery) Values() url.Values {
	v := url.Values{}
	for k, vs := range q.Filters {
		v[k] = vs
	}
	v.Set("group", strings.Join(q.Group, ","))
	v.Set("date_from", q.From.String())
	v.Set("date_to", q.To.String())
	if q.Timezone != "" {
		v.Set("timezone", q.Timezone)
	}
	return v
}

// Report fetches every row of q, 1000 per page (RedTrack's maximum).
func (c *Client) Report(ctx context.Context, q ReportQuery, fn func(*Response, []Row) error) error {
	return c.Pages(ctx, PathReport, q.Values(), 1000, 0, rowsFn(fn))
}

// LogQuery asks /conversions or /tracks for single events over whole days.
type LogQuery struct {
	From, To Day
	Filters  url.Values
}

func (q LogQuery) values() url.Values {
	v := url.Values{}
	for k, vs := range q.Filters {
		v[k] = vs
	}
	v.Set("date_from", q.From.String())
	v.Set("date_to", q.To.String())
	return v
}

// Conversions fetches every conversion in q, 10 000 per page.
func (c *Client) Conversions(ctx context.Context, q LogQuery, fn func(*Response, []Row) error) error {
	return c.Pages(ctx, PathConversions, q.values(), 10000, 0, rowsFn(fn))
}

// Clicks fetches every click in q, 10 000 per page.
func (c *Client) Clicks(ctx context.Context, q LogQuery, fn func(*Response, []Row) error) error {
	return c.Pages(ctx, PathClicks, q.values(), 10000, 0, rowsFn(fn))
}

func rowsFn(fn func(*Response, []Row) error) func(*Response, []json.RawMessage) error {
	return func(r *Response, items []json.RawMessage) error {
		rows := make([]Row, 0, len(items))
		for i, it := range items {
			var row Row
			if err := json.Unmarshal(it, &row); err != nil {
				return fmt.Errorf("redtrack %s row %d: %w", r.Path, i, err)
			}
			rows = append(rows, row)
		}
		return fn(r, rows)
	}
}

// Row is one report or log row with its fields as received.
type Row map[string]json.RawMessage

// String returns field k as text: strings as they are, numbers as written,
// "" when missing or null.
func (r Row) String(k string) string {
	raw, ok := r[k]
	if !ok || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// Float returns field k as a number. RedTrack writes some numbers as
// strings, so both are read. ok is false when the field is missing, null,
// or not a number.
func (r Row) Float(k string) (f float64, ok bool) {
	s := r.String(k)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}
