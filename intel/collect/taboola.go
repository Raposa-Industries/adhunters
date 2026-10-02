package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/Raposa-Industries/adhunters/intel/taboola"
)

// Kinds of Taboola answers, the loader's key to its parsers.
const (
	KindTbAccounts    = "taboola.accounts"
	KindTbCampaigns   = "taboola.campaigns"
	KindTbGroups      = "taboola.groups"
	KindTbItems       = "taboola.items"
	KindTbCampaignDay = "taboola.campaign_day"
	KindTbSiteDay     = "taboola.site_day"
	KindTbItemDay     = "taboola.item_day"
	KindTbBucket      = "taboola.bucket"
	KindTbHistory     = "taboola.history"
)

// TaboolaReader is the read-only client (intel/taboola); a test stands in.
type TaboolaReader interface {
	Get(ctx context.Context, path string, q url.Values) (*taboola.Response, error)
}

// TbAccount is one advertiser account a login may read.
type TbAccount struct {
	ID       string // alphabetic, e.g. zoltagroup-1-sc
	Type     string
	TimeZone string
	loc      *time.Location
}

// Taboola collects one Taboola login: every advertiser account its key may
// read. It only reads (the client refuses anything else), and stays inside
// its share of Taboola's limits through Pace.
type Taboola struct {
	Login string
	API   TaboolaReader
	Spool Spool
	Pace  *Pacer
	Log   *slog.Logger
	Now   func() time.Time
	// Only, when set, keeps to these advertiser accounts (the ones chosen
	// for a login on Launch's Contas page). Skip leaves out accounts another
	// login already reads, so none is read twice.
	Only map[string]bool
	Skip func(account string) bool

	mu        sync.Mutex
	accounts  []TbAccount
	campaigns map[string][]string // account -> campaign ids, from the last list
}

// Accounts reads which accounts the key may read and keeps the advertiser
// accounts for the other jobs (a network account lists no campaigns).
func (t *Taboola) Accounts(ctx context.Context) error {
	a, err := t.get(ctx, KindTbAccounts, "", "users/current/allowed-accounts", nil, nil)
	if err != nil {
		return err
	}
	var body struct {
		Results []struct {
			AccountID string `json:"account_id"`
			Type      string `json:"type"`
			TimeZone  string `json:"time_zone_name"`
		} `json:"results"`
	}
	if err := json.Unmarshal(a.Body, &body); err != nil {
		return fmt.Errorf("taboola accounts: %w", err)
	}
	var out []TbAccount
	for _, r := range body.Results {
		if r.Type == "NETWORK" || r.AccountID == "" {
			continue
		}
		if (t.Only != nil && !t.Only[r.AccountID]) || (t.Skip != nil && t.Skip(r.AccountID)) {
			continue
		}
		loc, err := time.LoadLocation(r.TimeZone)
		if err != nil {
			t.Log.Warn("unknown account time zone, using UTC", "account", r.AccountID, "time_zone", r.TimeZone)
			loc = time.UTC
		}
		out = append(out, TbAccount{ID: r.AccountID, Type: r.Type, TimeZone: r.TimeZone, loc: loc})
	}
	t.mu.Lock()
	t.accounts = out
	t.mu.Unlock()
	return nil
}

// Known returns the advertiser accounts from the last Accounts, reading
// them first when there are none yet.
func (t *Taboola) Known(ctx context.Context) ([]TbAccount, error) {
	t.mu.Lock()
	acc := t.accounts
	t.mu.Unlock()
	if len(acc) > 0 {
		return acc, nil
	}
	if err := t.Accounts(ctx); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.accounts, nil
}

