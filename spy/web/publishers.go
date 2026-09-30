package web

import (
	"context"
	"net/http"
	"strings"
)

// publishers is every publisher with its checks and sightings over the
// window, and the last 30 days' mix of operators and verticals.
// Filters: q (name, domain), network.
func (s *Server) publishers(r *http.Request) (any, error) {
	ctx := r.Context()
	win, err := s.window(ctx, r)
	if err != nil {
		return nil, err
	}
	var w where
	f, t := w.arg(win.From), w.arg(win.To)
	if v := strings.TrimSpace(r.URL.Query().Get("q")); v != "" {
		w.add("(p.name ILIKE ? OR p.domain ILIKE ?)", like(v), like(v))
	}
	if v := r.URL.Query().Get("network"); v != "" {
		w.add("p.network_id::text = ?", v)
	}
	sql := `
		SELECT p.id, p.name, p.domain, p.network_id, r.checks, r.sightings, r.per_100_checks, r.share_pct,
		       r.hours_checked, r.operators, r.even_operators,
		       ps.sightings_30d, ps.share_30d_pct, ps.scrapes_30d, ps.phone_sightings_30d, ps.creatives_30d,
		       ps.operators_30d, ps.even_operators AS even_operators_30d, ps.first_seen_at, ps.last_seen_at
		FROM tracks_api.publisher_v1 p
		LEFT JOIN spy_api.publisher_range_v1(` + f + `, ` + t + `) r ON r.publisher_id = p.id
		LEFT JOIN spy_api.publisher_stats_v1 ps ON ps.publisher_id = p.id
		WHERE ` + w.sql() + `
		ORDER BY r.sightings DESC NULLS LAST, ps.sightings_30d DESC NULLS LAST, p.name`
	return s.cached(r, win, func(ctx context.Context) (any, error) {
		items, err := s.rowsQ(ctx, sql, w.args...)
		if err != nil {
			return nil, err
		}
		return map[string]any{"window": win.json(), "items": items}, nil
	})
}

// publisher is one publisher's page: its numbers, its top operators and
// creatives, and its verticals over the last 30 days.
func (s *Server) publisher(r *http.Request) (any, error) {
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
			SELECT p.id, p.name, p.domain, p.network_id, ps.sightings_total, ps.sightings_today, ps.sightings_7d,
			       ps.sightings_30d, ps.share_30d_pct, ps.scrapes_30d, ps.phone_sightings_30d, ps.creatives_30d,
			       ps.operators_30d, ps.operator_hhi, ps.even_operators, ps.verticals, ps.first_seen_at, ps.last_seen_at
			FROM tracks_api.publisher_v1 p
			LEFT JOIN spy_api.publisher_stats_v1 ps ON ps.publisher_id = p.id
			WHERE p.id = $1`, id)
		if err != nil {
			return nil, err
		}
		numbers, err := s.rowsQ(ctx, `SELECT * FROM spy_api.publisher_range_v1($1, $2) WHERE publisher_id = $3`, win.From, win.To, id)
		if err != nil {
			return nil, err
		}
		operators, err := s.rowsQ(ctx, `
			SELECT (t->>'operator_id')::int AS id, o.code, COALESCE(o.display_name, o.name) AS name,
			       (t->>'sightings')::bigint AS sightings, (t->>'share_pct')::numeric AS share_pct
			FROM spy_api.publisher_stats_v1 ps, jsonb_array_elements(ps.top_operators) t
			JOIN spy_api.operator_v1 o ON o.id = (t->>'operator_id')::int
			WHERE ps.publisher_id = $1`, id)
		if err != nil {
			return nil, err
		}
		creatives, err := s.rowsQ(ctx, `
			SELECT cr.id, cr.image_url, cs.headline, (t->>'sightings')::bigint AS sightings, k.vertical_id
			FROM spy_api.publisher_stats_v1 ps, jsonb_array_elements(ps.top_creatives) t
			JOIN tracks_api.creative_v1 cr ON cr.id = (t->>'creative_id')::int
			LEFT JOIN spy_api.creative_stats_v1 cs ON cs.creative_id = cr.id
			LEFT JOIN spy_api.creative_class_v1 k ON k.creative_id = cr.id
			WHERE ps.publisher_id = $1`, id)
		if err != nil {
			return nil, err
		}
		s.name(creatives, "vertical_id")
		return map[string]any{"window": win.json(), "publisher": head, "numbers": numbers,
			"operators": operators, "creatives": creatives}, nil
	})
}
