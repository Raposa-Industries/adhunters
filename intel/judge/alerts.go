package judge

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Found is one alert condition that holds now.
type Found struct {
	Key      string
	Kind     string
	Account  string
	Campaign int64
	Item     int64
	Title    string
	Detail   string
	Numbers  map[string]any
}

// Usual is an account's usual cost per sale over usual_days: its tracked
// campaigns' cost over their sales (the network's count where RedTrack has
// no rows). Zero when it made no sale, and then no runaway is judged.
func (d *Data) Usual() map[string]float64 {
	cost, sales := map[string]float64{}, map[string]float64{}
	for k, c := range d.campaign {
		if k.window != "30d" {
			continue
		}
		acc := d.Campaigns[k.id].Account
		cost[acc] += c.cost()
		sales[acc] += float64(c.sales(d.tracked[k.id]))
	}
	out := map[string]float64{}
	for acc, s := range sales {
		if s > 0 {
			out[acc] = cost[acc] / s
		}
	}
	return out
}

// recent is Taboola's realtime numbers per campaign.
type recent struct {
	spentToday, spentDelay float64 // today so far; the last sale_delay minutes
	hourClicks             map[time.Time]int64
	lastBucket             time.Time
}

func gatherRecent(ctx context.Context, db *pgxpool.Pool, d *Data, delay time.Duration) (map[int64]*recent, error) {
	rows, err := db.Query(ctx, `SELECT campaign_id, account, bucket, clicks, spent::float8 FROM intel.tb_bucket
		WHERE bucket >= $1`, d.Now.Add(-36*time.Hour))
	if err != nil {
		return nil, err
	}
	out := map[int64]*recent{}
	var (
		id     int64
		acc    string
		bucket time.Time
		clicks int64
		spent  float64
	)
	_, err = pgx.ForEachRow(rows, []any{&id, &acc, &bucket, &clicks, &spent}, func() error {
		a, ok := d.Accounts[acc]
		if !ok {
			return nil
		}
		r := out[id]
		if r == nil {
			r = &recent{hourClicks: map[time.Time]int64{}}
			out[id] = r
		}
		l := bucket.In(a.Loc)
		if time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC).Equal(a.Today) {
			r.spentToday += spent
		}
		if bucket.After(d.Now.Add(-delay)) {
			r.spentDelay += spent
		}
		r.hourClicks[bucket.UTC().Truncate(time.Hour)] += clicks
		if bucket.After(r.lastBucket) {
			r.lastBucket = bucket
		}
		return nil
	})
	return out, err
}

