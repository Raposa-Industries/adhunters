// Package web serves AdHunters Spy: its pages, in the shared Frame, and the
// JSON they read, under /spy/ on the one address every app shares.
//
// It reads only published views: spy_api (Spy's numbers), tracks_api (what
// was seen) and raposa_api (investigations), and asks Raposa for an
// investigation through raposa_api.request_investigation_v1. Lists over the
// last 24 hours read what spy-numbers keeps ready; any other range is
// computed when asked (10 to 15 s at Tracks' volume), so each answer is kept
// for a few minutes and the pages show that they are waiting.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/shared/access"
	"github.com/Raposa-Industries/adhunters/shared/frame"
	"github.com/Raposa-Industries/adhunters/shared/verticals"
)

// Config tunes a Server. Zero values take the defaults.
type Config struct {
	Access     *access.Checker // nil: no login check (only for localhost and tests)
	RecentTTL  time.Duration   // how long a last-24-hours answer is kept (2 minutes)
	RangeTTL   time.Duration   // how long an answer over another range is kept (10 minutes)
	RangeLimit time.Duration   // how long a range may take (90 s)
	Now        func() time.Time
}

// Server answers Spy's pages and API.
type Server struct {
	db    *pgxpool.Pool
	log   *slog.Logger
	cfg   Config
	list  verticals.List
	names map[string]string // vertical and category id -> name
	cache *cache
	pages fs.FS
}

// New makes a Server.
func New(db *pgxpool.Pool, log *slog.Logger, cfg Config) (*Server, error) {
	l, err := verticals.Load()
	if err != nil {
		return nil, err
	}
	if cfg.RecentTTL <= 0 {
		cfg.RecentTTL = 2 * time.Minute
	}
	if cfg.RangeTTL <= 0 {
		cfg.RangeTTL = 10 * time.Minute
	}
	if cfg.RangeLimit <= 0 {
		cfg.RangeLimit = 90 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	names := map[string]string{}
	for _, c := range l.Categories {
		names[c.ID] = c.Name
		for _, v := range c.Verticals {
			names[v.ID] = v.Name
		}
	}
	sub, err := fs.Sub(pageFiles, "pages")
	if err != nil {
		return nil, err
	}
	return &Server{db: db, log: log, cfg: cfg, list: l, names: names, cache: newCache(), pages: sub}, nil
}

// Handler routes /spy/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/spy/_frame/", http.StripPrefix("/spy/_frame", frame.Handler()))
	mux.Handle("GET /spy/api/verticals", s.api(s.verticals))
	mux.Handle("GET /spy/api/facets", s.api(s.facets))
	mux.Handle("GET /spy/api/ads", s.api(s.ads))
	mux.Handle("GET /spy/api/ads/{id}", s.api(s.ad))
	mux.Handle("GET /spy/api/ads/export", http.HandlerFunc(s.exportAds))
	mux.Handle("GET /spy/api/ads/{id}/hours", s.api(s.adHours))
	mux.Handle("POST /spy/api/ads/{id}/investigate", s.api(s.investigate))
	mux.Handle("POST /spy/api/ads/{id}/vertical", s.api(s.fixVertical))
	mux.Handle("GET /spy/api/operators", s.api(s.operators))
	mux.Handle("GET /spy/api/operators/{id}", s.api(s.operator))
	mux.Handle("POST /spy/api/operators/{id}/mark", s.api(s.markOperator))
	mux.Handle("GET /spy/api/publishers", s.api(s.publishers))
	mux.Handle("GET /spy/api/publishers/{id}", s.api(s.publisher))
	mux.Handle("GET /spy/api/pulse", s.api(s.pulse))
	mux.Handle("GET /spy/api/search", s.api(s.search))
	mux.Handle("GET /spy/api/events", s.api(s.events))
	mux.Handle("/spy/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
	}))
	mux.Handle("/spy/", s.pageHandler())
	mux.Handle("GET /spy", http.RedirectHandler("/spy/", http.StatusMovedPermanently))
	var h http.Handler = mux
	if s.cfg.Access != nil {
		h = s.cfg.Access.Wrap(h)
	}
	return secure(h)
}

// errBad is a request the caller can fix; its text is shown.
type errBad struct{ msg string }

func (e errBad) Error() string { return e.msg }

func bad(format string, args ...any) error { return errBad{fmt.Sprintf(format, args...)} }

var errNotFound = errors.New("not found")

