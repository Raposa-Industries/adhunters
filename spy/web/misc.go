package web

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// verticals is the fixed list, by category, with how many creatives the
// classifier put in each (sure and unsure).
func (s *Server) verticals(r *http.Request) (any, error) {
	return s.cache.get(r.Context(), "verticals", 10*time.Minute, func(ctx context.Context) (any, error) {
		counts, err := s.rows(ctx, `
			SELECT vertical_id, count(*) FILTER (WHERE NOT unsure) AS sure, count(*) FILTER (WHERE unsure) AS unsure
			FROM spy_api.creative_class_v1 WHERE vertical_id IS NOT NULL GROUP BY 1`)
		if err != nil {
			return nil, err
		}
		by := map[string]map[string]any{}
		for _, c := range counts {
			by[c["vertical_id"].(string)] = c
		}
		var cats []map[string]any
		for _, c := range s.list.Categories {
			var vs []map[string]any
			for _, v := range c.Verticals {
				n := by[v.ID]
				var sure, unsure any = int64(0), int64(0)
				if n != nil {
					sure, unsure = n["sure"], n["unsure"]
				}
				vs = append(vs, map[string]any{"id": v.ID, "name": v.Name, "covers": v.Covers, "catch_all": v.CatchAll,
					"creatives": sure, "unsure": unsure})
			}
			cats = append(cats, map[string]any{"id": c.ID, "name": c.Name, "verticals": vs})
		}
		return map[string]any{"categories": cats}, nil
	})
}

// facets are the values the ads filters offer.
func (s *Server) facets(r *http.Request) (any, error) {
	return s.cache.get(r.Context(), "facets", 10*time.Minute, func(ctx context.Context) (any, error) {
		out := map[string]any{}
		for _, p := range []struct{ key, sql string }{
			{"networks", `SELECT id, code, name FROM tracks_api.network_v1 ORDER BY id`},
			{"publishers", `
				SELECT p.id, p.name, p.network_id FROM tracks_api.publisher_v1 p
				JOIN spy_api.publisher_stats_v1 ps ON ps.publisher_id = p.id
				WHERE ps.sightings_30d > 0 ORDER BY ps.sightings_30d DESC`},
			{"trackers", `
				SELECT t AS value, count(*) AS creatives FROM spy_api.creative_stats_v1, unnest(trackers) t
				WHERE NOT is_junk AND running GROUP BY 1 ORDER BY 2 DESC LIMIT 50`},
			{"affiliates", `
				SELECT t AS value, count(*) AS creatives FROM spy_api.creative_stats_v1, unnest(affiliate_networks) t
				WHERE NOT is_junk AND running GROUP BY 1 ORDER BY 2 DESC LIMIT 50`},
		} {
			rows, err := s.rows(ctx, p.sql)
			if err != nil {
				return nil, err
			}
			out[p.key] = rows
		}
		return out, nil
	})
}

// pulse is the market over the window: each vertical's presence and
// momentum, and how many creatives are rising, fading and stopped now.
func (s *Server) pulse(r *http.Request) (any, error) {
	ctx := r.Context()
	win, err := s.window(ctx, r)
	if err != nil {
		return nil, err
	}
	// Verticals are always counted when asked, the last 24 hours too.
	var w where
	src := rangeSource(Window{Before: win.Before, From: win.From, To: win.To}, &w, "", "vertical_range_v1")
	return s.cached(r, win, func(ctx context.Context) (any, error) {
		verts, err := s.rowsQ(ctx, `
			SELECT vertical AS vertical_id, network_id, sightings, presence, presence_usual, share_pct, rank,
			       momentum, momentum_low, momentum_high, momentum_word, momentum_sure, momentum_rank
			FROM `+src+`
			ORDER BY network_id, momentum_rank NULLS LAST, sightings DESC`, w.args...)
		if err != nil {
			return nil, err
		}
		s.name(verts, "vertical_id")
		for _, v := range verts {
			if id, ok := v["vertical_id"].(string); ok {
				if vv, ok := s.list.Vertical(id); ok {
					v["category_id"] = vv.Category
					v["category_name"] = s.names[vv.Category]
				}
			}
		}
		now, err := s.rowsQ(ctx, `
			SELECT kind, direction, count(*) AS n FROM spy_api.direction_v1
			WHERE kind IN ('creative', 'operator') GROUP BY 1, 2 ORDER BY 1, 2`)
		if err != nil {
			return nil, err
		}
		return map[string]any{"window": win.json(), "verticals": verts, "direction": now}, nil
	})
}

