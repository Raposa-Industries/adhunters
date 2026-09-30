package judge

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Suggestion is a change Intel proposes. Intel never makes it: LaunchURL
// opens Launch with the change filled in, and a person confirms there.
type Suggestion struct {
	Key      string
	Kind     string // pause-ads, pause-campaign, set-daily-cap
	Account  string
	Group    int64
	Campaign int64
	Items    []int64
	Values   map[string]any
	Title    string
	Why      string
	Numbers  map[string]any
}

// LaunchPath is the Launch link for a suggestion: the campaign's place in
// Taboola's tree, then the action and its values (the canvas's links).
// Launch owns what the parameters mean; Intel fills them in. Campaigns only
// RedTrack sees have no Launch page yet, so they get none.
func LaunchPath(s Suggestion, id int64) string {
	if s.Account == "" || strings.HasPrefix(s.Account, "redtrack:") {
		return ""
	}
	q := url.Values{"do": {s.Kind}, "from": {fmt.Sprintf("intel:%d", id)}}
	if len(s.Items) > 0 {
		ids := make([]string, len(s.Items))
		for i, it := range s.Items {
			ids[i] = strconv.FormatInt(it, 10)
		}
		q.Set("ads", strings.Join(ids, ","))
	}
	if v, ok := s.Values["daily_cap"]; ok {
		q.Set("cap", fmt.Sprint(v))
	}
	// Launch writes a campaign with no group as g/-.
	group := "-"
	if s.Group != 0 {
		group = strconv.FormatInt(s.Group, 10)
	}
	return fmt.Sprintf("/launch/taboola/%s/g/%s/c/%d?%s", url.PathEscape(s.Account), group, s.Campaign, q.Encode())
}

