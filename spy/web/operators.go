package web

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var operatorSorts = map[string]sortBy{
	"presence":  {"r.presence", true},
	"share":     {"r.share_pct", true},
	"momentum":  {"r.momentum_rank", false},
	"launches":  {"r.launches", true},
	"hit_rate":  {"r.hit_rate_pct", true},
	"creatives": {"os.live_creatives_count", true},
	"name":      {"lower(COALESCE(o.display_name, o.name))", false},
}

// operators is the operators list over the window. Filters: q (name, code,
// brand), vertical, kind (direct, affiliate, arbitrage), publisher, network,
// show (watched, hidden, all; none: all but the hidden). Sorts:
// operatorSorts, rev. Each row has its best creative of the last 7 days.
func (s *Server) operators(r *http.Request) (any, error) {
	ctx := r.Context()
	win, err := s.window(ctx, r)
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	sort, err := order(r, operatorSorts, "presence")
	if err != nil {
		return nil, err
	}
	limit, err := intParam(r, "limit", 50, 200)
	if err != nil {
		return nil, err
	}
	offset, err := intParam(r, "offset", 0, 0)
	if err != nil {
		return nil, err
	}
	var w where
	from := rangeSource(win, &w, "operator_recent_v1", "operator_range_v1")
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		p := w.arg(like(v))
		w.conds = append(w.conds, fmt.Sprintf(
			"(o.name ILIKE %[1]s OR o.display_name ILIKE %[1]s OR o.code ILIKE %[1]s OR EXISTS (SELECT 1 FROM unnest(os.brands) br WHERE br ILIKE %[1]s))", p))
	}
	if v := q.Get("vertical"); v != "" {
		w.add("os.vertical = ?", v)
	}
	if v := q.Get("kind"); v != "" {
		w.add("o.kind = ?", v)
	}
	switch q.Get("show") {
	case "":
		w.add("NOT COALESCE(m.hidden, FALSE)")
	case "watched":
		w.add("m.watched")
	case "hidden":
		w.add("m.hidden")
	case "all":
	default:
		return nil, bad("show must be watched, hidden or all")
	}
	for _, f := range []struct{ param, cond string }{
		{"publisher", "os.publisher_ids @> ARRAY[?::int]"},
		{"network", "r.network_id = ?"},
	} {
		if v := q.Get(f.param); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, bad("%s must be an id", f.param)
			}
			w.add(f.cond, n)
		}
	}
	lim, off := w.arg(limit), w.arg(offset)
	sql := `
		SELECT r.operator_id AS id, r.network_id, o.code, COALESCE(o.display_name, o.name) AS name, o.kind,
		       os.vertical AS vertical_id, os.brands[1:5] AS brands, os.creatives_count, os.live_creatives_count,
		       r.sightings, r.checks, r.presence, r.share_pct, r.rank, r.momentum, r.momentum_low, r.momentum_high,
		       r.momentum_word, r.momentum_sure, r.publishers, r.first_seen_at, r.launches, r.hits, r.misses,
		       r.testing, r.hit_rate_pct, r.hit_rate_low_pct, r.hit_rate_high_pct,
		       d.direction, d.reason_text AS direction_text,
		       COALESCE(m.hidden, FALSE) AS hidden, COALESCE(m.watched, FALSE) AS watched, m.nickname,
		       count(*) OVER () AS total
		FROM ` + from + ` r
		JOIN spy_api.operator_v1 o ON o.id = r.operator_id
		LEFT JOIN spy_api.operator_stats_v1 os ON os.operator_id = r.operator_id
		LEFT JOIN spy_api.direction_v1 d ON d.kind = 'operator' AND d.subject_id = r.operator_id
		LEFT JOIN spy_api.operator_mark_v1 m ON m.operator_id = r.operator_id
		WHERE ` + w.sql() + `
		ORDER BY ` + sort + `, r.operator_id
		LIMIT ` + lim + ` OFFSET ` + off
	return s.cached(r, win, func(ctx context.Context) (any, error) {
		items, err := s.rowsQ(ctx, sql, w.args...)
		if err != nil {
			return nil, err
		}
		total := int64(0)
		ids := make([]int32, 0, len(items))
		for _, it := range items {
			if t, ok := it["total"].(int64); ok {
				total = t
			}
			delete(it, "total")
			ids = append(ids, it["id"].(int32))
		}
		s.name(items, "vertical_id")
		if err := s.addBest(ctx, items, ids); err != nil {
			return nil, err
		}
		return map[string]any{"window": win.json(), "items": items, "total": total}, nil
	})
}

