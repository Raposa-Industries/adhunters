package judge

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Windows are the fixed spans results are kept for, in whole days of the
// account's time zone, ending today.
var Windows = []struct {
	Name string
	Days int // days back from today to the first day
	Last int // days back from today to the last day
}{
	{"today", 0, 0},
	{"yesterday", 1, 1},
	{"7d", 6, 0},
	{"30d", 29, 0},
}

// Settings are intel.setting, read on every run.
type Settings map[string]float64

func (s Settings) get(k string, def float64) float64 {
	if v, ok := s[k]; ok {
		return v
	}
	return def
}

// LoadSettings reads intel.setting.
func LoadSettings(ctx context.Context, db *pgxpool.Pool) (Settings, error) {
	rows, err := db.Query(ctx, `SELECT name, value::float8 FROM intel.setting`)
	if err != nil {
		return nil, err
	}
	s := Settings{}
	var name string
	var v float64
	_, err = pgx.ForEachRow(rows, []any{&name, &v}, func() error { s[name] = v; return nil })
	return s, err
}

// counts are what one campaign or ad did in one window.
type counts struct {
	impressions, clicks, networkSales         int64
	spent                                     float64
	trackerClicks, lpViews, lpClicks, rtSales int64
	revenue, rtCost                           float64
	tracked                                   bool // RedTrack has rows for it
}

func (c *counts) addTb(imp, clk, sales int64, spent float64) {
	c.impressions += imp
	c.clicks += clk
	c.networkSales += sales
	c.spent += spent
}

func (c *counts) addRt(clk, lpv, lpc, sales int64, rev, cost float64) {
	c.trackerClicks += clk
	c.lpViews += lpv
	c.lpClicks += lpc
	c.rtSales += sales
	c.revenue += rev
	c.rtCost += cost
	c.tracked = true
}

// cost is Taboola's spend when Intel has it, else RedTrack's cost (a live
// login without a Taboola key).
func (c counts) cost() float64 {
	if c.spent > 0 || c.impressions > 0 {
		return c.spent
	}
	return c.rtCost
}

func (c counts) sales(tracked bool) int64 {
	if tracked {
		return c.rtSales
	}
	return c.networkSales
}

type key struct {
	id     int64
	window string
}

// Data is everything the results, alerts and suggestions read, gathered
// once per run.
type Data struct {
	Now      time.Time
	Accounts map[string]Account
	// campaign id → account and group, for every campaign Intel has seen.
	Campaigns map[int64]Campaign
	campaign  map[key]*counts
	item      map[key]*counts
	itemOf    map[int64]int64 // item → campaign
	tracked   map[int64]bool  // campaign has RedTrack rows in the last 30 days
}

// Account is a Taboola account, or a RedTrack login's own stand-in
// ("redtrack:team") for campaigns only RedTrack sees.
type Account struct {
	ID       string
	TimeZone string
	Loc      *time.Location
	Today    time.Time // UTC midnight of the account's today
}

// Campaign is what Intel knows of a campaign's place and state.
type Campaign struct {
	ID       int64
	Account  string
	GroupID  int64
	Name     string
	IsActive bool
	Status   string
	Gone     bool
}

func (d *Data) account(id, zone string) Account {
	if a, ok := d.Accounts[id]; ok {
		return a
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	l := d.Now.In(loc)
	a := Account{ID: id, TimeZone: zone, Loc: loc, Today: time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)}
	d.Accounts[id] = a
	return a
}

// windowsOf lists the windows a day falls in for an account.
func windowsOf(a Account, day time.Time) []string {
	back := int(a.Today.Sub(day).Hours() / 24)
	var out []string
	for _, w := range Windows {
		if back >= w.Last && back <= w.Days {
			out = append(out, w.Name)
		}
	}
	return out
}