// api turns a function returning a value into a JSON handler.
func (s *Server) api(f func(*http.Request) (any, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := f(r)
		var b errBad
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, v)
		case errors.As(err, &b):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": b.msg})
		case errors.Is(err, errNotFound) || errors.Is(err, pgx.ErrNoRows):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		case errors.Is(err, context.DeadlineExceeded):
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "this range took too long; try a shorter one"})
		case r.Context().Err() != nil:
			// The person left; nothing to answer.
		default:
			s.log.Error("spy api failed", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "something failed on our side"})
		}
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// rows runs a query and returns each row as a map by column name.
func (s *Server) rows(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	rs, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rs, pgx.RowToMap)
	if out == nil {
		out = []map[string]any{}
	}
	return out, err
}

// row runs a query for one row; errNotFound when there is none.
func (s *Server) row(ctx context.Context, sql string, args ...any) (map[string]any, error) {
	rs, err := s.rows(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, errNotFound
	}
	return rs[0], nil
}

// Window is the range a list is about.
type Window struct {
	Recent bool      // the last 24 closed hours, kept ready by spy-numbers
	Hours  int       // the last this many closed hours (48, 72), counted when asked
	Before bool      // compared with the period just before it, not the usual weeks
	From   time.Time // [From, To)
	To     time.Time
}

// stored says the window's numbers are kept ready: the last 24 hours
// against the usual weeks.
func (w Window) stored() bool { return w.Recent && !w.Before }

// saoPaulo is where the team is: a range's dates are its days.
var saoPaulo = func() *time.Location {
	if l, err := time.LoadLocation("America/Sao_Paulo"); err == nil {
		return l
	}
	return time.FixedZone("America/Sao_Paulo", -3*3600) // no daylight saving since 2019
}()

// window reads from and to: RFC 3339, or YYYY-MM-DD for São Paulo days (a
// date "to" includes that day). Neither: the last 24 hours; "from=48h"
// alone: the last 48 hours, ending where the last 24 do. vs=before compares
// the range with the period of the same length just before it.
func (s *Server) window(ctx context.Context, r *http.Request) (Window, error) {
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	var before bool
	switch q.Get("vs") {
	case "", "usual":
	case "before":
		before = true
	default:
		return Window{}, bad("vs must be usual or before")
	}
	if from == "" && to == "" || strings.HasSuffix(from, "h") && to == "" {
		hours := 24
		if from != "" {
			n, err := strconv.Atoi(strings.TrimSuffix(from, "h"))
			if err != nil || n < 1 || n > 400*24 {
				return Window{}, bad("from: use 48h, 2026-10-01 or 2026-10-01T14:00:00Z")
			}
			hours = n
		}
		var end *time.Time
		if err := s.db.QueryRow(ctx, `SELECT window_end FROM spy_api.recent_window_v1`).Scan(&end); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Window{}, err
		}
		if end == nil {
			e := s.cfg.Now().UTC().Truncate(time.Hour)
			end = &e
		}
		w := Window{Before: before, From: end.Add(-time.Duration(hours) * time.Hour), To: *end}
		if hours == 24 {
			w.Recent = true
		} else {
			w.Hours = hours
		}
		return w, nil
	}
	f, err := parseTime(from, false)
	if err != nil {
		return Window{}, bad("from: %v", err)
	}
	t, err := parseTime(to, true)
	if err != nil {
		return Window{}, bad("to: %v", err)
	}
	if to == "" {
		t = s.cfg.Now().UTC().Truncate(time.Hour)
	}
	if from == "" {
		f = t.Add(-24 * time.Hour)
	}
	if !f.Before(t) {
		return Window{}, bad("from must be before to")
	}
	if t.Sub(f) > 400*24*time.Hour {
		return Window{}, bad("a range can be 400 days at most")
	}
	return Window{Before: before, From: f, To: t}, nil
}

func parseTime(v string, end bool) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	d, err := time.ParseInLocation("2006-01-02", v, saoPaulo)
	if err != nil {
		return time.Time{}, errors.New("use 2026-10-01 or 2026-10-01T14:00:00Z")
	}
	if end {
		d = d.AddDate(0, 0, 1)
	}
	return d.UTC(), nil
}

// days are the days of the daily tables a window reads. They are UTC days:
// a window of whole São Paulo days reads the UTC days of the same dates
// (3 hours off), any other the UTC days it touches.
func (w Window) days() (time.Time, time.Time) {
	if f, t := w.From.In(saoPaulo), w.To.In(saoPaulo); midnight(f) && midnight(t) {
		t = t.AddDate(0, 0, -1)
		return time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, time.UTC),
			time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	from := w.From.UTC().Truncate(24 * time.Hour)
	to := w.To.Add(-time.Nanosecond).UTC().Truncate(24 * time.Hour)
	return from, to
}

