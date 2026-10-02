package web

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// where collects SQL conditions and their arguments.
type where struct {
	conds []string
	args  []any
}

// add appends a condition; each ? becomes the next argument.
func (w *where) add(cond string, args ...any) {
	for _, a := range args {
		w.args = append(w.args, a)
		cond = strings.Replace(cond, "?", "$"+strconv.Itoa(len(w.args)), 1)
	}
	w.conds = append(w.conds, cond)
}

// arg adds an argument and returns its placeholder.
func (w *where) arg(a any) string {
	w.args = append(w.args, a)
	return "$" + strconv.Itoa(len(w.args))
}

func (w *where) sql() string {
	if len(w.conds) == 0 {
		return "TRUE"
	}
	return strings.Join(w.conds, " AND ")
}

// sortBy is one order a list offers: a column and its usual direction.
type sortBy struct {
	col  string
	desc bool
}

// adSorts are the orders the ads list offers. Presence first: how often you
// would see the ad if you looked now.
var adSorts = map[string]sortBy{
	"presence":  {"r.presence", true},
	"momentum":  {"r.momentum_rank", false},
	"share":     {"r.share_pct", true},
	"sightings": {"r.sightings", true},
	"newest":    {"r.first_seen_at", true},
	"last_seen": {"r.last_seen_at", true},
	"lifespan":  {"r.lifespan_days", true},
	"days":      {"cs.active_days", true},
	"total":     {"cs.sightings_total", true},
	"name":      {"lower(cs.headline)", false},
}

// order reads sort (def when empty) and rev (1: the other way round, Z to A
// for names) into an ORDER BY. Rows without a value stay last either way.
func order(r *http.Request, sorts map[string]sortBy, def string) (string, error) {
	name := r.URL.Query().Get("sort")
	if name == "" {
		name = def
	}
	sb, ok := sorts[name]
	if !ok {
		names := make([]string, 0, len(sorts))
		for k := range sorts {
			names = append(names, k)
		}
		slices.Sort(names)
		return "", bad("sort must be one of %s", strings.Join(names, ", "))
	}
	desc := sb.desc
	switch r.URL.Query().Get("rev") {
	case "", "0":
	case "1":
		desc = !desc
	default:
		return "", bad("rev must be 1 or nothing")
	}
	if desc {
		return sb.col + " DESC NULLS LAST", nil
	}
	return sb.col + " ASC NULLS LAST", nil
}

// rangeSource is the FROM for a subject's numbers: kept ready for the last
// 24 hours, computed when asked for any other range or against the period
// just before.
func rangeSource(win Window, w *where, recent, fn string) string {
	if win.stored() {
		return "spy_api." + recent
	}
	if win.Before {
		span := win.To.Sub(win.From)
		return fmt.Sprintf("spy_api.%s(%s, %s, %s, %s)", fn, w.arg(win.From), w.arg(win.To),
			w.arg(win.From.Add(-span)), w.arg(win.From))
	}
	return fmt.Sprintf("spy_api.%s(%s, %s)", fn, w.arg(win.From), w.arg(win.To))
}

// ads is the ads list: one row per creative (its best ad's headline), with
// its numbers over the window, filters and a sort.
//
// Filters: q (headline, brand, operator), category, vertical, operator,
// publisher, network, account (Tracks account id), tracker, affiliate,
// device (phone, desktop), status (new, running, ended, rising, fading,
// scaled, stopped), min_days, hidden (1: with the operators people hid).
// Sorts: adSorts, rev. limit (48, at most 100), offset.
func (s *Server) ads(r *http.Request) (any, error) {
	return s.adsUpTo(r, 100)
}