// Detect works out which alert conditions hold now.
func Detect(ctx context.Context, db *pgxpool.Pool, d *Data, s Settings) ([]Found, error) {
	var out []Found
	usual := d.Usual()
	delay := time.Duration(s.get("sale_delay_minutes", 60)) * time.Minute
	rec, err := gatherRecent(ctx, db, d, delay)
	if err != nil {
		return nil, err
	}

	// Runaway: today's spend with no sale today, less the spend too recent to
	// have its sales in, is far past the account's usual cost per sale.
	for id, camp := range d.Campaigns {
		c := d.CampaignCounts(id, "today")
		sales := c.sales(d.tracked[id])
		if sales > 0 {
			continue
		}
		spent := c.cost()
		recentSpend := 0.0
		if r := rec[id]; r != nil {
			spent = math.Max(spent, r.spentToday)
			recentSpend = r.spentDelay
		}
		judged := spent - recentSpend
		u := usual[camp.Account]
		if u <= 0 || judged < s.get("runaway_min_spend", 20) {
			continue
		}
		odds := NoSaleOdds(judged, u)
		if odds >= s.get("runaway_odds", 0.02) {
			continue
		}
		out = append(out, Found{
			Key: fmt.Sprintf("runaway:%d", id), Kind: "runaway", Account: camp.Account, Campaign: id,
			Title: fmt.Sprintf("%s has spent %s today with no sale", campaignName(camp), money(spent)),
			Detail: fmt.Sprintf("A normal campaign in this account (usually %s a sale) gets this far without a sale about %s. Spend in the last %d minutes is left out, as its sales may not be in yet.",
				money(u), oneIn(odds), int(delay.Minutes())),
			Numbers: map[string]any{"spent": round2(spent), "judged_spend": round2(judged), "usual_cost_per_sale": round2(u), "odds": odds},
		})
	}

	// Tracking and landing page gaps, over the last full hour both sides
	// have had time to report (collection runs hourly, so an hour back).
	hourRows, err := db.Query(ctx, `SELECT time_zone, day, hour, sub1, sum(clicks), sum(lp_views), sum(lp_clicks)
		FROM intel.rt_campaign_hour WHERE day >= $1::date GROUP BY 1, 2, 3, 4`, d.Now.AddDate(0, 0, -2))
	if err != nil {
		return nil, err
	}
	type hk struct {
		campaign string
		at       time.Time
	}
	rtHour := map[hk][3]int64{}
	var (
		zone, sub1   string
		day          time.Time
		hour         int16
		clk, lv, lcl int64
	)
	if _, err := pgx.ForEachRow(hourRows, []any{&zone, &day, &hour, &sub1, &clk, &lv, &lcl}, func() error {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return nil
		}
		at := time.Date(day.Year(), day.Month(), day.Day(), int(hour), 0, 0, 0, loc)
		rtHour[hk{sub1, at.UTC()}] = [3]int64{clk, lv, lcl}
		return nil
	}); err != nil {
		return nil, err
	}
	// When RedTrack's hours were last read, per time zone: a campaign with no
	// row had no tracker clicks only if the read came after the hour.
	fetchedRows, err := db.Query(ctx, `SELECT time_zone, max(fetched_at) FROM intel.rt_campaign_hour GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	rtRead := map[string]time.Time{}
	var readAt time.Time
	if _, err := pgx.ForEachRow(fetchedRows, []any{&zone, &readAt}, func() error { rtRead[zone] = readAt; return nil }); err != nil {
		return nil, err
	}
	lpSeen, err := campaignsWithPageViews(ctx, db, d.Now)
	if err != nil {
		return nil, err
	}
	for id, camp := range d.Campaigns {
		if !d.tracked[id] {
			continue
		}
		a := d.Accounts[camp.Account]
		if a.Loc == nil {
			continue
		}
		h := d.Now.In(a.Loc).Truncate(time.Hour).Add(-2 * time.Hour)
		rt, ok := rtHour[hk{fmt.Sprint(id), h.UTC()}]
		read := rtRead[a.TimeZone].After(h.Add(time.Hour))
		if r := rec[id]; r != nil && read && r.lastBucket.After(h.Add(time.Hour)) {
			tb := r.hourClicks[h.UTC()]
			if tb >= int64(s.get("tracking_gap_min_clicks", 30)) && float64(rt[0]) < s.get("tracking_gap_ratio", 0.5)*float64(tb) {
				out = append(out, Found{
					Key: fmt.Sprintf("tracking_gap:%d", id), Kind: "tracking_gap", Account: camp.Account, Campaign: id,
					Title:   fmt.Sprintf("%s: RedTrack got %d of Taboola's %d clicks", campaignName(camp), rt[0], tb),
					Detail:  fmt.Sprintf("In the hour from %s (%s), RedTrack counted far fewer clicks than Taboola charged for: the link or the tracker may be broken.", h.Format("15:04"), a.TimeZone),
					Numbers: map[string]any{"hour": h.Format(time.RFC3339), "taboola_clicks": tb, "tracker_clicks": rt[0]},
				})
			}
		}
		if ok && lpSeen[fmt.Sprint(id)] && rt[0] >= int64(s.get("page_gap_min_clicks", 30)) &&
			float64(rt[1]) < s.get("page_gap_ratio", 0.3)*float64(rt[0]) {
			out = append(out, Found{
				Key: fmt.Sprintf("page_gap:%d", id), Kind: "page_gap", Account: camp.Account, Campaign: id,
				Title:   fmt.Sprintf("%s: %d page views from %d clicks", campaignName(camp), rt[1], rt[0]),
				Detail:  fmt.Sprintf("In the hour from %s (%s), far fewer clicks reached the landing page than usual: the page may be down or slow.", h.Format("15:04"), a.TimeZone),
				Numbers: map[string]any{"hour": h.Format(time.RFC3339), "tracker_clicks": rt[0], "lp_views": rt[1]},
			})
		}
	}

	// Postback gap: yesterday, Taboola counted far fewer sales than RedTrack,
	// so Taboola's bidding works blind.
	for id, camp := range d.Campaigns {
		if !d.tracked[id] || strings.HasPrefix(camp.Account, "redtrack:") {
			continue
		}
		c := d.CampaignCounts(id, "yesterday")
		if c.rtSales >= int64(s.get("postback_gap_min_sales", 3)) && float64(c.networkSales) < s.get("postback_gap_ratio", 0.5)*float64(c.rtSales) {
			out = append(out, Found{
				Key: fmt.Sprintf("postback_gap:%d", id), Kind: "postback_gap", Account: camp.Account, Campaign: id,
				Title:   fmt.Sprintf("%s: Taboola saw %d of RedTrack's %d sales yesterday", campaignName(camp), c.networkSales, c.rtSales),
				Detail:  "The postback to Taboola may be broken, which leaves Taboola's bidding without its sales.",
				Numbers: map[string]any{"network_sales": c.networkSales, "tracker_sales": c.rtSales},
			})
		}
	}

	// Items Taboola turned down in review.
	rows, err := db.Query(ctx, `SELECT i.item_id, i.campaign_id, i.account, i.title, i.reject_reason
		FROM intel.tb_item i WHERE i.gone_at IS NULL AND i.approval_state = 'REJECTED'`)
	if err != nil {
		return nil, err
	}
	var (
		item, campaign int64
		acc, title     string
		reason         string
	)
	if _, err := pgx.ForEachRow(rows, []any{&item, &campaign, &acc, &title, &reason}, func() error {
		detail := "Taboola turned the ad down in review."
		if reason != "" {
			detail = "Taboola turned the ad down in review: " + reason + "."
		}
		out = append(out, Found{
			Key: fmt.Sprintf("item_rejected:%d", item), Kind: "item_rejected", Account: acc, Campaign: campaign, Item: item,
			Title: fmt.Sprintf("Ad rejected: %q", title), Detail: detail, Numbers: map[string]any{"reject_reason": reason},
		})
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// campaignsWithPageViews are campaigns whose landing page counted views in
// RedTrack in the last 7 days: only their page views can go missing.
func campaignsWithPageViews(ctx context.Context, db *pgxpool.Pool, now time.Time) (map[string]bool, error) {
	rows, err := db.Query(ctx, `SELECT DISTINCT sub1 FROM intel.rt_item_day WHERE day >= $1::date AND lp_views > 0`, now.AddDate(0, 0, -7))
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	var s string
	_, err = pgx.ForEachRow(rows, []any{&s}, func() error { out[s] = true; return nil })
	return out, err
}

// Sender posts an alert; nil sends nothing (only the pages show it).
type Sender interface {
	Send(ctx context.Context, html string, silent bool) error
}

// Record opens alerts that are new, keeps open ones that still hold, closes
// the rest, and sends each new one once. It returns how many are open.
func Record(ctx context.Context, db *pgxpool.Pool, log *slog.Logger, found []Found, now time.Time, send Sender, baseURL string) (int, error) {
	keys := make([]string, 0, len(found))
	b := &pgx.Batch{}
	for _, f := range found {
		keys = append(keys, f.Key)
		b.Queue(`
			INSERT INTO intel.alert (key, kind, account, campaign_id, item_id, title, detail, numbers, opened_at, seen_at)
			VALUES ($1, $2, $3, NULLIF($4, 0), NULLIF($5, 0), $6, $7, $8, $9, $9)
			ON CONFLICT (key) WHERE closed_at IS NULL DO UPDATE SET title = EXCLUDED.title, detail = EXCLUDED.detail,
				numbers = EXCLUDED.numbers, seen_at = EXCLUDED.seen_at`,
			f.Key, f.Kind, f.Account, f.Campaign, f.Item, f.Title, f.Detail, f.Numbers, now)
	}
	b.Queue(`UPDATE intel.alert SET closed_at = $2 WHERE closed_at IS NULL AND NOT (key = ANY($1))`, keys, now)
	if err := db.SendBatch(ctx, b).Close(); err != nil {
		return 0, err
	}
	if send == nil {
		return len(found), nil
	}
	rows, err := db.Query(ctx, `SELECT id, kind, account, COALESCE(campaign_id, 0), title, detail FROM intel.alert
		WHERE closed_at IS NULL AND sent_at IS NULL ORDER BY id`)
	if err != nil {
		return len(found), err
	}
	type unsent struct {
		id                       int64
		kind, acc, title, detail string
		campaign                 int64
	}
	var list []unsent
	var u unsent
	if _, err := pgx.ForEachRow(rows, []any{&u.id, &u.kind, &u.acc, &u.campaign, &u.title, &u.detail}, func() error {
		list = append(list, u)
		return nil
	}); err != nil {
		return len(found), err
	}
	for _, u := range list {
		msg := fmt.Sprintf("<b>Intel · %s</b>\n%s\n%s", html.EscapeString(kindWords[u.kind]), html.EscapeString(u.title), html.EscapeString(u.detail))
		if baseURL != "" && u.campaign != 0 {
			msg += fmt.Sprintf("\n%s/intel/alerts#a%d", strings.TrimRight(baseURL, "/"), u.id)
		}
		if err := send.Send(ctx, msg, u.kind == "item_rejected"); err != nil {
			log.Error("alert not sent", "alert", u.id, "err", err)
			continue
		}
		if _, err := db.Exec(ctx, `UPDATE intel.alert SET sent_at = $2 WHERE id = $1`, u.id, now); err != nil {
			return len(found), err
		}
	}
	return len(found), nil
}

var kindWords = map[string]string{
	"runaway":       "runaway spend",
	"tracking_gap":  "tracking gap",
	"page_gap":      "landing page gap",
	"postback_gap":  "postback gap",
	"item_rejected": "ad rejected",
}

func campaignName(c Campaign) string {
	if c.Name != "" {
		return fmt.Sprintf("%q", c.Name)
	}
	return fmt.Sprintf("campaign %d", c.ID)
}

func money(v float64) string { return fmt.Sprintf("$%.2f", v) }

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// oneIn says small odds the way the replan does: "1 time in 50".
func oneIn(p float64) string {
	if p <= 0 {
		return "almost never"
	}
	n := 1 / p
	switch {
	case n >= 1000:
		return "less than 1 time in 1,000"
	case n >= 100:
		return fmt.Sprintf("1 time in %d", int(math.Round(n/10)*10))
	default:
		return fmt.Sprintf("1 time in %d", int(math.Round(n)))
	}
}
