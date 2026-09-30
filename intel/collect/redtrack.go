package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/Raposa-Industries/adhunters/intel/redtrack"
)

// Kinds of RedTrack answers.
const (
	KindRtItemDay      = "redtrack.item_day"
	KindRtSiteDay      = "redtrack.site_day"
	KindRtCampaignHour = "redtrack.campaign_hour"
	KindRtConversions  = "redtrack.conversions"
)

// ErrRedTrackWrite means something tried to send a RedTrack request other
// than a GET. Intel only reads RedTrack; the team's account is never
// written.
var ErrRedTrackWrite = errors.New("redtrack: intel-collect only reads")

// ReadOnly wraps a transport so it refuses every request but a GET.
type ReadOnly struct{ Next http.RoundTripper }

func (r ReadOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return nil, fmt.Errorf("%w: %s %s", ErrRedTrackWrite, req.Method, req.URL.Path)
	}
	next := r.Next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}

// RedTrack collects one RedTrack login, per Taboola id in the sub slots
// (Taboola's preset: sub1 campaign, sub4 item, sub8 site), in each Taboola
// account's time zone so the days line up with Taboola's.
type RedTrack struct {
	Login string
	API   *redtrack.Client
	Spool Spool
	Log   *slog.Logger
	Now   func() time.Time
	// Zones returns the Taboola accounts' time zones, as Taboola names them.
	Zones func() []string
}

// Reports reads per item and per site for the last days days, and per hour
// of the day for today and yesterday.
func (r *RedTrack) Reports(ctx context.Context, days int) error {
	var errs []error
	for _, zone := range r.zones() {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			loc = time.UTC
		}
		today := dayIn(r.Now(), loc)
		for i := 0; i < days; i++ {
			d := today.AddDate(0, 0, -i)
			errs = append(errs, r.report(ctx, KindRtItemDay, zone, d, "sub1,sub4"))
			errs = append(errs, r.report(ctx, KindRtSiteDay, zone, d, "sub1,sub8"))
			if i < 2 {
				errs = append(errs, r.report(ctx, KindRtCampaignHour, zone, d, "sub1,hour_of_day"))
			}
		}
	}
	return errors.Join(errs...)
}

func (r *RedTrack) report(ctx context.Context, kind, zone string, day time.Time, group string) error {
	d := day.Format(time.DateOnly)
	q := url.Values{"group": {group}, "date_from": {d}, "date_to": {d}, "timezone": {IANAZone(zone)}}
	params := map[string]any{"from": d, "to": d, "time_zone": zone, "group": group}
	return r.pages(ctx, kind, redtrack.PathReport, q, 1000, params)
}

// Conversions reads every conversion of yesterday and today.
func (r *RedTrack) Conversions(ctx context.Context) error {
	now := r.Now().UTC()
	from := now.AddDate(0, 0, -1).Format(time.DateOnly)
	to := now.Format(time.DateOnly)
	q := url.Values{"date_from": {from}, "date_to": {to}}
	return r.pages(ctx, KindRtConversions, redtrack.PathConversions, q, 10000, map[string]any{"from": from, "to": to})
}

func (r *RedTrack) pages(ctx context.Context, kind, path string, q url.Values, per int, params map[string]any) error {
	err := r.API.Pages(ctx, path, q, per, 50, func(resp *redtrack.Response, _ []json.RawMessage) error {
		return r.put(kind, resp, params)
	})
	var apiErr *redtrack.APIError
	if errors.As(err, &apiErr) {
		// A refused page is kept too: what RedTrack said is data.
		return fmt.Errorf("redtrack %s: %w", kind, err)
	}
	return err
}

func (r *RedTrack) put(kind string, resp *redtrack.Response, params map[string]any) error {
	p := map[string]any{}
	for k, v := range params {
		p[k] = v
	}
	if pg := resp.Query.Get("page"); pg != "" {
		p["page"] = pg
	}
	a := &Answer{Source: "redtrack", Login: r.Login, Kind: kind, Path: resp.Path + "?" + resp.Query.Encode(),
		Params: p, Status: resp.Status, FetchedAt: r.Now().UTC(), Body: resp.Body}
	return r.Spool.Put(a)
}

func (r *RedTrack) zones() []string {
	var z []string
	if r.Zones != nil {
		z = r.Zones()
	}
	if len(z) == 0 {
		return []string{"UTC"}
	}
	return z
}

// IANAZone turns the old US/… names Taboola uses into the names every
// system knows; RedTrack's timezone parameter wants the latter.
func IANAZone(z string) string {
	switch z {
	case "US/Eastern", "EST5EDT":
		return "America/New_York"
	case "US/Central", "CST6CDT":
		return "America/Chicago"
	case "US/Mountain", "MST7MDT":
		return "America/Denver"
	case "US/Pacific", "PST8PDT":
		return "America/Los_Angeles"
	case "US/Arizona":
		return "America/Phoenix"
	case "Brazil/East":
		return "America/Sao_Paulo"
	}
	return z
}