// adsUpTo is the ads list with at most max rows a page.
func (s *Server) adsUpTo(r *http.Request, max int) (any, error) {
	ctx := r.Context()
	win, err := s.window(ctx, r)
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	sort, err := order(r, adSorts, "presence")
	if err != nil {
		return nil, err
	}
	limit, err := intParam(r, "limit", 48, max)
	if err != nil {
		return nil, err
	}
	offset, err := intParam(r, "offset", 0, 0)
	if err != nil {
		return nil, err
	}
	var w where
	from := rangeSource(win, &w, "creative_recent_v1", "creative_range_v1")
	w.add("NOT r.is_junk")
	switch q.Get("hidden") {
	case "", "0":
		w.add("NOT EXISTS (SELECT 1 FROM spy_api.operator_mark_v1 hm WHERE hm.operator_id = cs.operator_id AND hm.hidden)")
	case "1":
	default:
		return nil, bad("hidden must be 1 or nothing")
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		p := w.arg(like(v))
		w.conds = append(w.conds, fmt.Sprintf("(cs.headline ILIKE %[1]s OR b.name ILIKE %[1]s OR o.name ILIKE %[1]s OR o.display_name ILIKE %[1]s)", p))
	}
	for _, f := range []struct{ param, cond string }{
		{"category", "k.category_id = ?"},
		{"vertical", "k.vertical_id = ?"},
		{"tracker", "? = ANY (cs.trackers)"},
		{"affiliate", "? = ANY (cs.affiliate_networks)"},
	} {
		if v := q.Get(f.param); v != "" {
			w.add(f.cond, v)
		}
	}
	for _, f := range []struct{ param, cond string }{
		{"operator", "cs.operator_id = ?"},
		{"publisher", "cs.publisher_ids @> ARRAY[?::int]"},
		{"network", "r.network_id = ?"},
		{"account", "? = ANY (cs.account_ids)"},
	} {
		if v := q.Get(f.param); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, bad("%s must be an id", f.param)
			}
			w.add(f.cond, n)
		}
	}
	switch q.Get("device") {
	case "":
	case "phone":
		w.add("r.phone_presence > 0")
	case "desktop":
		w.add("r.desktop_presence > 0")
	default:
		return nil, bad("device must be phone or desktop")
	}
	for _, st := range q["status"] {
		switch st {
		case "new":
			w.add("cs.is_new")
		case "running":
			w.add("r.running")
		case "ended":
			w.add("NOT r.running")
		case "rising":
			w.add("r.momentum_word = 'rising'")
		case "fading":
			w.add("r.momentum_word = 'fading'")
		case "scaled":
			w.add("r.scaled")
		case "stopped":
			w.add("d.direction = 'stopped'")
		case "":
		default:
			return nil, bad("status %q is not one of new, running, ended, rising, fading, scaled, stopped", st)
		}
	}
	if v := q.Get("min_days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, bad("min_days must be a number")
		}
		w.add("cs.active_days >= ?", n)
	}
	lim, off := w.arg(limit), w.arg(offset)
	sql := `
		SELECT r.creative_id AS id, r.network_id, cr.image_url, cr.format_type, cs.headline, ta.description, ta.cta,
		       b.name AS brand, cs.operator_id, o.code AS operator_code, COALESCE(o.display_name, o.name) AS operator_name,
		       k.category_id, k.vertical_id, COALESCE(k.unsure, TRUE) AS vertical_unsure,
		       r.sightings, r.checks, r.presence, r.presence_usual, r.phone_presence, r.desktop_presence,
		       r.share_pct, r.rank, r.momentum, r.momentum_low, r.momentum_high, r.momentum_word, r.momentum_sure,
		       r.fall_on_one_publisher, r.publishers, r.first_seen_at, r.last_seen_at, r.running, r.lifespan_days,
		       r.lifespan_pct, r.scaled, cs.is_new, cs.sightings_total, cs.active_days, cs.ads_count,
		       d.direction, d.reason_text AS direction_text,
		       count(*) OVER () AS total
		FROM ` + from + ` r
		JOIN spy_api.creative_stats_v1 cs ON cs.creative_id = r.creative_id
		JOIN tracks_api.creative_v1 cr ON cr.id = r.creative_id
		LEFT JOIN tracks_api.ad_v1 ta ON ta.id = cs.top_ad_id
		LEFT JOIN tracks_api.brand_v1 b ON b.id = cs.brand_id
		LEFT JOIN spy_api.operator_v1 o ON o.id = cs.operator_id
		LEFT JOIN spy_api.creative_class_v1 k ON k.creative_id = r.creative_id
		LEFT JOIN spy_api.direction_v1 d ON d.kind = 'creative' AND d.subject_id = r.creative_id
		WHERE ` + w.sql() + `
		ORDER BY ` + sort + `, r.creative_id
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
		s.name(items, "category_id", "vertical_id")
		if err := s.addSeries(ctx, items, ids, 14); err != nil {
			return nil, err
		}
		next := any(nil)
		if offset+len(items) < int(total) {
			next = offset + len(items)
		}
		return map[string]any{"window": win.json(), "items": items, "total": total, "next_offset": next}, nil
	})
}

// addSeries puts each creative's daily presence (its sparkline) on its row.
func (s *Server) addSeries(ctx context.Context, items []map[string]any, ids []int32, days int) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.rowsQ(ctx, `SELECT creative_id, day, presence FROM spy_api.creative_series_v1($1, $2, $3)`,
		ids, days, s.cfg.Now())
	if err != nil {
		return err
	}
	by := map[int32][]any{}
	for _, r := range rows {
		id := r["creative_id"].(int32)
		by[id] = append(by[id], r["presence"])
	}
	for _, it := range items {
		it["series"] = by[it["id"].(int32)]
	}
	return nil
}

// ad is one creative's page: what it is, its numbers over the window, its
// ads, where and when it runs, its links, campaigns, prices, and what
// Raposa found.
func (s *Server) ad(r *http.Request) (any, error) {
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
			SELECT cr.id, cr.creative_key, cr.image_url, cr.format_type, cr.video_duration, cr.language,
			       cr.first_seen_at, cr.last_seen_at, cs.headline, b.name AS brand, cs.operator_id,
			       o.code AS operator_code, COALESCE(o.display_name, o.name) AS operator_name, o.kind AS operator_kind,
			       k.category_id, k.vertical_id, k.confidence AS vertical_confidence, k.unsure AS vertical_unsure,
			       k.source AS vertical_source, cs.sightings_total, cs.sightings_today, cs.sightings_7d,
			       cs.sightings_30d, cs.active_days, cs.ads_count, cs.publishers_count, cs.is_new, cs.running,
			       cs.trackers, cs.affiliate_networks, cs.is_junk,
			       d.direction, d.direction_since, d.reason_text AS direction_text,
			       z.share_24h_pct, z.rank_24h, z.share_7d_pct, z.rank_7d, z.scaled
			FROM tracks_api.creative_v1 cr
			LEFT JOIN spy_api.creative_stats_v1 cs ON cs.creative_id = cr.id
			LEFT JOIN tracks_api.brand_v1 b ON b.id = cs.brand_id
			LEFT JOIN spy_api.operator_v1 o ON o.id = cs.operator_id
			LEFT JOIN spy_api.creative_class_v1 k ON k.creative_id = cr.id
			LEFT JOIN spy_api.direction_v1 d ON d.kind = 'creative' AND d.subject_id = cr.id
			LEFT JOIN spy_api.size_v1 z ON z.kind = 'creative' AND z.subject_id = cr.id
			WHERE cr.id = $1`, id)
		if err != nil {
			return nil, err
		}
		s.name([]map[string]any{head}, "category_id", "vertical_id")
		out := map[string]any{"window": win.json(), "creative": head}

		var w where
		src := rangeSource(win, &w, "creative_recent_v1", "creative_range_v1")
		numbers, err := s.rowsQ(ctx, `SELECT * FROM `+src+` r WHERE r.creative_id = `+w.arg(id), w.args...)
		if err != nil {
			return nil, err
		}
		out["numbers"] = numbers

		d0, d1 := win.days()
		parts := []struct {
			key, sql string
			args     []any
		}{
			{"ads", `
				SELECT a.id, a.headline, a.description, a.cta, b.name AS brand, acc.external_id AS account,
				       a.first_seen_at, a.last_seen_at, COALESCE(n.sightings, 0) AS sightings,
				       dir.direction, dir.reason_text AS direction_text
				FROM tracks_api.ad_v1 a
				LEFT JOIN (SELECT dd.ad_id, sum(dd.sightings) AS sightings FROM tracks_api.ad_daily_v1 dd
				           WHERE dd.creative_id = $1 AND dd.day BETWEEN $2 AND $3 GROUP BY 1) n ON n.ad_id = a.id
				LEFT JOIN tracks_api.brand_v1 b ON b.id = a.brand_id
				LEFT JOIN tracks_api.account_v1 acc ON acc.id = a.account_id
				LEFT JOIN spy_api.direction_v1 dir ON dir.kind = 'ad' AND dir.subject_id = a.id
				WHERE a.creative_id = $1
				ORDER BY sightings DESC, a.id DESC
				LIMIT 50`, []any{id, d0, d1}},
			{"publishers", `
				SELECT p.id, p.name, p.network_id, dv.code AS device, sum(dd.sightings) AS sightings,
				       sum(dd.scrapes) AS scrapes, min(dd.first_seen_at) AS first_seen_at, max(dd.last_seen_at) AS last_seen_at
				FROM tracks_api.ad_daily_v1 dd
				JOIN tracks_api.publisher_v1 p ON p.id = dd.publisher_id
				JOIN tracks_api.device_v1 dv ON dv.id = dd.device_id
				WHERE dd.creative_id = $1 AND dd.day BETWEEN $2 AND $3
				GROUP BY p.id, p.name, p.network_id, dv.code
				ORDER BY sightings DESC`, []any{id, d0, d1}},
			{"hours", hoursSQL, []any{id, win.To.Add(-hoursSpan), win.To}},
			{"links", `
				SELECT l.id, l.host, l.path, l.tracker, l.affiliate_network, l.item_id, l.params, l.sample_url,
				       n.sightings, n.last_seen_at
				FROM (SELECT cl.link_id, sum(cl.sightings) AS sightings, max(cl.last_seen_at) AS last_seen_at
				      FROM tracks_api.creative_link_daily_v1 cl
				      WHERE cl.creative_id = $1 AND cl.day BETWEEN $2 AND $3 GROUP BY 1) n
				JOIN tracks_api.link_v1 l ON l.id = n.link_id
				ORDER BY n.sightings DESC LIMIT 20`, []any{id, d0, d1}},
			{"campaigns", `
				SELECT c.id, c.network_id, c.external_id, c.name, c.parent_external_id, c.parent_name, c.objective,
				       acc.external_id AS account, n.sightings, n.last_seen_at,
				       (SELECT b.name FROM tracks_api.ad_account_daily_v1 d JOIN tracks_api.brand_v1 b ON b.id = d.brand_id
				        WHERE d.account_id = c.account_id AND d.creative_id = $1 AND d.day BETWEEN $2 AND $3
				        GROUP BY b.name ORDER BY sum(d.sightings) DESC, b.name LIMIT 1) AS brand
				FROM (SELECT cc.campaign_id, sum(cc.sightings) AS sightings, max(cc.last_seen_at) AS last_seen_at
				      FROM tracks_api.creative_campaign_daily_v1 cc
				      WHERE cc.creative_id = $1 AND cc.day BETWEEN $2 AND $3 GROUP BY 1) n
				JOIN tracks_api.campaign_v1 c ON c.id = n.campaign_id
				LEFT JOIN tracks_api.account_v1 acc ON acc.id = c.account_id
				ORDER BY n.sightings DESC LIMIT 20`, []any{id, d0, d1}},
			{"prices", `
				SELECT pub.network_id, nw.name AS network, count(DISTINCT p.day) AS days, sum(p.auctions) AS auctions, sum(p.rtb) AS rtb,
				       sum(p.clearing_n) AS clearing_n, sum(p.clearing_sum) / NULLIF(sum(p.clearing_n), 0) AS clearing_avg,
				       sum(p.clearing_p50 * p.clearing_n) / NULLIF(sum(p.clearing_n) FILTER (WHERE p.clearing_p50 IS NOT NULL), 0) AS clearing_typical,
				       sum(p.bid_n) AS bid_n, sum(p.bid_sum) / NULLIF(sum(p.bid_n), 0) AS bid_avg,
				       sum(p.bid_p50 * p.bid_n) / NULLIF(sum(p.bid_n) FILTER (WHERE p.bid_p50 IS NOT NULL), 0) AS bid_typical,
				       sum(p.second_n) AS second_n, sum(p.second_sum) / NULLIF(sum(p.second_n), 0) AS second_avg
				FROM spy_api.price_day_v1 p
				JOIN tracks_api.publisher_v1 pub ON pub.id = p.publisher_id
				JOIN tracks_api.network_v1 nw ON nw.id = pub.network_id
				WHERE p.creative_id = $1 AND p.day BETWEEN $2 AND $3
				GROUP BY 1, 2`, []any{id, d0, d1}},
			{"prices_by_publisher", `
				SELECT p.publisher_id, pub.name, dv.code AS device, sum(p.auctions) AS auctions,
				       sum(p.clearing_sum) / NULLIF(sum(p.clearing_n), 0) AS clearing_avg,
				       sum(p.bid_sum) / NULLIF(sum(p.bid_n), 0) AS bid_avg,
				       sum(p.second_sum) / NULLIF(sum(p.second_n), 0) AS second_avg
				FROM spy_api.price_day_v1 p
				JOIN tracks_api.publisher_v1 pub ON pub.id = p.publisher_id
				JOIN tracks_api.device_v1 dv ON dv.id = p.device_id
				WHERE p.creative_id = $1 AND p.day BETWEEN $2 AND $3
				GROUP BY 1, 2, 3 ORDER BY auctions DESC`, []any{id, d0, d1}},
			{"series", `SELECT day, sightings, checks, presence FROM spy_api.creative_series_v1(ARRAY[$1::int], $2, $3)`,
				seriesArgs(id, win, s.cfg.Now())},
		}
		for _, p := range parts {
			rows, err := s.rowsQ(ctx, p.sql, p.args...)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p.key, err)
			}
			out[p.key] = rows
		}
		out["hours_state"] = "database"
		if !win.Recent {
			out["hours_state"] = s.hoursState(ctx, win.To.Add(-hoursSpan), win.To)
		}
		out["raposa"], err = s.raposaFor(ctx, id)
		if err != nil {
			return nil, err
		}
		return out, nil
	})
}

