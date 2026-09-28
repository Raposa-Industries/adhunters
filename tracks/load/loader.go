// Package load is tracks-loader: it turns archived raw files into facts,
// closes each hour into counts, and replays any range.
//
// Live loading claims pending tracks.raw_file rows oldest minute first. For
// each file, in one transaction, it removes whatever an earlier load of that
// file stored, resolves the lookups, COPYs the scrapes, sightings and
// auctions, files live links for Raposa, marks the hours it touched dirty and
// the file loaded. So loading is idempotent, and a replay is only "mark
// these files pending again".
//
// Between files the same loop closes hours whose files are all loaded (5
// minutes after they end), rebuilds their day's daily counts, rewrites the
// open hours every 5 minutes, prunes live links and, once an hour, makes
// partitions ahead and drops those past their keep time.
package load

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/tracks/archive"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
	"github.com/Raposa-Industries/adhunters/tracks/parse"
)

// Config tunes a Loader. Zero values are the design's defaults.
type Config struct {
	LiveLinkTTL      time.Duration // 15 min
	LiveLinksMax     int           // per host, 40
	KeepSightings    int           // days, 3
	KeepScrapes      int           // days, 35
	KeepAuctions     int           // days, 14
	CloseAfter       time.Duration // after an hour ends, 5 min
	OpenEvery        time.Duration // open hours rewritten, 5 min
	MaxAttempts      int           // failed loads before a file is quarantined, 3
	Idle             time.Duration // wait when nothing is pending, 2 s
	Now              func() time.Time
	DisableRetention bool
}

func (c *Config) defaults() {
	if c.LiveLinkTTL == 0 {
		c.LiveLinkTTL = 15 * time.Minute
	}
	if c.LiveLinksMax == 0 {
		c.LiveLinksMax = 40
	}
	if c.KeepSightings == 0 {
		c.KeepSightings = 3
	}
	if c.KeepScrapes == 0 {
		c.KeepScrapes = 35
	}
	if c.KeepAuctions == 0 {
		c.KeepAuctions = 14
	}
	if c.CloseAfter == 0 {
		c.CloseAfter = 5 * time.Minute
	}
	if c.OpenEvery == 0 {
		c.OpenEvery = 5 * time.Minute
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 3
	}
	if c.Idle == 0 {
		c.Idle = 2 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Loader loads raw files. One goroutine uses it.
type Loader struct {
	db                            *pgxpool.Pool
	store                         archive.Store
	log                           *slog.Logger
	cfg                           Config
	m                             *Metrics
	caches                        *caches
	parts                         map[string]bool // partitions known to exist, "table day"
	lastOpen, lastPrune, lastKeep time.Time
}

// New returns a Loader. metrics may be nil.
func New(db *pgxpool.Pool, store archive.Store, log *slog.Logger, cfg Config, metrics *Metrics) *Loader {
	cfg.defaults()
	return &Loader{db: db, store: store, log: log, cfg: cfg, m: metrics, caches: newCaches(), parts: map[string]bool{}}
}

// Metrics are the loader's live signals.
type Metrics struct {
	Files       *prometheus.CounterVec // by outcome: loaded, failed, quarantined
	Scrapes     *prometheus.CounterVec // by network and outcome; unparsed is format drift
	Sightings   *prometheus.CounterVec // by network
	Pending     prometheus.Gauge
	Quarantined prometheus.Gauge
	Lag         prometheus.Gauge
	LastClosed  prometheus.Gauge
	CloseTook   prometheus.Histogram
	lagSeconds  atomic.Int64
}

// NewMetrics registers the loader's metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Files: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tracks_loader_files_total", Help: "Raw files handled, by outcome (loaded, failed, quarantined).",
		}, []string{"outcome"}),
		Scrapes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tracks_loader_scrapes_total", Help: "Scrapes loaded, by network and outcome. unparsed means format drift.",
		}, []string{"network", "outcome"}),
		Sightings: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tracks_loader_sightings_total", Help: "Sightings loaded, by network.",
		}, []string{"network"}),
		Pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_loader_files_pending", Help: "Archived raw files not loaded yet.",
		}),
		Quarantined: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_loader_files_quarantined", Help: "Raw files set aside after failing to load.",
		}),
		Lag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_loader_lag_seconds", Help: "How far behind capture the loader is: age of the oldest pending file's minute end, 0 when none.",
		}),
		LastClosed: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_loader_last_closed_hour_timestamp_seconds", Help: "Start of the newest closed hour.",
		}),
		CloseTook: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "tracks_loader_hour_close_seconds", Help: "Time to close one hour and rebuild its day.",
			Buckets: []float64{1, 2, 5, 10, 20, 40, 80, 160},
		}),
	}
	reg.MustRegister(m.Files, m.Scrapes, m.Sightings, m.Pending, m.Quarantined, m.Lag, m.LastClosed, m.CloseTook)
	return m
}

