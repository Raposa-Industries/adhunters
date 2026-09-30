package web

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Windows are the result windows intel-numbers keeps, in page order.
var Windows = []string{"today", "yesterday", "7d", "30d"}

var windowNames = map[string]string{"today": "Hoje", "yesterday": "Ontem", "7d": "7 dias", "30d": "30 dias"}

// Result is a campaign's numbers over one window; nil when it has none.
type Result struct {
	Window      string
	Impressions int64
	Clicks      int64
	Spent       float64
	NetSales    int64
	TrkClicks   int64
	LPViews     int64
	LPClicks    int64
	Sales       int64
	Revenue     float64
	Profit      float64
	ROI         *float64
	CPS         *float64
	CPSLow      *float64
	CPSHigh     *float64
	SalesSource string
}

// Campaign is one row of a campaign list, with its result in the chosen
// window.
type Campaign struct {
	ID       int64
	Account  string
	GroupID  int64
	Name     string
	Status   string
	IsActive bool
	CPC      *float64
	DailyCap *float64
	Gone     bool
	R        *Result
}

// Ad is one row of a campaign's ads.
type Ad struct {
	ItemID                          int64
	Title                           string
	Thumb                           string
	Status                          string
	Approval                        string
	RejectReason                    string
	CustomID                        string
	Gone                            bool
	Has                             bool // has a result in the window
	Impressions                     int64
	Clicks                          int64
	Spent                           float64
	TrkClicks                       int64
	LPClicks                        int64
	Sales                           int64
	Revenue                         float64
	Profit                          float64
	CTR, CTRLow, CTRHigh            *float64
	LPRate, LPRateLow, LPRateHigh   *float64
	SaleRate, SaleRateLow, SaleHigh *float64
	P1000, P1000Low, P1000High      *float64
	Basis, Word, Sureness           string
	SpendToTell                     *float64
}

// Suggestion is a change Intel proposes; Launch makes it.
type Suggestion struct {
	ID         int64
	Kind       string
	Account    string
	GroupID    int64
	CampaignID int64
	Campaign   string
	ItemIDs    []int64
	Title      string
	Why        string
	LaunchURL  string
	State      string
	CreatedAt  time.Time
	AnsweredAt *time.Time
	AnsweredBy string
}

// Alert is something wrong now, or lately.
type Alert struct {
	ID         int64
	Kind       string
	Account    string
	CampaignID *int64
	GroupID    int64
	Known      bool // the campaign has a page
	ItemID     *int64
	Title      string
	Detail     string
	OpenedAt   time.Time
	ClosedAt   *time.Time
}

// LineCampaign is a campaign in the same line as another: copied from it,
// or copied to it (a move to another group makes a copy with a new id).
type LineCampaign struct {
	ID      int64
	Account string
	GroupID int64
	Name    string
	Known   bool
	Older   bool
}

type store struct{ db *pgxpool.Pool }

func (s store) asOf(ctx context.Context) (*time.Time, error) {
	var t time.Time
	err := s.db.QueryRow(ctx, `SELECT done_at FROM intel.job_mark WHERE job = 'round'`).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &t, err
}

func (s store) accounts(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT account FROM intel.tb_account WHERE type <> 'NETWORK' ORDER BY account`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (s store) accountKnown(ctx context.Context, account string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM intel.tb_account WHERE account = $1 AND type <> 'NETWORK')`, account).Scan(&ok)
	return ok, err
}

const resultCols = `r.time_window, r.impressions, r.clicks, r.spent::float8, r.network_sales, r.tracker_clicks,
	r.lp_views, r.lp_clicks, r.sales, r.revenue::float8, r.profit::float8, r.roi, r.cost_per_sale,
	r.cost_per_sale_low, r.cost_per_sale_high, r.sales_source`

