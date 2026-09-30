package load

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func tbAccounts(ctx context.Context, tx pgx.Tx, a answer, body []byte) error {
	rows, err := decodeRows(body, "results")
	if err != nil {
		return err
	}
	b := &pgx.Batch{}
	for _, r := range rows {
		acc := r.str("account_id")
		if acc == "" {
			continue
		}
		id, _ := r.id("id")
		b.Queue(`
			INSERT INTO intel.tb_account (account, login, numeric_id, name, type, currency, time_zone, fetched_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (account) DO UPDATE SET login = EXCLUDED.login, numeric_id = EXCLUDED.numeric_id,
				name = EXCLUDED.name, type = EXCLUDED.type, currency = EXCLUDED.currency,
				time_zone = EXCLUDED.time_zone, fetched_at = EXCLUDED.fetched_at
			WHERE intel.tb_account.fetched_at <= EXCLUDED.fetched_at`,
			acc, a.Login, id, r.str("name"), r.str("type"), r.str("currency"), r.str("time_zone_name"), a.FetchedAt)
	}
	return tx.SendBatch(ctx, b).Close()
}

// volatile fields change without anyone changing a setting; they stay out
// of the versions.
var volatile = map[string]bool{"spent": true}

func settingsOf(r row) row {
	s := row{}
	for k, v := range r {
		if !volatile[k] {
			s[k] = v
		}
	}
	return s
}

func tbCampaigns(ctx context.Context, tx pgx.Tx, a answer, body []byte) error {
	rows, err := decodeRows(body, "results")
	if err != nil {
		return err
	}
	b := &pgx.Batch{}
	var ids []int64
	for _, r := range rows {
		id, ok := r.id("id")
		if !ok {
			continue
		}
		ids = append(ids, id)
		var group *int64
		if g, ok := r.id("campaign_group_id"); ok {
			group = &g
		}
		var platforms []string
		if pt, ok := r["platform_targeting"].(map[string]any); ok && pt["type"] == "INCLUDE" {
			if vs, ok := pt["value"].([]any); ok {
				for _, v := range vs {
					platforms = append(platforms, fmt.Sprint(v))
				}
			}
		}
		if platforms == nil {
			platforms = []string{}
		}
		settings := settingsOf(r)
		b.Queue(`
			INSERT INTO intel.tb_campaign (campaign_id, account, group_id, name, status, is_active, bid_strategy, cpc,
				daily_cap, spending_limit, platforms, traffic_allocation_mode, settings, first_seen_at, fetched_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)
			ON CONFLICT (campaign_id) DO UPDATE SET account = EXCLUDED.account, group_id = EXCLUDED.group_id,
				name = EXCLUDED.name, status = EXCLUDED.status, is_active = EXCLUDED.is_active,
				bid_strategy = EXCLUDED.bid_strategy, cpc = EXCLUDED.cpc, daily_cap = EXCLUDED.daily_cap,
				spending_limit = EXCLUDED.spending_limit, platforms = EXCLUDED.platforms,
				traffic_allocation_mode = EXCLUDED.traffic_allocation_mode, settings = EXCLUDED.settings,
				first_seen_at = LEAST(intel.tb_campaign.first_seen_at, EXCLUDED.first_seen_at),
				fetched_at = EXCLUDED.fetched_at, gone_at = NULL
			WHERE intel.tb_campaign.fetched_at <= EXCLUDED.fetched_at`,
			id, a.Account, group, r.str("name"), r.str("status"), r.bool("is_active"), r.str("bid_strategy"),
			nullNum(r, "cpc"), nullNum(r, "daily_cap"), nullNum(r, "spending_limit"), platforms,
			r.str("traffic_allocation_mode"), settings, a.FetchedAt)
		b.Queue(`
			INSERT INTO intel.tb_campaign_version (campaign_id, valid_from, settings)
			SELECT $1, $2, $3
			WHERE NOT EXISTS (
				SELECT 1 FROM (SELECT settings FROM intel.tb_campaign_version
				               WHERE campaign_id = $1 AND valid_from <= $2 ORDER BY valid_from DESC LIMIT 1) last
				WHERE last.settings = $3::jsonb)
			ON CONFLICT DO NOTHING`, id, a.FetchedAt, settings)
		b.Queue(`
			INSERT INTO intel.tb_campaign_status (campaign_id, valid_from, account, status)
			SELECT $1, $2, $3, $4
			WHERE $4 <> '' AND NOT EXISTS (
				SELECT 1 FROM (SELECT status FROM intel.tb_campaign_status
				               WHERE campaign_id = $1 AND valid_from <= $2 ORDER BY valid_from DESC LIMIT 1) last
				WHERE last.status = $4)
			ON CONFLICT DO NOTHING`, id, a.FetchedAt, a.Account, r.str("status"))
	}
	if ids == nil {
		ids = []int64{}
	}
	// Taboola lists no deleted campaign; one we knew that this list lacks is
	// gone as of this answer.
	b.Queue(`
		WITH gone AS (
			UPDATE intel.tb_campaign SET gone_at = $3
			WHERE account = $1 AND gone_at IS NULL AND fetched_at < $3 AND NOT (campaign_id = ANY($2))
			RETURNING campaign_id
		)
		INSERT INTO intel.tb_campaign_status (campaign_id, valid_from, account, status)
		SELECT campaign_id, $3, $1, 'DELETED' FROM gone
		ON CONFLICT DO NOTHING`,
		a.Account, ids, a.FetchedAt)
	return tx.SendBatch(ctx, b).Close()
}

