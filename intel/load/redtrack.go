package load

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// rtReport loads one page of a RedTrack report asked for one day, grouped
// by the Taboola ids in the sub slots.
func rtReport(ctx context.Context, tx pgx.Tx, a answer, body []byte) error {
	rows, err := decodeRows(body, "")
	if err != nil {
		return err
	}
	day, err := a.day("from")
	if err != nil {
		return fmt.Errorf("report without its day: %w", err)
	}
	if a.param("to") != a.param("from") {
		return fmt.Errorf("report over %s..%s: only single days load", a.param("from"), a.param("to"))
	}
	zone := a.param("time_zone")
	b := &pgx.Batch{}
	for _, r := range rows {
		sub1 := r.str("sub1")
		if sub1 == "" {
			// Clicks that came without Taboola's campaign id can't be joined.
			continue
		}
		clicks, lpv, lpc, conv := r.int("clicks"), r.int("lp_views"), r.int("lp_clicks"), r.int("conversions")
		rev, cost := r.num("revenue"), r.num("cost")
		switch a.Kind {
		case "redtrack.item_day":
			b.Queue(`
				INSERT INTO intel.rt_item_day (login, time_zone, day, sub1, sub4, clicks, lp_views, lp_clicks, conversions, revenue, cost, fetched_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				ON CONFLICT (login, time_zone, day, sub1, sub4) DO UPDATE SET clicks = EXCLUDED.clicks,
					lp_views = EXCLUDED.lp_views, lp_clicks = EXCLUDED.lp_clicks, conversions = EXCLUDED.conversions,
					revenue = EXCLUDED.revenue, cost = EXCLUDED.cost, fetched_at = EXCLUDED.fetched_at
				WHERE intel.rt_item_day.fetched_at <= EXCLUDED.fetched_at`,
				a.Login, zone, day, sub1, r.str("sub4"), clicks, lpv, lpc, conv, rev, cost, a.FetchedAt)
		case "redtrack.site_day":
			b.Queue(`
				INSERT INTO intel.rt_site_day (login, time_zone, day, sub1, sub8, clicks, lp_views, lp_clicks, conversions, revenue, cost, fetched_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				ON CONFLICT (login, time_zone, day, sub1, sub8) DO UPDATE SET clicks = EXCLUDED.clicks,
					lp_views = EXCLUDED.lp_views, lp_clicks = EXCLUDED.lp_clicks, conversions = EXCLUDED.conversions,
					revenue = EXCLUDED.revenue, cost = EXCLUDED.cost, fetched_at = EXCLUDED.fetched_at
				WHERE intel.rt_site_day.fetched_at <= EXCLUDED.fetched_at`,
				a.Login, zone, day, sub1, r.str("sub8"), clicks, lpv, lpc, conv, rev, cost, a.FetchedAt)
		case "redtrack.campaign_hour":
			b.Queue(`
				INSERT INTO intel.rt_campaign_hour (login, time_zone, day, hour, sub1, clicks, lp_views, lp_clicks, conversions, revenue, cost, fetched_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				ON CONFLICT (login, time_zone, day, hour, sub1) DO UPDATE SET clicks = EXCLUDED.clicks,
					lp_views = EXCLUDED.lp_views, lp_clicks = EXCLUDED.lp_clicks, conversions = EXCLUDED.conversions,
					revenue = EXCLUDED.revenue, cost = EXCLUDED.cost, fetched_at = EXCLUDED.fetched_at
				WHERE intel.rt_campaign_hour.fetched_at <= EXCLUDED.fetched_at`,
				a.Login, zone, day, r.int("hour_of_day"), sub1, clicks, lpv, lpc, conv, rev, cost, a.FetchedAt)
		}
	}
	return tx.SendBatch(ctx, b).Close()
}

// Field names RedTrack may use for a conversion's parts. Its row shape is
// not confirmed on real traffic yet, so each is looked for under the names
// its API reference and the mcp-redtrack client use.
var (
	convID      = []string{"id", "conversion_id", "_id"}
	convClickID = []string{"clickid", "click_id"}
	convType    = []string{"type", "conversion_type", "event"}
	convStatus  = []string{"status"}
	convPayout  = []string{"payout", "revenue"}
	convClicked = []string{"click_time", "click_date", "clicked_at", "track_date"}
	convAt      = []string{"created_at", "conversion_date", "date", "time", "server_time"}
)

func first(r row, keys []string) string {
	for _, k := range keys {
		if v := r.str(k); v != "" {
			return v
		}
	}
	return ""
}

func rtConversions(ctx context.Context, tx pgx.Tx, a answer, body []byte) error {
	rows, err := decodeRows(body, "items")
	if err != nil {
		return err
	}
	b := &pgx.Batch{}
	for _, r := range rows {
		id := first(r, convID)
		if id == "" {
			id = "h" + hashJSON(r)
		}
		var payout *float64
		if s := first(r, convPayout); s != "" {
			f := row{"v": s}.num("v")
			payout = &f
		}
		b.Queue(`
			INSERT INTO intel.rt_conversion (login, conversion_id, click_id, sub1, sub4, sub8, type, status, payout,
				clicked_at, converted_at, row, fetched_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (login, conversion_id) DO UPDATE SET click_id = EXCLUDED.click_id, sub1 = EXCLUDED.sub1,
				sub4 = EXCLUDED.sub4, sub8 = EXCLUDED.sub8, type = EXCLUDED.type, status = EXCLUDED.status,
				payout = EXCLUDED.payout, clicked_at = EXCLUDED.clicked_at, converted_at = EXCLUDED.converted_at,
				row = EXCLUDED.row, fetched_at = EXCLUDED.fetched_at
			WHERE intel.rt_conversion.fetched_at <= EXCLUDED.fetched_at`,
			a.Login, id, first(r, convClickID), r.str("sub1"), r.str("sub4"), r.str("sub8"), first(r, convType),
			first(r, convStatus), payout, when(first(r, convClicked)), when(first(r, convAt)), r, a.FetchedAt)
	}
	return tx.SendBatch(ctx, b).Close()
}

// when reads the time formats RedTrack uses; nil when none fits.
func when(s string) *time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}