// Suggest works out the suggestions that hold now.
//
//   - pause-ads: in a campaign with RedTrack's sales, ads that spent since
//     yesterday with no sale, past what a normal ad would rarely reach
//     (pause_odds), while other ads keep running.
//   - pause-campaign: every running ad in it is like that.
//   - set-daily-cap: the campaign is running away today (the runaway
//     alert); halve its daily cap until someone looks.
func Suggest(ctx context.Context, db *pgxpool.Pool, d *Data, s Settings, alerts []Found) ([]Suggestion, error) {
	usual := d.Usual()
	active, caps, err := itemStates(ctx, db)
	if err != nil {
		return nil, err
	}
	byCampaign := map[int64][]int64{}
	for item, cid := range d.itemOf {
		byCampaign[cid] = append(byCampaign[cid], item)
	}
	var out []Suggestion
	for cid, items := range byCampaign {
		camp := d.Campaigns[cid]
		if !d.tracked[cid] || camp.Gone {
			continue
		}
		u := usual[camp.Account]
		if u <= 0 {
			continue
		}
		type bad struct {
			id    int64
			spent float64
		}
		var bads []bad
		running, goodProfit, badSpent := 0, 0.0, 0.0
		for _, it := range items {
			if st, known := active[it]; known && !st {
				continue // already paused
			}
			running++
			var c counts
			for _, w := range []string{"today", "yesterday"} {
				if x := d.item[key{it, w}]; x != nil {
					c.addTb(x.impressions, x.clicks, x.networkSales, x.cost())
					c.addRt(x.trackerClicks, x.lpViews, x.lpClicks, x.rtSales, x.revenue, x.rtCost)
				}
			}
			if c.rtSales == 0 && c.cost() >= s.get("pause_min_spend", 10) && NoSaleOdds(c.cost(), u) < s.get("pause_odds", 0.05) {
				bads = append(bads, bad{it, c.cost()})
				badSpent += c.cost()
			} else {
				goodProfit += c.revenue - c.cost()
			}
		}
		if len(bads) == 0 {
			continue
		}
		sort.Slice(bads, func(i, j int) bool { return bads[i].spent > bads[j].spent })
		ids := make([]int64, len(bads))
		spends := map[string]float64{}
		for i, b := range bads {
			ids[i] = b.id
			spends[strconv.FormatInt(b.id, 10)] = round2(b.spent)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		numbers := map[string]any{"spent": round2(badSpent), "usual_cost_per_sale": round2(u), "ad_spend": spends,
			"other_ads_profit": round2(goodProfit)}
		if len(bads) == running {
			out = append(out, Suggestion{
				Key: fmt.Sprintf("pause-campaign:%d", cid), Kind: "pause-campaign", Account: camp.Account, Group: camp.GroupID,
				Campaign: cid, Title: fmt.Sprintf("Pause %s: none of its ads sells", campaignName(camp)),
				Why: fmt.Sprintf("Its %d running ads spent %s since yesterday with no sale; the account usually pays %s a sale.",
					running, money(badSpent), money(u)),
				Numbers: numbers,
			})
			continue
		}
		title := fmt.Sprintf("Pause %d ads that spend without sales", len(bads))
		if len(bads) == 1 {
			title = "Pause an ad that spends without sales"
		}
		out = append(out, Suggestion{
			Key: fmt.Sprintf("pause-ads:%d:%s", cid, joinIDs(ids)), Kind: "pause-ads", Account: camp.Account, Group: camp.GroupID,
			Campaign: cid, Items: ids, Title: title,
			Why: fmt.Sprintf("%s. These spent %s since yesterday with no sale; the other ads made %s profit.",
				campaignName(camp), money(badSpent), money(goodProfit)),
			Numbers: numbers,
		})
	}
	for _, a := range alerts {
		if a.Kind != "runaway" {
			continue
		}
		camp := d.Campaigns[a.Campaign]
		cap, ok := caps[a.Campaign]
		if !ok || cap <= 0 {
			continue
		}
		half := math.Round(cap/2*100) / 100
		out = append(out, Suggestion{
			Key: fmt.Sprintf("set-daily-cap:%d", a.Campaign), Kind: "set-daily-cap", Account: camp.Account, Group: camp.GroupID,
			Campaign: a.Campaign, Values: map[string]any{"daily_cap": half},
			Title:   fmt.Sprintf("Halve %s's daily cap to %s", campaignName(camp), money(half)),
			Why:     a.Title + ". " + a.Detail + " A lower cap limits the loss until someone checks the page and the tracking.",
			Numbers: map[string]any{"daily_cap": cap, "suggested": half},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// itemStates reads whether each item is running and each campaign's daily
// cap, as last listed.
func itemStates(ctx context.Context, db *pgxpool.Pool) (map[int64]bool, map[int64]float64, error) {
	active := map[int64]bool{}
	rows, err := db.Query(ctx, `SELECT i.item_id, i.is_active AND i.gone_at IS NULL AND i.status NOT IN ('PAUSED', 'STOPPED')
		FROM intel.tb_item i`)
	if err != nil {
		return nil, nil, err
	}
	var id int64
	var on bool
	if _, err := pgx.ForEachRow(rows, []any{&id, &on}, func() error { active[id] = on; return nil }); err != nil {
		return nil, nil, err
	}
	caps := map[int64]float64{}
	rows, err = db.Query(ctx, `SELECT campaign_id, daily_cap::float8 FROM intel.tb_campaign WHERE daily_cap IS NOT NULL`)
	if err != nil {
		return nil, nil, err
	}
	var cap float64
	_, err = pgx.ForEachRow(rows, []any{&id, &cap}, func() error { caps[id] = cap; return nil })
	return active, caps, err
}

func joinIDs(ids []int64) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(s, ",")
}

// Keep writes the suggestions that hold now: a new one opens (unless people
// said "not now" to the same one in the last day), an open one is kept
// fresh, an open one that no longer holds is marked gone, or done when
// Taboola's settings show the change was made.
func Keep(ctx context.Context, db *pgxpool.Pool, list []Suggestion, now time.Time) (int, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	keys := make([]string, 0, len(list))
	for _, s := range list {
		keys = append(keys, s.Key)
		var id int64
		err := tx.QueryRow(ctx, `
			UPDATE intel.suggestion SET seen_at = $2, why = $3, numbers = $4, title = $5
			WHERE key = $1 AND state = 'open' RETURNING id`, s.Key, now, s.Why, s.Numbers, s.Title).Scan(&id)
		if err == nil {
			continue
		}
		if err != pgx.ErrNoRows {
			return 0, err
		}
		var recent bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM intel.suggestion WHERE key = $1 AND state = 'dismissed'
			AND answered_at > $2)`, s.Key, now.Add(-24*time.Hour)).Scan(&recent); err != nil {
			return 0, err
		}
		if recent {
			continue
		}
		values := s.Values
		if values == nil {
			values = map[string]any{}
		}
		items := s.Items
		if items == nil {
			items = []int64{}
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO intel.suggestion (key, kind, account, group_id, campaign_id, item_ids, values, title, why, numbers,
				launch_url, created_at, seen_at)
			VALUES ($1, $2, $3, NULLIF($4, 0), $5, $6, $7, $8, $9, $10, '', $11, $11) RETURNING id`,
			s.Key, s.Kind, s.Account, s.Group, s.Campaign, items, values, s.Title, s.Why, s.Numbers, now).Scan(&id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE intel.suggestion SET launch_url = $2 WHERE id = $1`, id, LaunchPath(s, id)); err != nil {
			return 0, err
		}
	}
	// Made in Launch (or by hand in Taboola): the settings show it.
	if _, err := tx.Exec(ctx, `
		UPDATE intel.suggestion s SET state = 'done', answered_at = $1
		WHERE s.state = 'open' AND (
			(s.kind = 'pause-ads' AND NOT EXISTS (SELECT 1 FROM intel.tb_item i WHERE i.item_id = ANY(s.item_ids)
				AND i.is_active AND i.gone_at IS NULL))
			OR (s.kind = 'pause-campaign' AND EXISTS (SELECT 1 FROM intel.tb_campaign c WHERE c.campaign_id = s.campaign_id
				AND NOT c.is_active))
			OR (s.kind = 'set-daily-cap' AND EXISTS (SELECT 1 FROM intel.tb_campaign c WHERE c.campaign_id = s.campaign_id
				AND c.daily_cap <= (s.values->>'daily_cap')::numeric)))
			AND EXISTS (SELECT 1 FROM intel.tb_campaign c WHERE c.campaign_id = s.campaign_id)`, now); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE intel.suggestion SET state = 'gone' WHERE state = 'open' AND NOT (key = ANY($1))`, keys); err != nil {
		return 0, err
	}
	return len(list), tx.Commit(ctx)
}