func tbItems(ctx context.Context, tx pgx.Tx, a answer, body []byte) error {
	rows, err := decodeRows(body, "results")
	if err != nil {
		return err
	}
	campaign, err := strconv.ParseInt(a.param("campaign_id"), 10, 64)
	if err != nil {
		return fmt.Errorf("items answer without its campaign: %w", err)
	}
	b := &pgx.Batch{}
	ids := []int64{}
	for _, r := range rows {
		id, ok := r.id("id")
		if !ok {
			continue
		}
		ids = append(ids, id)
		custom := ""
		if cd, ok := r["custom_data"].(map[string]any); ok {
			custom = row(cd).str("custom_id")
		}
		reject := ""
		if pr, ok := r["policy_review"].(map[string]any); ok {
			reject = row(pr).str("reject_reason")
		}
		settings := settingsOf(r)
		b.Queue(`
			INSERT INTO intel.tb_item (item_id, campaign_id, account, title, url, thumbnail_url, custom_id, status,
				is_active, approval_state, reject_reason, settings, first_seen_at, fetched_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)
			ON CONFLICT (item_id) DO UPDATE SET campaign_id = EXCLUDED.campaign_id, account = EXCLUDED.account,
				title = EXCLUDED.title, url = EXCLUDED.url, thumbnail_url = EXCLUDED.thumbnail_url,
				custom_id = EXCLUDED.custom_id, status = EXCLUDED.status, is_active = EXCLUDED.is_active,
				approval_state = EXCLUDED.approval_state, reject_reason = EXCLUDED.reject_reason,
				settings = EXCLUDED.settings,
				first_seen_at = LEAST(intel.tb_item.first_seen_at, EXCLUDED.first_seen_at),
				fetched_at = EXCLUDED.fetched_at, gone_at = NULL
			WHERE intel.tb_item.fetched_at <= EXCLUDED.fetched_at`,
			id, campaign, a.Account, r.str("title"), r.str("url"), r.str("thumbnail_url"), custom, r.str("status"),
			r.bool("is_active"), r.str("approval_state"), reject, settings, a.FetchedAt)
		b.Queue(`
			INSERT INTO intel.tb_item_version (item_id, valid_from, title, thumbnail_url, url, status, approval_state, settings)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8
			WHERE NOT EXISTS (
				SELECT 1 FROM (SELECT settings FROM intel.tb_item_version
				               WHERE item_id = $1 AND valid_from <= $2 ORDER BY valid_from DESC LIMIT 1) last
				WHERE last.settings = $8::jsonb)
			ON CONFLICT DO NOTHING`,
			id, a.FetchedAt, r.str("title"), r.str("thumbnail_url"), r.str("url"), r.str("status"), r.str("approval_state"), settings)
	}
	b.Queue(`UPDATE intel.tb_item SET gone_at = $3
		WHERE campaign_id = $1 AND gone_at IS NULL AND fetched_at < $3 AND NOT (item_id = ANY($2))`,
		campaign, ids, a.FetchedAt)
	return tx.SendBatch(ctx, b).Close()
}

func tbCampaignDay(ctx context.Context, tx pgx.Tx, a answer, body []byte) error {
	rows, err := decodeRows(body, "results")
	if err != nil {
		return err
	}
	b := &pgx.Batch{}
	for _, r := range rows {
		id, ok := r.id("campaign")
		if !ok {
			continue
		}
		day, err := reportDay(r.str("date"))
		if err != nil {
			return err
		}
		b.Queue(`
			INSERT INTO intel.tb_campaign_day (campaign_id, day, account, impressions, visible_impressions, clicks, spent, conversions, fetched_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (campaign_id, day) DO UPDATE SET account = EXCLUDED.account, impressions = EXCLUDED.impressions,
				visible_impressions = EXCLUDED.visible_impressions, clicks = EXCLUDED.clicks, spent = EXCLUDED.spent,
				conversions = EXCLUDED.conversions, fetched_at = EXCLUDED.fetched_at
			WHERE intel.tb_campaign_day.fetched_at <= EXCLUDED.fetched_at`,
			id, day, a.Account, r.int("impressions"), r.int("visible_impressions"), r.int("clicks"), r.num("spent"),
			r.int("cpa_actions_num"), a.FetchedAt)
	}
	return tx.SendBatch(ctx, b).Close()
}

