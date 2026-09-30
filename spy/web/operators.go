package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

var operatorSorts = map[string]string{
	"presence":  "r.presence DESC NULLS LAST",
	"share":     "r.share_pct DESC NULLS LAST",
	"momentum":  "r.momentum_rank ASC NULLS LAST",
	"launches":  "r.launches DESC NULLS LAST",
	"hit_rate":  "r.hit_rate_pct DESC NULLS LAST",
	"creatives": "os.live_creatives_count DESC NULLS LAST",
	"name":      "lower(COALESCE(o.display_name, o.name)) ASC",
}

// operators is the operators list over the window. Filters: q (name, code,
// brand), vertical, kind (direct, affiliate, arbitrage), publisher, network.
func (s *Server) operators(r *http.Request) (any, error) {
	ctx := r.Context()
	win, err := s.window(ctx, r)
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	sort, ok := operatorSorts[q.Get("sort")]
	if q.Get("sort") == "" {
		sort, ok = operatorSorts["presence"], true
	}
	if !ok {
		return nil, bad("sort must be one of presence, share, momentum, launches, hit_rate, creatives, name")
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
		       count(*) OVER () AS total
		FROM ` + from + ` r
		JOIN spy_api.operator_v1 o ON o.id = r.operator_id
		LEFT JOIN spy_api.operator_stats_v1 os ON os.operator_id = r.operator_id
		LEFT JOIN spy_api.direction_v1 d ON d.kind = 'operator' AND d.subject_id = r.operator_id
		WHERE ` + w.sql() + `
		ORDER BY ` + sort + `, r.operator_id
		LIMIT ` + lim + ` OFFSET ` + off
	return s.cached(r, win, func(ctx context.Context) (any, error) {
		items, err := s.rowsQ(ctx, sql, w.args...)
		if err != nil {
			return nil, err
		}
		total := int64(0)
		for _, it := range items {
			if t, ok := it["total"].(int64); ok {
				total = t
			}
			delete(it, "total")
		}
		s.name(items, "vertical_id")
		return map[string]any{"window": win.json(), "items": items, "total": total}, nil
	})
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
	return s.cached(r, win, func(ctx context.Context) (any, error) {
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
		parts := []struct {
			key, sql string
			args     []any
		}{
			{"accounts", `
				SELECT a.id, a.network_id, a.external_id, a.org_external_id, a.first_seen_at, a.last_seen_at
				FROM spy_api.account_operator_v1 ao
				JOIN tracks_api.account_v1 a ON a.id = ao.account_id
				WHERE ao.operator_id = $1
				ORDER BY a.last_seen_at DESC LIMIT 100`, []any{id}},
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
}