// search is ⌘K: ads by headline, brand or operator (with their images),
// operators by name or code, publishers, and verticals by name.
func (s *Server) search(r *http.Request) (any, error) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) < 2 {
		return map[string]any{"ads": []any{}, "operators": []any{}, "publishers": []any{}, "verticals": []any{}}, nil
	}
	return s.cache.get(r.Context(), "search "+strings.ToLower(q), time.Minute, func(ctx context.Context) (any, error) {
		p := like(q)
		ads, err := s.rows(ctx, `
			SELECT cs.creative_id AS id, cr.image_url, cs.headline, b.name AS brand,
			       COALESCE(o.display_name, o.name) AS operator_name, cs.running
			FROM spy_api.creative_stats_v1 cs
			JOIN tracks_api.creative_v1 cr ON cr.id = cs.creative_id
			LEFT JOIN tracks_api.brand_v1 b ON b.id = cs.brand_id
			LEFT JOIN spy_api.operator_v1 o ON o.id = cs.operator_id
			WHERE NOT cs.is_junk AND (cs.headline ILIKE $1 OR b.name ILIKE $1 OR o.display_name ILIKE $1 OR o.name ILIKE $1)
			ORDER BY cs.running DESC, cs.sightings_7d DESC, cs.creative_id
			LIMIT 12`, p)
		if err != nil {
			return nil, err
		}
		ops, err := s.rows(ctx, `
			SELECT o.id, o.code, COALESCE(o.display_name, o.name) AS name, os.live_creatives_count
			FROM spy_api.operator_v1 o
			LEFT JOIN spy_api.operator_stats_v1 os ON os.operator_id = o.id
			WHERE o.name ILIKE $1 OR o.display_name ILIKE $1 OR o.code ILIKE $1
			ORDER BY os.sightings_7d DESC NULLS LAST, o.id
			LIMIT 8`, p)
		if err != nil {
			return nil, err
		}
		pubs, err := s.rows(ctx, `
			SELECT id, name, domain FROM tracks_api.publisher_v1
			WHERE name ILIKE $1 OR domain ILIKE $1 ORDER BY name LIMIT 5`, p)
		if err != nil {
			return nil, err
		}
		var verts []map[string]any
		lq := strings.ToLower(q)
		for _, v := range s.list.Verticals() {
			if strings.Contains(strings.ToLower(v.Name), lq) {
				verts = append(verts, map[string]any{"id": v.ID, "name": v.Name, "category_name": s.names[v.Category]})
			}
		}
		if verts == nil {
			verts = []map[string]any{}
		}
		return map[string]any{"ads": ads, "operators": ops, "publishers": pubs, "verticals": verts}, nil
	})
}

// events are the latest changes of direction (newest first), for a feed.
// kind (ad, creative, operator, vertical) and to (rising, fading, stopped)
// narrow it.
func (s *Server) events(r *http.Request) (any, error) {
	limit, err := intParam(r, "limit", 50, 200)
	if err != nil {
		return nil, err
	}
	var w where
	if v := r.URL.Query().Get("kind"); v != "" {
		w.add("e.kind = ?", v)
	}
	if v := r.URL.Query().Get("to"); v != "" {
		w.add("e.to_direction = ?", v)
	}
	lim := w.arg(limit)
	return s.rows(r.Context(), `
		SELECT e.id, e.at, e.kind, e.key, e.subject_id, e.from_direction, e.to_direction, e.reason_text,
		       CASE WHEN e.kind = 'creative' THEN cr.image_url END AS image_url,
		       CASE WHEN e.kind = 'creative' THEN cs.headline END AS headline
		FROM spy_api.direction_event_v1 e
		LEFT JOIN tracks_api.creative_v1 cr ON e.kind = 'creative' AND cr.id = e.subject_id
		LEFT JOIN spy_api.creative_stats_v1 cs ON e.kind = 'creative' AND cs.creative_id = e.subject_id
		WHERE `+w.sql()+`
		ORDER BY e.id DESC LIMIT `+lim, w.args...)
}
