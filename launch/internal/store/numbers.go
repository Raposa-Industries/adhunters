package store

import (
	"context"
	"time"
)

// Windows are the spans Intel keeps numbers for, in each account's time zone.
var Windows = map[string]bool{"today": true, "yesterday": true, "7d": true, "30d": true}

// Numbers is one campaign's or ad's money and steps over a window, read from
// Intel's intel_api views (Launch never asks Taboola for reports). Money is
// USD. Sales and revenue come from the tracker.
type Numbers struct {
	Impressions int64   `json:"impressions"`
	Clicks      int64   `json:"clicks"`
	Spent       float64 `json:"spent"`
	Sales       float64 `json:"sales"`
	Revenue     float64 `json:"revenue"`
	Profit      float64 `json:"profit"`
	// Ads only: Intel's word on the ad and how sure it is.
	Word     string `json:"word,omitempty"`
	Sureness string `json:"sureness,omitempty"`
}

// Results is a window's numbers by campaign id and by ad (item) id.
type Results struct {
	Window string `json:"window"`
	// Available is false when Intel's views are not in this database yet.
	Available   bool               `json:"available"`
	Campaigns   map[string]Numbers `json:"campaigns"`
	Ads         map[string]Numbers `json:"ads"`
	RefreshedAt *time.Time         `json:"refreshed_at"`
}

// Results reads a window's numbers for the given accounts (every account
// when none). Without Intel's views it answers Available false, no error.
func (s *Store) Results(ctx context.Context, window string, accounts []string) (Results, error) {
	out := Results{Window: window, Campaigns: map[string]Numbers{}, Ads: map[string]Numbers{}}
	var there bool
	if err := s.db.QueryRow(ctx, `SELECT to_regclass('intel_api.campaign_result_v1') IS NOT NULL AND to_regclass('intel_api.ad_result_v1') IS NOT NULL`).Scan(&there); err != nil {
		return out, err
	}
	if !there {
		return out, nil
	}
	out.Available = true
	if accounts == nil {
		accounts = []string{}
	}
	scan := func(q string, into map[string]Numbers) error {
		rows, err := s.db.Query(ctx, q, window, accounts)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var n Numbers
			var at *time.Time
			var word, sure *string
			if err := rows.Scan(&id, &n.Impressions, &n.Clicks, &n.Spent, &n.Sales, &n.Revenue, &n.Profit, &at, &word, &sure); err != nil {
				return err
			}
			if word != nil {
				n.Word = *word
			}
			if sure != nil {
				n.Sureness = *sure
			}
			into[id] = n
			if at != nil && (out.RefreshedAt == nil || at.After(*out.RefreshedAt)) {
				out.RefreshedAt = at
			}
		}
		return rows.Err()
	}
	const pick = `COALESCE(impressions, 0)::bigint, COALESCE(clicks, 0)::bigint, COALESCE(spent, 0)::float8, COALESCE(sales, 0)::float8,
		COALESCE(revenue, 0)::float8, COALESCE(profit, 0)::float8, refreshed_at`
	if err := scan(`SELECT campaign_id::text, `+pick+`, NULL::text, NULL::text FROM intel_api.campaign_result_v1
		WHERE time_window = $1 AND (cardinality($2::text[]) = 0 OR account = ANY($2))`, out.Campaigns); err != nil {
		return out, err
	}
	if err := scan(`SELECT item_id::text, `+pick+`, word::text, sureness::text FROM intel_api.ad_result_v1
		WHERE time_window = $1 AND (cardinality($2::text[]) = 0 OR account = ANY($2))`, out.Ads); err != nil {
		return out, err
	}
	return out, nil
}