// Gather reads the last 30 days of Taboola and RedTrack numbers.
func Gather(ctx context.Context, db *pgxpool.Pool, now time.Time) (*Data, error) {
	d := &Data{Now: now, Accounts: map[string]Account{}, Campaigns: map[int64]Campaign{},
		campaign: map[key]*counts{}, item: map[key]*counts{}, itemOf: map[int64]int64{}, tracked: map[int64]bool{}}

	rows, err := db.Query(ctx, `SELECT account, time_zone FROM intel.tb_account WHERE type <> 'NETWORK'`)
	if err != nil {
		return nil, err
	}
	var acc, zone string
	if _, err := pgx.ForEachRow(rows, []any{&acc, &zone}, func() error { d.account(acc, zone); return nil }); err != nil {
		return nil, err
	}

	rows, err = db.Query(ctx, `
		SELECT campaign_id, account, COALESCE(group_id, 0), name, is_active, status, gone_at IS NOT NULL FROM intel.tb_campaign
		UNION ALL
		SELECT DISTINCT campaign_id, account, 0, '', false, '', true FROM intel.tb_campaign_day
		WHERE campaign_id NOT IN (SELECT campaign_id FROM intel.tb_campaign)
		UNION ALL
		SELECT DISTINCT campaign_id, account, 0, '', false, '', true FROM intel.tb_item_day
		WHERE campaign_id NOT IN (SELECT campaign_id FROM intel.tb_campaign)
		  AND campaign_id NOT IN (SELECT campaign_id FROM intel.tb_campaign_day)`)
	if err != nil {
		return nil, err
	}
	var c Campaign
	if _, err := pgx.ForEachRow(rows, []any{&c.ID, &c.Account, &c.GroupID, &c.Name, &c.IsActive, &c.Status, &c.Gone}, func() error {
		if _, ok := d.Campaigns[c.ID]; !ok {
			d.Campaigns[c.ID] = c
		}
		return nil
	}); err != nil {
		return nil, err
	}

	add := func(m map[key]*counts, id int64, windows []string, f func(*counts)) {
		for _, w := range windows {
			k := key{id, w}
			if m[k] == nil {
				m[k] = &counts{}
			}
			f(m[k])
		}
	}

	var (
		id, campaign      int64
		day               time.Time
		imp, clk, sales   int64
		spent             float64
		login, sub1, sub4 string
		lpv, lpc          int64
		rev, cost         float64
		since             = now.AddDate(0, 0, -32)
	)

	rows, err = db.Query(ctx, `SELECT campaign_id, account, day, impressions, clicks, conversions, spent::float8
		FROM intel.tb_campaign_day WHERE day >= $1::date`, since)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&id, &acc, &day, &imp, &clk, &sales, &spent}, func() error {
		a, ok := d.Accounts[acc]
		if !ok {
			return nil
		}
		add(d.campaign, id, windowsOf(a, day), func(c *counts) { c.addTb(imp, clk, sales, spent) })
		return nil
	}); err != nil {
		return nil, err
	}

	rows, err = db.Query(ctx, `SELECT item_id, campaign_id, account, day, impressions, clicks, conversions, spent::float8
		FROM intel.tb_item_day WHERE day >= $1::date`, since)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&id, &campaign, &acc, &day, &imp, &clk, &sales, &spent}, func() error {
		a, ok := d.Accounts[acc]
		if !ok {
			return nil
		}
		d.itemOf[id] = campaign
		add(d.item, id, windowsOf(a, day), func(c *counts) { c.addTb(imp, clk, sales, spent) })
		return nil
	}); err != nil {
		return nil, err
	}

	rows, err = db.Query(ctx, `SELECT login, time_zone, day, sub1, sub4, clicks, lp_views, lp_clicks, conversions,
		revenue::float8, cost::float8 FROM intel.rt_item_day WHERE day >= $1::date`, since)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&login, &zone, &day, &sub1, &sub4, &clk, &lpv, &lpc, &sales, &rev, &cost}, func() error {
		cid, err := strconv.ParseInt(sub1, 10, 64)
		if err != nil {
			return nil
		}
		var a Account
		if c, ok := d.Campaigns[cid]; ok {
			a = d.Accounts[c.Account]
			if a.TimeZone != zone {
				// Asked in another account's time zone: its days don't line up.
				return nil
			}
		} else {
			// Only RedTrack sees this campaign (a login without a Taboola key).
			a = d.account("redtrack:"+login, zone)
			if a.TimeZone != zone {
				return nil
			}
			d.Campaigns[cid] = Campaign{ID: cid, Account: a.ID}
		}
		ws := windowsOf(a, day)
		d.tracked[cid] = true
		add(d.campaign, cid, ws, func(c *counts) { c.addRt(clk, lpv, lpc, sales, rev, cost) })
		if iid, err := strconv.ParseInt(sub4, 10, 64); err == nil {
			d.itemOf[iid] = cid
			add(d.item, iid, ws, func(c *counts) { c.addRt(clk, lpv, lpc, sales, rev, cost) })
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return d, nil
}

// CampaignCounts returns a campaign's counts in a window (zero when none).
func (d *Data) CampaignCounts(id int64, window string) counts {
	if c := d.campaign[key{id, window}]; c != nil {
		return *c
	}
	return counts{}
}

// Results rebuilds intel.campaign_result and intel.ad_result.
func Results(ctx context.Context, db *pgxpool.Pool, d *Data, s Settings) (int, error) {
	level := s.get("range_level", 0.9)
	strength := s.get("prior_strength", 200)
	now := d.Now
	b := &pgx.Batch{}
	b.Queue(`DELETE FROM intel.campaign_result`)
	b.Queue(`DELETE FROM intel.ad_result`)
	n := 0
	for k, c := range d.campaign {
		camp := d.Campaigns[k.id]
		tracked := d.tracked[k.id]
		sales := c.sales(tracked)
		cost := c.cost()
		profit := c.revenue - cost
		var roi, cps, lo, hi *float64
		if tracked && cost > 0 {
			v := profit / cost
			roi = &v
		}
		if sales > 0 {
			v := cost / float64(sales)
			l, h := costPerSaleRange(cost, sales, level)
			cps, lo, hi = &v, &l, &h
		}
		source := "network"
		if tracked {
			source = "tracker"
		}
		b.Queue(`INSERT INTO intel.campaign_result (campaign_id, time_window, account, impressions, clicks, spent,
			network_sales, tracker_clicks, lp_views, lp_clicks, sales, revenue, profit, roi, cost_per_sale,
			cost_per_sale_low, cost_per_sale_high, sales_source, refreshed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
			k.id, k.window, camp.Account, c.impressions, c.clicks, cost, c.networkSales, c.trackerClicks, c.lpViews,
			c.lpClicks, sales, c.revenue, profit, roi, cps, lo, hi, source, now)
		n++
	}
	for k, c := range d.item {
		cid := d.itemOf[k.id]
		camp := d.Campaigns[cid]
		parent := d.CampaignCounts(cid, k.window)
		r := judgeAd(*c, parent, d.tracked[cid], level, strength)
		b.Queue(`INSERT INTO intel.ad_result (item_id, time_window, campaign_id, account, impressions, clicks, spent,
			tracker_clicks, lp_views, lp_clicks, sales, revenue, profit, ctr, ctr_low, ctr_high, lp_click_rate,
			lp_click_rate_low, lp_click_rate_high, sale_rate, sale_rate_low, sale_rate_high, order_value,
			profit_per_1000, profit_per_1000_low, profit_per_1000_high, profit_basis, word, sureness, spend_to_tell,
			refreshed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21,
			        $22, $23, $24, $25, $26, $27, $28, $29, $30, $31)`,
			k.id, k.window, cid, camp.Account, c.impressions, c.clicks, c.cost(), c.trackerClicks, c.lpViews,
			c.lpClicks, r.sales, c.revenue, c.revenue-c.cost(),
			r.ctr.v(), r.ctr.lo(), r.ctr.hi(), r.lpc.v(), r.lpc.lo(), r.lpc.hi(), r.sale.v(), r.sale.lo(), r.sale.hi(),
			r.orderValue, r.ppm.v(), r.ppm.lo(), r.ppm.hi(), r.basis, r.word, r.sureness, r.spendToTell, now)
		n++
	}
	if err := db.SendBatch(ctx, b).Close(); err != nil {
		return 0, fmt.Errorf("results: %w", err)
	}
	return n, nil
}

func (r Rate) v() *float64 {
	if !r.OK {
		return nil
	}
	return &r.Value
}
func (r Rate) lo() *float64 {
	if !r.OK {
		return nil
	}
	return &r.Low
}
func (r Rate) hi() *float64 {
	if !r.OK {
		return nil
	}
	return &r.High
}

type adVerdict struct {
	ctr, lpc, sale, ppm Rate
	sales               int64
	orderValue          *float64
	basis               string
	word, sureness      string
	spendToTell         *float64
}

// judgeAd works out one ad's step rates and profit per 1,000 impressions,
// each with a likely range, and says how it compares with its campaign.
//
// Profit per 1,000 impressions is measured from the ad's own sales once it
// has 10; before that it is estimated as click rate × sale rate × order
// value, with the sale rate pulled toward the campaign's (worth
// prior_strength clicks) and the campaign's order value, minus what 1,000
// impressions cost. Without impressions (RedTrack only) there is no click
// rate, and the ad is judged per click instead.
func judgeAd(c, parent counts, tracked bool, level, strength float64) adVerdict {
	var v adVerdict
	v.sales = c.sales(tracked)
	v.ctr = wilson(float64(c.clicks), float64(c.impressions), level)
	clicks := float64(c.trackerClicks)
	if !tracked {
		clicks = float64(c.clicks)
	}
	if tracked {
		v.lpc = wilson(float64(c.lpClicks), clicks, level)
	}
	parentClicks := float64(parent.trackerClicks)
	if !tracked {
		parentClicks = float64(parent.clicks)
	}
	parentSales := float64(parent.sales(tracked))
	prior := 0.0
	if parentClicks > 0 {
		prior = parentSales / parentClicks
	}
	v.sale = shrunk(float64(v.sales), clicks, prior, strength, level)
	if v.sales > 0 && tracked {
		ov := c.revenue / float64(v.sales)
		v.orderValue = &ov
	}
	parentOV := 0.0
	if parent.rtSales > 0 {
		parentOV = parent.revenue / float64(parent.rtSales)
	}
	ov := parentOV
	if v.orderValue != nil && v.sales >= 5 {
		ov = *v.orderValue
	}

	// Per 1,000 impressions when Taboola counted them, else per 1,000 clicks.
	per := float64(c.impressions)
	var reach Rate // share of the unit that became a click
	if per > 0 {
		reach = v.ctr
	} else {
		per = clicks
		reach = Rate{Value: 1, Low: 1, High: 1, OK: clicks > 0}
	}
	cost := c.cost()
	if per <= 0 || !tracked || ov <= 0 {
		// Without RedTrack's revenue there is no profit to compare.
		v.word = "too_little"
		return v
	}
	unitCost := cost / per * 1000
	if v.sales >= 10 {
		v.basis = "measured"
		pv := (c.revenue - cost) / per * 1000
		lo, hi := costPerSaleRange(1, v.sales, level) // relative spread of the sale count
		mid := 1 / float64(v.sales)
		rev := c.revenue / per * 1000
		v.ppm = Rate{Value: pv, Low: rev*(mid/hi) - unitCost, High: rev*(mid/lo) - unitCost, OK: true}
		if v.ppm.Low > v.ppm.High {
			v.ppm.Low, v.ppm.High = v.ppm.High, v.ppm.Low
		}
	} else {
		v.basis = "estimated"
		value := func(r, s float64) float64 { return 1000*r*s*ov - unitCost }
		v.ppm = Rate{Value: value(reach.Value, v.sale.Value), Low: value(reach.Low, v.sale.Low), High: value(reach.High, v.sale.High), OK: true}
	}

	// The campaign's own profit per 1,000, the yardstick.
	pPer := float64(parent.impressions)
	if pPer <= 0 {
		pPer = parentClicks
	}
	if pPer <= 0 || clicks < 30 {
		v.word = "too_little"
		return v
	}
	yard := (parent.revenue - parent.cost()) / pPer * 1000
	band := math.Max(math.Abs(yard)*0.2, 0.01)
	switch {
	case v.ppm.Low > yard+band || v.ppm.High < yard-band:
		v.sureness = "clear"
		v.word = "better"
		if v.ppm.High < yard {
			v.word = "worse"
		}
	case v.ppm.Low >= yard-band && v.ppm.High <= yard+band:
		v.word = "usual"
	case v.ppm.Value > yard+band && v.ppm.Low > yard-band || v.ppm.Value < yard-band && v.ppm.High < yard+band:
		v.sureness = "likely"
		v.word = "better"
		if v.ppm.Value < yard {
			v.word = "worse"
		}
	default:
		v.word = "unclear"
	}
	if v.word == "unclear" || v.sureness == "likely" {
		// The range narrows with the square root of the traffic: about how
		// much more spend brings its half-width down to the gap that matters.
		half := (v.ppm.High - v.ppm.Low) / 2
		gap := math.Max(math.Abs(v.ppm.Value-yard), band)
		if half > gap && cost > 0 {
			more := cost * ((half/gap)*(half/gap) - 1)
			more = math.Round(more)
			v.spendToTell = &more
		}
	}
	return v
}