// addBest puts each operator's best creative on its row: the one seen most
// in the last 7 days, junk left out, with its image, headline and vertical.
func (s *Server) addBest(ctx context.Context, items []map[string]any, ids []int32) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.rowsQ(ctx, `
		SELECT DISTINCT ON (cs.operator_id) cs.operator_id, cs.creative_id AS id, cr.image_url, cs.headline, k.vertical_id
		FROM spy_api.creative_stats_v1 cs
		JOIN tracks_api.creative_v1 cr ON cr.id = cs.creative_id
		LEFT JOIN spy_api.creative_class_v1 k ON k.creative_id = cs.creative_id
		WHERE cs.operator_id = ANY ($1) AND NOT cs.is_junk
		ORDER BY cs.operator_id, cs.sightings_7d DESC NULLS LAST, cs.creative_id`, ids)
	if err != nil {
		return err
	}
	s.name(rows, "vertical_id")
	by := map[int32]map[string]any{}
	for _, r := range rows {
		by[r["operator_id"].(int32)] = r
		delete(r, "operator_id")
	}
	for _, it := range items {
		if b, ok := by[it["id"].(int32)]; ok {
			it["best"] = b
		}
	}
	return nil
}

// operator is one operator's page: its accounts and brands, where it runs,
// its numbers over the window, and its creatives with the most presence.
func (s *Server) operator(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	win, err := s.window(ctx, r)
	if err != nil {
		return nil, err
	}
	v, err := s.cached(r, win, func(ctx context.Context) (any, error) {
		head, err := s.oneQ(ctx, `
			SELECT o.id, o.code, COALESCE(o.display_name, o.name) AS name, o.name AS full_name, o.kind,
			       os.vertical AS vertical_id, os.brands, os.creatives_count, os.live_creatives_count,
			       os.sightings_total, os.sightings_today, os.sightings_7d, os.sightings_30d, os.share_7d_pct,
			       os.share_30d_pct, os.first_seen_at, os.last_seen_at,
			       d.direction, d.direction_since, d.reason_text AS direction_text,
			       z.share_24h_pct, z.rank_24h, z.share_7d_pct AS vertical_share_7d_pct, z.rank_7d
			FROM spy_api.operator_v1 o
			LEFT JOIN spy_api.operator_stats_v1 os ON os.operator_id = o.id
			LEFT JOIN spy_api.direction_v1 d ON d.kind = 'operator' AND d.subject_id = o.id
			LEFT JOIN spy_api.size_v1 z ON z.kind = 'operator' AND z.subject_id = o.id
			WHERE o.id = $1`, id)
		if err != nil {
			return nil, err
		}
		s.name([]map[string]any{head}, "vertical_id")
		out := map[string]any{"window": win.json(), "operator": head}

		var w where
		src := rangeSource(win, &w, "operator_recent_v1", "operator_range_v1")
		numbers, err := s.rowsQ(ctx, `SELECT * FROM `+src+` r WHERE r.operator_id = `+w.arg(id), w.args...)
		if err != nil {
			return nil, err
		}
		out["numbers"] = numbers

		var cw where
		csrc := rangeSource(win, &cw, "creative_recent_v1", "creative_range_v1")
		creatives, err := s.rowsQ(ctx, `
			SELECT r.creative_id AS id, cr.image_url, cs.headline, k.vertical_id, r.presence, r.momentum,
			       r.momentum_word, r.running, r.first_seen_at, r.last_seen_at, r.lifespan_days
			FROM `+csrc+` r
			JOIN spy_api.creative_stats_v1 cs ON cs.creative_id = r.creative_id
			JOIN tracks_api.creative_v1 cr ON cr.id = r.creative_id
			LEFT JOIN spy_api.creative_class_v1 k ON k.creative_id = r.creative_id
			WHERE r.operator_id = `+cw.arg(id)+` AND NOT r.is_junk
			ORDER BY r.presence DESC NULLS LAST, r.creative_id
			LIMIT 48`, cw.args...)
		if err != nil {
			return nil, err
		}
		s.name(creatives, "vertical_id")
		out["creatives"] = creatives

		d0, d1 := win.days()
		s0, s1 := d0, d1
		if win.Recent || win.Hours > 0 {
			s0 = d1.AddDate(0, 0, -29)
		} else if s1.Sub(s0) > 119*24*time.Hour {
			s0 = s1.AddDate(0, 0, -119)
		}
		parts := []struct {
			key, sql string
			args     []any
		}{
			{"accounts", `
				SELECT a.id, a.network_id, a.external_id, a.org_external_id, a.first_seen_at, a.last_seen_at,
				       om.reason, om.agency
				FROM spy_api.account_operator_v1 ao
				JOIN tracks_api.account_v1 a ON a.id = ao.account_id
				LEFT JOIN spy_api.operator_member_v1 om ON om.member = 'account' AND om.member_id = a.id
				WHERE ao.operator_id = $1
				ORDER BY a.last_seen_at DESC LIMIT 100`, []any{id}},
			{"sites", `
				SELECT om.member_id AS id, om.domain, om.reason
				FROM spy_api.operator_member_v1 om
				WHERE om.member = 'site' AND om.operator_id = $1
				ORDER BY om.domain LIMIT 100`, []any{id}},
			{"series", operatorSeriesSQL, []any{id, s0, s1}},
			{"campaigns", operatorCampaignsSQL, []any{id, d0, d1}},
			{"brands", `
				SELECT b.name, n.sightings, n.ads
				FROM (SELECT d.brand_id, sum(d.sightings) AS sightings, count(DISTINCT d.ad_id) AS ads
				      FROM tracks_api.ad_account_daily_v1 d
				      JOIN spy_api.account_operator_v1 ao ON ao.account_id = d.account_id
				      WHERE ao.operator_id = $1 AND d.day BETWEEN $2 AND $3 GROUP BY 1) n
				JOIN tracks_api.brand_v1 b ON b.id = n.brand_id
				ORDER BY n.sightings DESC LIMIT 30`, []any{id, d0, d1}},
			{"publishers", `
				SELECT p.id, p.name, dv.code AS device, n.sightings
				FROM (SELECT d.publisher_id, d.device_id, sum(d.sightings) AS sightings
				      FROM tracks_api.ad_account_daily_v1 d
				      JOIN spy_api.account_operator_v1 ao ON ao.account_id = d.account_id
				      WHERE ao.operator_id = $1 AND d.day BETWEEN $2 AND $3 GROUP BY 1, 2) n
				JOIN tracks_api.publisher_v1 p ON p.id = n.publisher_id
				JOIN tracks_api.device_v1 dv ON dv.id = n.device_id
				ORDER BY n.sightings DESC`, []any{id, d0, d1}},
		}
		for _, p := range parts {
			rows, err := s.rowsQ(ctx, p.sql, p.args...)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p.key, err)
			}
			out[p.key] = rows
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	// What people marked changes on a click: read fresh, beside the kept answer.
	out := maps.Clone(v.(map[string]any))
	if out["mark"], out["notices"], err = s.marksFor(ctx, id); err != nil {
		return nil, err
	}
	return out, nil
}

// operatorSeriesSQL is an operator's volume per UTC day from $2 to $3:
// sightings of its accounts' ads, how many creatives, and presence (per 100
// checks of the networks its accounts run on). A day nobody checked has no
// presence.
const operatorSeriesSQL = `
	WITH acc AS (SELECT account_id FROM spy_api.account_operator_v1 WHERE operator_id = $1),
	net AS (SELECT DISTINCT a.network_id FROM tracks_api.account_v1 a JOIN acc ON acc.account_id = a.id),
	seen AS (
		SELECT d.day, sum(d.sightings) AS sightings, count(DISTINCT d.creative_id) AS creatives
		FROM tracks_api.ad_account_daily_v1 d JOIN acc ON acc.account_id = d.account_id
		WHERE d.day BETWEEN $2 AND $3 GROUP BY 1),
	checked AS (
		SELECT (c.hour AT TIME ZONE 'UTC')::date AS day, sum(c.scrapes) AS checks
		FROM tracks_api.scrape_coverage_v2 c
		JOIN tracks_api.publisher_v1 p ON p.id = c.publisher_id
		WHERE p.network_id IN (SELECT network_id FROM net) AND c.closed
		  AND c.hour >= $2::date::timestamp AT TIME ZONE 'UTC' AND c.hour < ($3::date + 1)::timestamp AT TIME ZONE 'UTC'
		GROUP BY 1)
	SELECT g.day::date AS day, COALESCE(seen.sightings, 0) AS sightings, COALESCE(seen.creatives, 0) AS creatives,
	       checked.checks,
	       CASE WHEN checked.checks > 0 THEN round(100.0 * COALESCE(seen.sightings, 0) / checked.checks, 4) END AS presence
	FROM generate_series($2::date::timestamp, $3::date::timestamp, interval '1 day') AS g(day)
	LEFT JOIN seen ON seen.day = g.day::date
	LEFT JOIN checked ON checked.day = g.day::date
	ORDER BY 1`

// operatorCampaignsSQL is an operator's campaigns seen from $2 to $3, the
// most seen first, each with its brands: on Taboola a campaign shows one
// brand (a campaign setting), so this is where brands belong. A campaign's
// brands are those of its account's ads with its creatives.
const operatorCampaignsSQL = `
	WITH acc AS (SELECT account_id FROM spy_api.account_operator_v1 WHERE operator_id = $1),
	cc AS (
		SELECT cc.campaign_id, c.account_id, cc.creative_id, sum(cc.sightings) AS sightings,
		       max(cc.last_seen_at) AS last_seen_at
		FROM tracks_api.campaign_v1 c
		JOIN acc ON acc.account_id = c.account_id
		JOIN tracks_api.creative_campaign_daily_v1 cc ON cc.campaign_id = c.id
		WHERE cc.day BETWEEN $2 AND $3
		GROUP BY 1, 2, 3),
	top AS (
		SELECT campaign_id, account_id, sum(sightings) AS sightings, max(last_seen_at) AS last_seen_at,
		       count(*) AS creatives, array_agg(creative_id) AS creative_ids
		FROM cc GROUP BY 1, 2 ORDER BY 3 DESC, 1 LIMIT 50)
	SELECT c.id, c.external_id, c.name, c.parent_name, a.external_id AS account, t.sightings, t.creatives,
	       t.last_seen_at,
	       ARRAY(SELECT b.name
	             FROM tracks_api.ad_account_daily_v1 d JOIN tracks_api.brand_v1 b ON b.id = d.brand_id
	             WHERE d.account_id = t.account_id AND d.creative_id = ANY (t.creative_ids) AND d.day BETWEEN $2 AND $3
	             GROUP BY b.name ORDER BY sum(d.sightings) DESC, b.name LIMIT 5) AS brands
	FROM top t
	JOIN tracks_api.campaign_v1 c ON c.id = t.campaign_id
	LEFT JOIN tracks_api.account_v1 a ON a.id = t.account_id
	ORDER BY t.sightings DESC, c.id`