func tbSiteDay(ctx context.Context, tx pgx.Tx, a answer, body []byte) error {
	rows, err := decodeRows(body, "results")
	if err != nil {
		return err
	}
	b := &pgx.Batch{}
	for _, r := range rows {
		id, ok := r.id("campaign")
		site, ok2 := r.id("site_id")
		if !ok || !ok2 {
			continue
		}
		day, err := reportDay(r.str("date"))
		if err != nil {
			return err
		}
		b.Queue(`
			INSERT INTO intel.tb_site_day (campaign_id, site_id, day, account, site, site_name, impressions,
				visible_impressions, clicks, spent, conversions, blocking_level, fetched_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (campaign_id, day, site_id) DO UPDATE SET account = EXCLUDED.account, site = EXCLUDED.site,
				site_name = EXCLUDED.site_name, impressions = EXCLUDED.impressions,
				visible_impressions = EXCLUDED.visible_impressions, clicks = EXCLUDED.clicks, spent = EXCLUDED.spent,
				conversions = EXCLUDED.conversions, blocking_level = EXCLUDED.blocking_level, fetched_at = EXCLUDED.fetched_at
			WHERE intel.tb_site_day.fetched_at <= EXCLUDED.fetched_at`,
			id, site, day, a.Account, r.str("site"), r.str("site_name"), r.int("impressions"), r.int("visible_impressions"),
			r.int("clicks"), r.num("spent"), r.int("cpa_actions_num"), r.str("blocking_level"), a.FetchedAt)
	}
	return tx.SendBatch(ctx, b).Close()
}

func tbItemDay(ctx context.Context, tx pgx.Tx, a answer, body []byte) error {
	rows, err := decodeRows(body, "results")
	if err != nil {
		return err
	}
	day, err := a.day("from")
	if err != nil {
		return fmt.Errorf("item report without its day: %w", err)
	}
	if a.param("to") != a.param("from") {
		return fmt.Errorf("item report over %s..%s: only single days can be split per day", a.param("from"), a.param("to"))
	}
	b := &pgx.Batch{}
	for _, r := range rows {
		campaign, ok := r.id("campaign")
		if !ok {
			continue
		}
		item, ok := r.id("item")
		old := false
		if !ok {
			// Items of deleted campaigns: "4304222189 (Old version)".
			f := strings.Fields(r.str("old_item_version_id"))
			if len(f) == 0 {
				continue
			}
			if item, err = strconv.ParseInt(f[0], 10, 64); err != nil {
				continue
			}
			old = true
		}
		b.Queue(`
			INSERT INTO intel.tb_item_day (item_id, day, campaign_id, account, old_version, title, thumbnail_url, custom_id,
				impressions, visible_impressions, clicks, spent, conversions, fetched_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (item_id, day) DO UPDATE SET campaign_id = EXCLUDED.campaign_id, account = EXCLUDED.account,
				old_version = EXCLUDED.old_version, title = EXCLUDED.title, thumbnail_url = EXCLUDED.thumbnail_url,
				custom_id = EXCLUDED.custom_id, impressions = EXCLUDED.impressions,
				visible_impressions = EXCLUDED.visible_impressions, clicks = EXCLUDED.clicks, spent = EXCLUDED.spent,
				conversions = EXCLUDED.conversions, fetched_at = EXCLUDED.fetched_at
			WHERE intel.tb_item_day.fetched_at <= EXCLUDED.fetched_at`,
			item, day, campaign, a.Account, old, r.str("item_name"), r.str("thumbnail_url"), r.str("custom_id"),
			r.int("impressions"), r.int("visible_impressions"), r.int("clicks"), r.num("spent"), r.int("actions"), a.FetchedAt)
	}
	return tx.SendBatch(ctx, b).Close()
}

func tbBucket(ctx context.Context, tx pgx.Tx, a answer, body []byte) error {
	rows, err := decodeRows(body, "results")
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(a.param("time_zone"))
	if err != nil {
		loc = time.UTC
	}
	b := &pgx.Batch{}
	for _, r := range rows {
		campaign, ok := r.id("campaign_id")
		if !ok {
			continue
		}
		bucket, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSuffix(r.str("date"), ".0"), loc)
		if err != nil {
			return fmt.Errorf("bucket date %q: %w", r.str("date"), err)
		}
		b.Queue(`
			INSERT INTO intel.tb_bucket (campaign_id, bucket, account, visible_impressions, clicks, spent, conversions, fetched_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (campaign_id, bucket) DO UPDATE SET account = EXCLUDED.account,
				visible_impressions = EXCLUDED.visible_impressions, clicks = EXCLUDED.clicks, spent = EXCLUDED.spent,
				conversions = EXCLUDED.conversions, fetched_at = EXCLUDED.fetched_at
			WHERE intel.tb_bucket.fetched_at <= EXCLUDED.fetched_at`,
			campaign, bucket, a.Account, r.int("visible_impressions"), r.int("clicks"), r.num("spent"),
			r.int("cpa_actions_num"), a.FetchedAt)
	}
	return tx.SendBatch(ctx, b).Close()
}

func nullNum(r row, k string) *float64 {
	if r.str(k) == "" {
		return nil
	}
	f := r.num(k)
	return &f
}