// hoursSpan is how far back from a range's end the hour of day reads.
const hoursSpan = 7 * 24 * time.Hour

// hoursSQL is a creative's sightings by hour of day (São Paulo) and device.
const hoursSQL = `
	SELECT extract(hour FROM h.hour AT TIME ZONE 'America/Sao_Paulo')::int AS hour, dv.code AS device,
	       sum(h.sightings) AS sightings
	FROM tracks_api.ad_hourly_v1 h
	JOIN tracks_api.ad_v1 a ON a.id = h.ad_id
	JOIN tracks_api.device_v1 dv ON dv.id = h.device_id
	WHERE a.creative_id = $1 AND h.hour >= $2 AND h.hour < $3
	GROUP BY 1, 2 ORDER BY 1, 2`

// seriesArgs are creative_series_v1's arguments for the ad page: the last
// 30 days for the last 24, 48 or 72 hours, else every day of the range (the
// last 120 at most), so an old week reads day by day from the daily counts.
func seriesArgs(id int, win Window, now time.Time) []any {
	if win.Recent || win.Hours > 0 {
		return []any{id, 30, now}
	}
	d0, d1 := win.days()
	days := int(d1.Sub(d0).Hours()/24) + 1
	return []any{id, min(days, 120), win.To.Add(-time.Microsecond)}
}