func midnight(t time.Time) bool {
	return t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0
}

func (w Window) json() map[string]any {
	vs := "usual"
	if w.Before {
		vs = "before"
	}
	return map[string]any{"recent": w.Recent, "hours": w.Hours, "vs": vs, "from": w.From, "to": w.To}
}

// cached answers from the cache when it can; a range computed when asked
// runs with a longer statement timeout.
func (s *Server) cached(r *http.Request, w Window, f func(context.Context) (any, error)) (any, error) {
	ttl := s.cfg.RecentTTL
	if !w.stored() {
		ttl = s.cfg.RangeTTL
	}
	key := r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery
	return s.cache.get(r.Context(), key, ttl, func(ctx context.Context) (any, error) {
		if w.stored() {
			return f(ctx)
		}
		ctx, cancel := context.WithTimeout(ctx, s.cfg.RangeLimit)
		defer cancel()
		var out any
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", s.cfg.RangeLimit.Milliseconds())); err != nil {
				return err
			}
			var err error
			out, err = f(context.WithValue(ctx, txKey{}, tx))
			return err
		})
		return out, err
	})
}

type txKey struct{}

// q is the querier for ctx: the range's transaction inside cached, else the pool.
func (s *Server) q(ctx context.Context) interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
} {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return s.db
}

// rowsQ is rows through q(ctx).
func (s *Server) rowsQ(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	rs, err := s.q(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rs, pgx.RowToMap)
	if out == nil {
		out = []map[string]any{}
	}
	return out, err
}

// cache keeps answers for a while, and runs one query per key at a time:
// a second person asking for the same slow range waits for the first.
type cache struct {
	mu      sync.Mutex
	entries map[string]entry
	flying  map[string]*flight
}

type entry struct {
	v   any
	exp time.Time
}

type flight struct {
	done chan struct{}
	v    any
	err  error
}

func newCache() *cache {
	return &cache{entries: map[string]entry{}, flying: map[string]*flight{}}
}

func (c *cache) get(ctx context.Context, key string, ttl time.Duration, f func(context.Context) (any, error)) (any, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && time.Now().Before(e.exp) {
		c.mu.Unlock()
		return e.v, nil
	}
	fl, ok := c.flying[key]
	if !ok {
		fl = &flight{done: make(chan struct{})}
		c.flying[key] = fl
		c.mu.Unlock()
		go func() {
			// Detached from the first asker: if they leave, the answer is
			// still kept for the next.
			fl.v, fl.err = f(context.WithoutCancel(ctx))
			c.mu.Lock()
			delete(c.flying, key)
			if fl.err == nil {
				c.entries[key] = entry{fl.v, time.Now().Add(ttl)}
				if len(c.entries) > 2000 {
					c.sweep()
				}
			}
			c.mu.Unlock()
			close(fl.done)
		}()
	} else {
		c.mu.Unlock()
	}
	select {
	case <-fl.done:
		return fl.v, fl.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// clear forgets every kept answer: a person changed what the lists show.
func (c *cache) clear() {
	c.mu.Lock()
	c.entries = map[string]entry{}
	c.mu.Unlock()
}

// sweep drops expired entries; called with mu held.
func (c *cache) sweep() {
	now := time.Now()
	for k, e := range c.entries {
		if now.After(e.exp) {
			delete(c.entries, k)
		}
	}
}

// intParam reads a positive integer query parameter.
func intParam(r *http.Request, name string, def, max int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, bad("%s must be a whole number", name)
	}
	if max > 0 && n > max {
		n = max
	}
	return n, nil
}

func pathID(r *http.Request) (int, error) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		return 0, errNotFound
	}
	return id, nil
}

// like makes a text an ILIKE pattern that matches it anywhere, literally.
func like(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(strings.TrimSpace(q)) + "%"
}

// name adds the vertical's and category's names beside their ids.
func (s *Server) name(rows []map[string]any, keys ...string) {
	for _, r := range rows {
		for _, k := range keys {
			if id, ok := r[k].(string); ok {
				r[strings.TrimSuffix(k, "_id")+"_name"] = s.names[id]
			}
		}
	}
}

// secure sets the headers every answer carries.
func secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: data:; style-src 'self' 'unsafe-inline'; "+
			"script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		h.ServeHTTP(w, r)
	})
}