// LagSeconds is the last measured lag, for the health check.
func (m *Metrics) LagSeconds() int64 { return m.lagSeconds.Load() }

// Run loads and closes until ctx is done. Work cut off by the stop rolls back
// and is redone at the next start.
func (l *Loader) Run(ctx context.Context) error {
	if err := CheckUTC(ctx, l.db); err != nil {
		return err
	}
	for ctx.Err() == nil {
		loaded, err := l.LoadNext(ctx)
		if err != nil && ctx.Err() == nil {
			l.log.Error("loading", "err", err)
		}
		if ctx.Err() != nil {
			break
		}
		if err := l.Maintain(ctx); err != nil && ctx.Err() == nil {
			l.log.Error("closing hours", "err", err)
		}
		if !loaded || err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(l.cfg.Idle):
			}
		}
	}
	return nil
}

type rawFile struct {
	id       int64
	key      string
	network  string
	minute   time.Time
	rows     int
	sha256   string
	attempts int
}

// errPermanent marks a failure that loading again will not fix.
type errPermanent struct{ error }

// LoadNext loads the oldest pending file. It reports whether it loaded one.
func (l *Loader) LoadNext(ctx context.Context) (bool, error) {
	var f rawFile
	err := l.db.QueryRow(ctx, `
		SELECT id, key, network, minute, rows, sha256, attempts FROM tracks.raw_file
		WHERE loaded_at IS NULL AND quarantined_at IS NULL
		ORDER BY minute, id LIMIT 1`).Scan(&f.id, &f.key, &f.network, &f.minute, &f.rows, &f.sha256, &f.attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		l.gauges(ctx)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	start := time.Now()
	w, err := l.load(ctx, f)
	if err != nil {
		if ctx.Err() != nil {
			return false, err
		}
		l.fail(ctx, f, err)
		return false, fmt.Errorf("%s: %w", f.key, err)
	}
	if l.m != nil {
		l.m.Files.WithLabelValues("loaded").Inc()
	}
	l.log.Info("raw file loaded", "key", f.key, "scrapes", w.scrapes, "sightings", w.sightings,
		"auctions", w.auctions, "live_links", w.liveLinks, "took_ms", time.Since(start).Milliseconds())
	l.gauges(ctx)
	return true, nil
}

func (l *Loader) load(ctx context.Context, f rawFile) (written, error) {
	rc, err := l.store.Get(ctx, f.key)
	if err != nil {
		if errors.Is(err, archive.ErrNotFound) {
			return written{}, errPermanent{fmt.Errorf("not in the archive %s", l.store)}
		}
		return written{}, err // the archive is down: not the file's fault
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return written{}, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != f.sha256 {
		return written{}, errPermanent{fmt.Errorf("sha256 %x does not match the recorded %s", sum, f.sha256)}
	}
	var scrapes []parse.Scrape
	skipped := 0
	err = spool.ReadRecords(bytes.NewReader(data), func(n int, rec *spool.Record) error {
		s, err := parse.Parse(rec)
		if err != nil {
			skipped++
			l.log.Warn("record skipped", "key", f.key, "line", n, "err", err)
			return nil
		}
		scrapes = append(scrapes, s)
		return nil
	})
	if err != nil {
		return written{}, errPermanent{err}
	}
	if len(scrapes)+skipped != f.rows {
		return written{}, errPermanent{fmt.Errorf("%d records, the shipper counted %d", len(scrapes)+skipped, f.rows)}
	}

	days := map[time.Time]bool{}
	for i := range scrapes {
		days[truncDay(scrapes[i].At)] = true
	}
	if err := l.checkDays(ctx, days); err != nil {
		return written{}, err
	}
	for d := range days {
		if err := l.ensureDay(ctx, d); err != nil {
			return written{}, err
		}
	}

	live := l.cfg.Now().Sub(f.minute) < time.Hour
	links := l.cfg.Now().Sub(f.minute) < l.cfg.LiveLinkTTL
	var w written
	p := newCaches()
	err = pgx.BeginFunc(ctx, l.db, func(tx pgx.Tx) error {
		var still int64
		if err := tx.QueryRow(ctx, `
			SELECT id FROM tracks.raw_file WHERE id = $1 AND loaded_at IS NULL AND quarantined_at IS NULL
			FOR UPDATE SKIP LOCKED`, f.id).Scan(&still); err != nil {
			return err // another loader has it, or it was loaded meanwhile
		}
		var err error
		if w, err = l.write(ctx, tx, f.id, scrapes, live, links, p); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE tracks.raw_file SET loaded_at = now(), scrapes = $2, last_error = NULL WHERE id = $1`,
			f.id, w.scrapes)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return written{}, nil
	}
	if err != nil {
		return written{}, err
	}
	l.caches.commit(p)
	if l.m != nil {
		for i := range scrapes {
			s := &scrapes[i]
			l.m.Scrapes.WithLabelValues(networkCode(s), s.Outcome).Inc()
			l.m.Sightings.WithLabelValues(networkCode(s)).Add(float64(len(s.Ads)))
		}
	}
	return w, nil
}

// checkDays refuses a file for a day whose sightings were already dropped:
// counting it into that day's closed hours would replace a whole hour's
// counts with one file's. Such a file waits for a replay of the day.
func (l *Loader) checkDays(ctx context.Context, days map[time.Time]bool) error {
	var list []time.Time
	for d := range days {
		list = append(list, d)
	}
	var dropped []time.Time
	rows, err := l.db.Query(ctx, `SELECT day::timestamptz FROM tracks.day_state WHERE day = ANY($1::date[]) AND sightings_dropped_at IS NOT NULL`, list)
	if err != nil {
		return err
	}
	dropped, err = pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		return err
	}
	if len(dropped) > 0 {
		return errPermanent{fmt.Errorf("a late file for %s, whose sightings were dropped: replay that day", dropped[0].Format("2006-01-02"))}
	}
	return nil
}

// fail counts a failed load; a file that keeps failing, or can never load,
// is quarantined so the loader moves on. An archive or database outage does
// not count against the file.
func (l *Loader) fail(ctx context.Context, f rawFile, cause error) {
	var perm errPermanent
	permanent := errors.As(cause, &perm)
	var pgErr interface{ SQLState() string }
	counts := permanent || errors.As(cause, &pgErr)
	if !counts {
		if l.m != nil {
			l.m.Files.WithLabelValues("failed").Inc()
		}
		return
	}
	attempts := f.attempts + 1
	quarantine := permanent || attempts >= l.cfg.MaxAttempts
	_, err := l.db.Exec(ctx, `
		UPDATE tracks.raw_file SET attempts = $2, last_error = $3,
			quarantined_at = CASE WHEN $4 THEN now() END
		WHERE id = $1`, f.id, attempts, clip(cause.Error(), 2000), quarantine)
	if err != nil {
		l.log.Error("recording a failed load", "key", f.key, "err", err)
	}
	if l.m != nil {
		if quarantine {
			l.m.Files.WithLabelValues("quarantined").Inc()
		} else {
			l.m.Files.WithLabelValues("failed").Inc()
		}
	}
	if quarantine {
		l.log.Error("raw file quarantined; fix the cause, then replay it", "key", f.key, "err", cause)
	}
}

func (l *Loader) gauges(ctx context.Context) {
	if l.m == nil {
		return
	}
	var pending, quarantined int64
	var oldest *time.Time
	if err := l.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE loaded_at IS NULL AND quarantined_at IS NULL),
		       count(*) FILTER (WHERE quarantined_at IS NOT NULL),
		       min(minute) FILTER (WHERE loaded_at IS NULL AND quarantined_at IS NULL)
		FROM tracks.raw_file WHERE loaded_at IS NULL`).Scan(&pending, &quarantined, &oldest); err != nil {
		return
	}
	l.m.Pending.Set(float64(pending))
	l.m.Quarantined.Set(float64(quarantined))
	lag := 0.0
	if oldest != nil {
		lag = max(0, l.cfg.Now().Sub(oldest.Add(time.Minute)).Seconds())
	}
	l.m.Lag.Set(lag)
	l.m.lagSeconds.Store(int64(lag))
}

func truncDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
