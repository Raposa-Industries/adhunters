// Package numbers runs Spy's derived numbers on a schedule: the last 24
// hours, the read model, and Size and Direction. The numbers themselves are
// SQL functions in the spy schema (migrations/sql); this package decides when
// each runs, times it and reports it.
//
// Every minute it asks for the last 24 hours, which rebuild only when Tracks
// closed a new hour (or closed one again). Every 5 minutes it rebuilds the
// read model, then Direction, which reads the read model's junk flags. A job
// that fails is logged and counted; the next tick tries again.
package numbers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Jobs, in the order a 5-minute tick runs them.
const (
	JobRecent    = "recent"
	JobReadModel = "read_model"
	JobDirection = "direction"
)

// Config says how often each part runs. Zero values take the defaults.
type Config struct {
	Tick      time.Duration    // how often the last 24 hours are checked (1 minute)
	Every     int              // the read model and Direction run every this many ticks (5)
	Now       func() time.Time // the clock, for tests
	StaleWarn time.Duration    // health fails when a job has not succeeded for this long (20 minutes)
}

// Runner runs the jobs.
type Runner struct {
	db  *pgxpool.Pool
	log *slog.Logger
	cfg Config

	runs    *prometheus.CounterVec
	seconds *prometheus.HistogramVec
	rows    *prometheus.GaugeVec
	last    *prometheus.GaugeVec
	winEnd  prometheus.Gauge

	mu      sync.Mutex
	started time.Time
	ok      map[string]time.Time
}

// New makes a Runner. reg may be nil.
func New(db *pgxpool.Pool, log *slog.Logger, cfg Config, reg prometheus.Registerer) *Runner {
	if cfg.Tick <= 0 {
		cfg.Tick = time.Minute
	}
	if cfg.Every <= 0 {
		cfg.Every = 5
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.StaleWarn <= 0 {
		cfg.StaleWarn = 20 * time.Minute
	}
	r := &Runner{
		db: db, log: log, cfg: cfg, ok: map[string]time.Time{}, started: time.Now(),
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "spy_numbers_runs_total",
			Help: "Runs of each numbers job, by outcome: done, current (nothing to do), busy (another run holds it), failed.",
		}, []string{"job", "outcome"}),
		seconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "spy_numbers_seconds",
			Help:    "How long each numbers job took.",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 20, 40, 80, 160, 320, 600},
		}, []string{"job"}),
		rows: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "spy_numbers_rows",
			Help: "Rows the job's last run wrote.",
		}, []string{"job"}),
		last: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "spy_numbers_last_success_timestamp_seconds",
			Help: "When each job last succeeded.",
		}, []string{"job"}),
		winEnd: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "spy_recent_window_end_timestamp_seconds",
			Help: "Where the last 24 hours end: Tracks' last closed hour, plus one hour.",
		}),
	}
	if reg != nil {
		reg.MustRegister(r.runs, r.seconds, r.rows, r.last, r.winEnd)
	}
	return r
}

// Recent rebuilds the last 24 hours if Tracks closed a new hour.
func (r *Runner) Recent(ctx context.Context) (int64, error) {
	return r.job(ctx, JobRecent, `SELECT spy.refresh_recent($1)`, r.cfg.Now())
}

// ReadModel rebuilds creative_stats, operator_stats and publisher_stats.
func (r *Runner) ReadModel(ctx context.Context) (int64, error) {
	return r.job(ctx, JobReadModel, `SELECT spy.refresh_read_model($1)`, r.cfg.Now())
}

// Direction judges every subject now; rebuild redoes the daily part first.
func (r *Runner) Direction(ctx context.Context, rebuild bool) (int64, error) {
	return r.job(ctx, JobDirection, `SELECT spy.refresh_direction($1, $2)`, r.cfg.Now(), rebuild)
}

// All runs the three once, in order, and returns the first error.
func (r *Runner) All(ctx context.Context, rebuild bool) error {
	var errs []error
	if _, err := r.Recent(ctx); err != nil {
		errs = append(errs, err)
	}
	if _, err := r.ReadModel(ctx); err != nil {
		errs = append(errs, err)
	}
	if _, err := r.Direction(ctx, rebuild); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (r *Runner) job(ctx context.Context, name, query string, args ...any) (int64, error) {
	start := time.Now()
	var n int64
	err := r.db.QueryRow(ctx, query, args...).Scan(&n)
	took := time.Since(start)
	r.seconds.WithLabelValues(name).Observe(took.Seconds())
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		r.runs.WithLabelValues(name, "failed").Inc()
		r.log.Error("numbers job failed", "job", name, "took_ms", took.Milliseconds(), "err", err)
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	outcome := "done"
	if n == 0 {
		// The functions return 0 when there was nothing to do or another run
		// holds the lock; both count as success.
		outcome = "current"
	}
	r.runs.WithLabelValues(name, outcome).Inc()
	r.rows.WithLabelValues(name).Set(float64(n))
	r.last.WithLabelValues(name).SetToCurrentTime()
	r.mu.Lock()
	r.ok[name] = time.Now()
	r.mu.Unlock()
	if n > 0 || name != JobRecent {
		r.log.Info("numbers job done", "job", name, "rows", n, "took_ms", took.Milliseconds())
	}
	if name == JobRecent {
		var end *time.Time
		if err := r.db.QueryRow(ctx, `SELECT window_end FROM spy.recent_window`).Scan(&end); err == nil && end != nil {
			r.winEnd.Set(float64(end.Unix()))
		}
	}
	return n, nil
}

// Run ticks until ctx ends. The first tick runs at once.
func (r *Runner) Run(ctx context.Context) error {
	t := time.NewTicker(r.cfg.Tick)
	defer t.Stop()
	for tick := 0; ; tick++ {
		_, _ = r.Recent(ctx)
		if tick%r.cfg.Every == 0 {
			_, _ = r.ReadModel(ctx)
			_, _ = r.Direction(ctx, false)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Healthy fails when the read model or Direction has not succeeded for the
// StaleWarn time (counted from the start before the first success).
func (r *Runner) Healthy(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, job := range []string{JobReadModel, JobDirection} {
		last, ok := r.ok[job]
		if !ok {
			last = r.started
		}
		if since := time.Since(last); since > r.cfg.StaleWarn {
			return fmt.Errorf("%s has not succeeded for %s", job, since.Round(time.Second))
		}
	}
	return nil
}