func scanResult(row interface{ Scan(...any) error }, dst []any) (*Result, error) {
	var r Result
	// A LEFT JOIN with no result leaves every column NULL, so everything is
	// scanned into nullable holders first.
	var (
		win, src                     *string
		imp, clk, ns, tc, lv, lc, sa *int64
		sp, rev, pr                  *float64
	)
	all := append(dst, &win, &imp, &clk, &sp, &ns, &tc, &lv, &lc, &sa, &rev, &pr, &r.ROI, &r.CPS, &r.CPSLow, &r.CPSHigh, &src)
	if err := row.Scan(all...); err != nil {
		return nil, err
	}
	if win == nil {
		return nil, nil
	}
	r.Window = *win
	r.Impressions, r.Clicks, r.NetSales, r.TrkClicks, r.LPViews, r.LPClicks, r.Sales = *imp, *clk, *ns, *tc, *lv, *lc, *sa
	r.Spent, r.Revenue, r.Profit, r.SalesSource = *sp, *rev, *pr, *src
	return &r, nil
}

// campaigns lists campaigns with their result in window: of one account
// (and group) or of all. A campaign Taboola no longer lists shows while it
// has spend in the window.
func (s store) campaigns(ctx context.Context, account string, group int64, window string) ([]Campaign, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.campaign_id, c.account, COALESCE(c.group_id, 0), c.name, c.status, c.is_active,
		       c.cpc::float8, c.daily_cap::float8, c.gone_at IS NOT NULL, `+resultCols+`
		FROM intel.tb_campaign c
		LEFT JOIN intel.campaign_result r ON r.campaign_id = c.campaign_id AND r.time_window = $1
		WHERE ($2 = '' OR c.account = $2) AND ($3 = 0 OR c.group_id = $3)
		  AND (c.gone_at IS NULL OR r.spent > 0)
		ORDER BY r.spent DESC NULLS LAST, c.campaign_id DESC`, window, account, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Campaign
	for rows.Next() {
		var c Campaign
		r, err := scanResult(rows, []any{&c.ID, &c.Account, &c.GroupID, &c.Name, &c.Status, &c.IsActive, &c.CPC, &c.DailyCap, &c.Gone})
		if err != nil {
			return nil, err
		}
		c.R = r
		out = append(out, c)
	}
	return out, rows.Err()
}

// trackerOnly lists campaigns only the tracker saw in window (a campaign of
// a login Intel does not read, or one deleted before Intel first read it).
func (s store) trackerOnly(ctx context.Context, window string) ([]Campaign, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.campaign_id, r.account, `+resultCols+`
		FROM intel.campaign_result r
		WHERE r.time_window = $1 AND r.account LIKE 'redtrack:%'
		  AND NOT EXISTS (SELECT 1 FROM intel.tb_campaign c WHERE c.campaign_id = r.campaign_id)
		ORDER BY r.tracker_clicks DESC, r.campaign_id DESC`, window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Campaign
	for rows.Next() {
		var c Campaign
		r, err := scanResult(rows, []any{&c.ID, &c.Account})
		if err != nil {
			return nil, err
		}
		c.R = r
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s store) campaign(ctx context.Context, account string, id int64) (*Campaign, error) {
	var c Campaign
	err := s.db.QueryRow(ctx, `
		SELECT campaign_id, account, COALESCE(group_id, 0), name, status, is_active, cpc::float8, daily_cap::float8, gone_at IS NOT NULL
		FROM intel.tb_campaign WHERE account = $1 AND campaign_id = $2`, account, id).
		Scan(&c.ID, &c.Account, &c.GroupID, &c.Name, &c.Status, &c.IsActive, &c.CPC, &c.DailyCap, &c.Gone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

// results are a campaign's numbers in every window, in page order.
func (s store) results(ctx context.Context, id int64) ([]*Result, error) {
	rows, err := s.db.Query(ctx, `SELECT `+resultCols+` FROM intel.campaign_result r WHERE r.campaign_id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]*Result{}
	for rows.Next() {
		r, err := scanResult(rows, nil)
		if err != nil {
			return nil, err
		}
		by[r.Window] = r
	}
	out := make([]*Result, len(Windows))
	for i, w := range Windows {
		out[i] = by[w]
		if out[i] == nil {
			out[i] = &Result{Window: w}
		}
	}
	return out, rows.Err()
}

// ads are a campaign's ads with their results in window: every ad Taboola
// lists, and old versions that only the report still has.
func (s store) ads(ctx context.Context, campaign int64, window string) ([]Ad, error) {
	rows, err := s.db.Query(ctx, `
		WITH ids AS (
			SELECT item_id FROM intel.tb_item WHERE campaign_id = $1
			UNION SELECT item_id FROM intel.ad_result WHERE campaign_id = $1 AND time_window = $2
		)
		SELECT ids.item_id, COALESCE(i.title, ''), COALESCE(i.thumbnail_url, ''), COALESCE(i.status, ''),
		       COALESCE(i.approval_state, ''), COALESCE(i.reject_reason, ''), COALESCE(i.custom_id, ''),
		       i.item_id IS NULL OR i.gone_at IS NOT NULL, a.item_id IS NOT NULL,
		       COALESCE(a.impressions, 0), COALESCE(a.clicks, 0), COALESCE(a.spent, 0)::float8,
		       COALESCE(a.tracker_clicks, 0), COALESCE(a.lp_clicks, 0), COALESCE(a.sales, 0),
		       COALESCE(a.revenue, 0)::float8, COALESCE(a.profit, 0)::float8,
		       a.ctr, a.ctr_low, a.ctr_high, a.lp_click_rate, a.lp_click_rate_low, a.lp_click_rate_high,
		       a.sale_rate, a.sale_rate_low, a.sale_rate_high,
		       a.profit_per_1000, a.profit_per_1000_low, a.profit_per_1000_high,
		       COALESCE(a.profit_basis, ''), COALESCE(a.word, ''), COALESCE(a.sureness, ''), a.spend_to_tell::float8
		FROM ids
		LEFT JOIN intel.tb_item i ON i.item_id = ids.item_id
		LEFT JOIN intel.ad_result a ON a.item_id = ids.item_id AND a.time_window = $2
		ORDER BY COALESCE(a.spent, 0) DESC, ids.item_id DESC`, campaign, window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ad
	for rows.Next() {
		var a Ad
		if err := rows.Scan(&a.ItemID, &a.Title, &a.Thumb, &a.Status, &a.Approval, &a.RejectReason, &a.CustomID,
			&a.Gone, &a.Has, &a.Impressions, &a.Clicks, &a.Spent, &a.TrkClicks, &a.LPClicks, &a.Sales, &a.Revenue, &a.Profit,
			&a.CTR, &a.CTRLow, &a.CTRHigh, &a.LPRate, &a.LPRateLow, &a.LPRateHigh,
			&a.SaleRate, &a.SaleRateLow, &a.SaleHigh, &a.P1000, &a.P1000Low, &a.P1000High,
			&a.Basis, &a.Word, &a.Sureness, &a.SpendToTell); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// line is every campaign copied from this one's line, older first, then
// the ones copied from it.
func (s store) line(ctx context.Context, id int64) ([]LineCampaign, error) {
	rows, err := s.db.Query(ctx, `
		WITH RECURSIVE down AS (
			SELECT new_campaign_id AS id, 1 AS depth FROM intel.campaign_link WHERE old_campaign_id = $1
			UNION ALL
			SELECT l.new_campaign_id, down.depth + 1 FROM down JOIN intel.campaign_link l ON l.old_campaign_id = down.id
			WHERE down.depth < 50
		), line AS (
			SELECT ancestor_id AS id, -depth AS pos FROM intel.campaign_line WHERE campaign_id = $1 AND depth > 0
			UNION ALL SELECT id, depth FROM down
		)
		SELECT line.id, COALESCE(c.account, ''), COALESCE(c.group_id, 0), COALESCE(c.name, ''), c.campaign_id IS NOT NULL, line.pos < 0
		FROM line LEFT JOIN intel.tb_campaign c ON c.campaign_id = line.id
		ORDER BY line.pos`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LineCampaign
	for rows.Next() {
		var l LineCampaign
		if err := rows.Scan(&l.ID, &l.Account, &l.GroupID, &l.Name, &l.Known, &l.Older); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// suggestions lists open suggestions, and with recent those answered or
// gone in the last 7 days; campaign 0 means every campaign.
func (s store) suggestions(ctx context.Context, campaign int64, recent bool) ([]Suggestion, error) {
	rows, err := s.db.Query(ctx, `
		SELECT s.id, s.kind, s.account, COALESCE(s.group_id, 0), s.campaign_id, COALESCE(c.name, ''), s.item_ids,
		       s.title, s.why, s.launch_url, s.state, s.created_at, s.answered_at, s.answered_by
		FROM intel.suggestion s LEFT JOIN intel.tb_campaign c ON c.campaign_id = s.campaign_id
		WHERE ($1 = 0 OR s.campaign_id = $1)
		  AND (s.state = 'open' OR ($2 AND s.seen_at > now() - interval '7 days'))
		ORDER BY s.state = 'open' DESC, s.created_at DESC
		LIMIT 200`, campaign, recent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Suggestion
	for rows.Next() {
		var g Suggestion
		if err := rows.Scan(&g.ID, &g.Kind, &g.Account, &g.GroupID, &g.CampaignID, &g.Campaign, &g.ItemIDs,
			&g.Title, &g.Why, &g.LaunchURL, &g.State, &g.CreatedAt, &g.AnsweredAt, &g.AnsweredBy); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// alerts lists open alerts, and with recent those closed in the last 7
// days; campaign 0 means every campaign.
func (s store) alerts(ctx context.Context, campaign int64, recent bool) ([]Alert, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.id, a.kind, a.account, a.campaign_id, COALESCE(c.group_id, 0), c.campaign_id IS NOT NULL, a.item_id,
		       a.title, a.detail, a.opened_at, a.closed_at
		FROM intel.alert a LEFT JOIN intel.tb_campaign c ON c.campaign_id = a.campaign_id
		WHERE ($1 = 0 OR a.campaign_id = $1)
		  AND (a.closed_at IS NULL OR ($2 AND a.closed_at > now() - interval '7 days'))
		ORDER BY a.closed_at IS NULL DESC, a.opened_at DESC
		LIMIT 200`, campaign, recent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.Kind, &a.Account, &a.CampaignID, &a.GroupID, &a.Known, &a.ItemID,
			&a.Title, &a.Detail, &a.OpenedAt, &a.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// notNow sets an open suggestion aside for a day; it reports whether one
// was open.
func (s store) notNow(ctx context.Context, id int64, who string) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE intel.suggestion SET state = 'dismissed', answered_at = now(), answered_by = $2
		WHERE id = $1 AND state = 'open'`, id, who)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Found is one ⌘K result.
type Found struct {
	Title string `json:"title"`
	Sub   string `json:"sub"`
	Href  string `json:"href"`
}

func (s store) search(ctx context.Context, q string) ([]Found, error) {
	rows, err := s.db.Query(ctx, `
		SELECT campaign_id, account, COALESCE(group_id, 0), name FROM intel.tb_campaign
		WHERE name ILIKE '%' || $1 || '%' OR campaign_id::text = $1
		ORDER BY gone_at IS NULL DESC, fetched_at DESC LIMIT 20`, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Found
	for rows.Next() {
		var id, group int64
		var account, name string
		if err := rows.Scan(&id, &account, &group, &name); err != nil {
			return nil, err
		}
		out = append(out, Found{Title: name, Sub: account + " · " + itoa(id), Href: campaignPath(account, group, id)})
	}
	return out, rows.Err()
}