// hoursState asks Tracks where the hourly counts of [from, to) are, and
// asks for archived days back (tracks_api.hourly_days_v1): "database" when
// every day is there, "coming" while Tracks brings some back, "archive" when
// it could not. Older hours than Tracks keeps are in its archive; the page
// waits for them. When Tracks cannot answer, the hours read as they are.
func (s *Server) hoursState(ctx context.Context, from, to time.Time) string {
	const q = `
		SELECT COALESCE(max(CASE state WHEN 'coming' THEN 2 WHEN 'archive' THEN 1 ELSE 0 END), 0)
		FROM tracks_api.hourly_days_v1($1, $2, true)`
	var n int
	var err error
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		// A savepoint: a failure here must not end the range's transaction.
		var sp pgx.Tx
		if sp, err = tx.Begin(ctx); err == nil {
			if err = sp.QueryRow(ctx, q, from, to).Scan(&n); err != nil {
				_ = sp.Rollback(ctx)
			} else {
				err = sp.Commit(ctx)
			}
		}
	} else {
		err = s.db.QueryRow(ctx, q, from, to).Scan(&n)
	}
	if err != nil {
		s.log.Warn("where the hourly counts are: Tracks did not answer", "err", err)
		return "database"
	}
	return [...]string{"database", "archive", "coming"}[n]
}

// adHours is the hour of day alone, never cached: the ad page asks again
// while Tracks brings archived hours back.
func (s *Server) adHours(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	win, err := s.window(ctx, r)
	if err != nil {
		return nil, err
	}
	state := "database"
	if !win.Recent {
		state = s.hoursState(ctx, win.To.Add(-hoursSpan), win.To)
	}
	rows, err := s.rows(ctx, hoursSQL, id, win.To.Add(-hoursSpan), win.To)
	if err != nil {
		return nil, err
	}
	return map[string]any{"state": state, "hours": rows}, nil
}

// oneQ is row through q(ctx).
func (s *Server) oneQ(ctx context.Context, sql string, args ...any) (map[string]any, error) {
	rows, err := s.rowsQ(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errNotFound
	}
	return rows[0], nil
}