// Settings reads every account's campaigns, then each listed campaign's
// items. Taboola drops deleted campaigns from the list; the loader notices.
func (t *Taboola) Settings(ctx context.Context) error {
	lists, err := t.campaignLists(ctx)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	for _, l := range lists {
		for _, id := range l.ids {
			path := url.PathEscape(l.account) + "/campaigns/" + url.PathEscape(id) + "/items/"
			if _, err := t.get(ctx, KindTbItems, l.account, path, nil, map[string]any{"campaign_id": id}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Statuses reads every account's campaign groups and campaign list, often,
// so a change of delivery status is seen within minutes (two requests per
// account).
func (t *Taboola) Statuses(ctx context.Context) error {
	accs, err := t.Known(ctx)
	if err != nil {
		return err
	}
	// The groups first: a campaign whose group was deleted stays in the
	// campaign list with its old status (Realize says "Campaign Group Was
	// Deleted"), and only the group list shows the group is gone.
	var errs []error
	for _, acc := range accs {
		if _, err := t.get(ctx, KindTbGroups, acc.ID, url.PathEscape(acc.ID)+"/campaigns_group/", nil, nil); err != nil {
			errs = append(errs, err)
		}
	}
	_, err = t.campaignLists(ctx)
	return errors.Join(append(errs, err)...)
}

type campaignList struct {
	account string
	ids     []string
}

// campaignLists reads each account's campaigns and remembers their ids.
func (t *Taboola) campaignLists(ctx context.Context) ([]campaignList, error) {
	accs, err := t.Known(ctx)
	if err != nil {
		return nil, err
	}
	var out []campaignList
	var errs []error
	for _, acc := range accs {
		a, err := t.get(ctx, KindTbCampaigns, acc.ID, url.PathEscape(acc.ID)+"/campaigns", nil, nil)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rows, err := taboola.Results(a.Body)
		if err != nil {
			errs = append(errs, fmt.Errorf("taboola campaigns %s: %w", acc.ID, err))
			continue
		}
		var ids []string
		for _, r := range rows {
			if id := text(r["id"]); id != "" {
				ids = append(ids, id)
			}
		}
		t.mu.Lock()
		if t.campaigns == nil {
			t.campaigns = map[string][]string{}
		}
		t.campaigns[acc.ID] = ids
		t.mu.Unlock()
		out = append(out, campaignList{account: acc.ID, ids: ids})
	}
	return out, errors.Join(errs...)
}

// Reports reads the daily reports for the last days days (today included):
// campaign by day and campaign by site by day in one call each, and items
// one day per call (the item report has no day split).
func (t *Taboola) Reports(ctx context.Context, days int) error {
	accs, err := t.Known(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, acc := range accs {
		today := dayIn(t.Now(), acc.loc)
		from := today.AddDate(0, 0, -(days - 1))
		errs = append(errs, t.dayReports(ctx, acc, from, today))
	}
	return errors.Join(errs...)
}

// Month reads the month so far again (and the month before, until the 5th,
// while Taboola still adjusts it for billing), so late changes land.
func (t *Taboola) Month(ctx context.Context) error {
	accs, err := t.Known(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, acc := range accs {
		today := dayIn(t.Now(), acc.loc)
		from := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		if today.Day() <= 5 {
			from = from.AddDate(0, -1, 0)
		}
		errs = append(errs, t.dayReports(ctx, acc, from, today))
	}
	return errors.Join(errs...)
}

func (t *Taboola) dayReports(ctx context.Context, acc TbAccount, from, to time.Time) error {
	var errs []error
	base := url.PathEscape(acc.ID) + "/reports/"
	span := map[string]any{"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly), "time_zone": acc.TimeZone}
	q := url.Values{"start_date": {from.Format(time.DateOnly)}, "end_date": {to.Format(time.DateOnly)}}
	if _, err := t.get(ctx, KindTbCampaignDay, acc.ID, base+"campaign-summary/dimensions/campaign_day_breakdown", q, span); err != nil {
		errs = append(errs, err)
	}
	if _, err := t.get(ctx, KindTbSiteDay, acc.ID, base+"campaign-summary/dimensions/campaign_site_day_breakdown", q, span); err != nil {
		errs = append(errs, err)
	}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		day := d.Format(time.DateOnly)
		dq := url.Values{"start_date": {day}, "end_date": {day}}
		p := map[string]any{"from": day, "to": day, "time_zone": acc.TimeZone}
		if _, err := t.get(ctx, KindTbItemDay, acc.ID, base+"top-campaign-content/dimensions/item_breakdown", dq, p); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Realtime reads the last hour of 5-minute buckets per campaign. Without a
// campaign filter Taboola allows one hour at most, and it counts a span that
// starts on the hour and ends in the next one as two hours (22:00 to 23:00 is
// refused, 22:05 to 23:04 is not), so the span starts 55 minutes before the
// end's 5-minute bucket. The runs overlap, and the loader keeps the newest
// copy of each bucket. Taboola keeps these 24 hours.
func (t *Taboola) Realtime(ctx context.Context) error {
	accs, err := t.Known(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, acc := range accs {
		now := t.Now().In(acc.loc)
		end := now.Truncate(time.Minute)
		start := end.Truncate(5 * time.Minute).Add(-55 * time.Minute)
		const layout = "2006-01-02T15:04:05"
		q := url.Values{"start_date": {start.Format(layout)}, "end_date": {end.Format(layout)}}
		p := map[string]any{"start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339), "time_zone": acc.TimeZone}
		path := url.PathEscape(acc.ID) + "/reports/realtime-campaign-summary/dimensions/by_campaign_by_smallest_time_bucket"
		if _, err := t.get(ctx, KindTbBucket, acc.ID, path, q, p); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// History reads the change history of yesterday and today, every page.
// Launch keeps the history of what it does; this is how changes made in
// Taboola's own dashboard reach Intel.
func (t *Taboola) History(ctx context.Context) error {
	accs, err := t.Known(ctx)
	if err != nil {
		return err
	}
	var errs []error
	const pageSize = 500
	for _, acc := range accs {
		today := dayIn(t.Now(), acc.loc)
		from := today.AddDate(0, 0, -1)
		for page := 1; page <= 40; page++ {
			q := url.Values{
				"start_date": {from.Format(time.DateOnly)}, "end_date": {today.Format(time.DateOnly)},
				"campaigns_group_id": {"-1"}, "page": {fmt.Sprint(page)}, "page_size": {fmt.Sprint(pageSize)},
			}
			p := map[string]any{"from": from.Format(time.DateOnly), "to": today.Format(time.DateOnly), "page": page, "time_zone": acc.TimeZone}
			a, err := t.get(ctx, KindTbHistory, acc.ID, url.PathEscape(acc.ID)+"/reports/campaign-history/dimensions/by_account", q, p)
			if err != nil {
				errs = append(errs, err)
				break
			}
			rows, err := taboola.Results(a.Body)
			if err != nil || len(rows) < pageSize {
				break
			}
		}
	}
	return errors.Join(errs...)
}

// get asks, spools the answer (a refused one too, so what Taboola said is
// kept), and returns it. The answer is lost only when the spool cannot be
// written, and then the job fails and runs again next time.
func (t *Taboola) get(ctx context.Context, kind, account, path string, q url.Values, params map[string]any) (*Answer, error) {
	realtime := kind == KindTbBucket
	if err := t.Pace.Wait(ctx, realtime); err != nil {
		return nil, err
	}
	resp, err := t.API.Get(ctx, path, q)
	if resp == nil {
		if err == nil {
			err = errors.New("no answer")
		}
		return nil, fmt.Errorf("taboola %s %s: %w", kind, account, err)
	}
	full := path
	if len(q) > 0 {
		full += "?" + q.Encode()
	}
	a := &Answer{Source: "taboola", Login: t.Login, Account: account, Kind: kind, Path: full, Params: params,
		Status: resp.Status, FetchedAt: t.Now().UTC(), Body: resp.Body}
	if perr := t.Spool.Put(a); perr != nil {
		return nil, fmt.Errorf("taboola %s %s: spool: %w", kind, account, perr)
	}
	if err != nil {
		return a, fmt.Errorf("taboola %s %s: %w", kind, account, err)
	}
	return a, nil
}

// dayIn is t's calendar day in loc, as a UTC midnight.
func dayIn(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// text reads a JSON value as text: strings as they are, numbers as written.
func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}
