// Package bridge is tracks-bridge: it writes Tracks' scrapes into the
// collector's database, so today's Spy (app.adhunters.pro) and every job the
// collector still runs keep reading fresh data there after the collector
// stops scraping. It is temporary: it goes when the new Spy replaces the old
// one. See platform/SWITCH-OVER.md and decision 0012.
//
// It reads the raw files the shipper archived, from its starting minute on,
// parses them with the loader's parsers, and writes each scrape through the
// collector's own writer (writer.go). The collector keyed every scrape by its
// batch id; the bridge uses the capture record's id, so writing a file again
// stores nothing twice. tracks.bridge_file records what was written.
package bridge

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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/tracks/archive"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
	"github.com/Raposa-Industries/adhunters/tracks/parse"
)

// Config tunes a Bridge. Zero values are the defaults.
type Config struct {
	From        time.Time     // the first minute written; files before it are the collector's own
	MaxAttempts int           // failed writes before a file is quarantined, 3
	Idle        time.Duration // wait when nothing is pending, 2 s
	Rescan      time.Duration // how often the start of the pending files is found again, 5 min
	Version     string        // stored as the scrapes' pipeline_version
	Now         func() time.Time
}

func (c *Config) defaults() {
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 3
	}
	if c.Idle == 0 {
		c.Idle = 2 * time.Second
	}
	if c.Rescan == 0 {
		c.Rescan = 5 * time.Minute
	}
	if c.Version == "" {
		c.Version = "dev"
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Bridge writes raw files into the collector's database. One goroutine uses it.
type Bridge struct {
	db     *pgxpool.Pool // Tracks
	old    *pgxpool.Pool // the collector's database
	store  archive.Store
	w      *SpyWriter
	log    *slog.Logger
	cfg    Config
	m      *Metrics
	lowID  int64 // every raw file with a smaller id is written, quarantined or before From
	rescan time.Time
}

// New returns a Bridge. metrics may be nil.
func New(db, old *pgxpool.Pool, store archive.Store, log *slog.Logger, cfg Config, metrics *Metrics) *Bridge {
	cfg.defaults()
	return &Bridge{db: db, old: old, store: store, w: NewSpyWriter(old), log: log, cfg: cfg, m: metrics}
}

// Metrics are the bridge's live signals.
type Metrics struct {
	Files       *prometheus.CounterVec // by outcome: written, failed, quarantined
	Scrapes     prometheus.Counter
	Sightings   prometheus.Counter
	Pending     prometheus.Gauge
	Quarantined prometheus.Gauge
	Lag         prometheus.Gauge
	lagSeconds  atomic.Int64
}

// NewMetrics registers the bridge's metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Files: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tracks_bridge_files_total", Help: "Raw files handled, by outcome (written, failed, quarantined).",
		}, []string{"outcome"}),
		Scrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tracks_bridge_scrapes_total", Help: "Scrapes written into the collector's database (skipped repeats included).",
		}),
		Sightings: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tracks_bridge_sightings_total", Help: "Sightings in the scrapes written.",
		}),
		Pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_bridge_files_pending", Help: "Archived raw files from the starting minute on not written yet.",
		}),
		Quarantined: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_bridge_files_quarantined", Help: "Raw files set aside after failing to be written.",
		}),
		Lag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_bridge_lag_seconds", Help: "How far behind capture the bridge is: age of the oldest pending file's minute end, 0 when none.",
		}),
	}
	reg.MustRegister(m.Files, m.Scrapes, m.Sightings, m.Pending, m.Quarantined, m.Lag)
	return m
}

// LagSeconds is the last measured lag, for the health check.
func (m *Metrics) LagSeconds() int64 { return m.lagSeconds.Load() }

// Run writes files until ctx is done. A file cut off by the stop rolls back
// its open batch; the next start writes it again, skipping what was stored.
func (b *Bridge) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		did, err := b.Next(ctx)
		if err != nil && ctx.Err() == nil {
			b.log.Error("bridging", "err", err)
		}
		if !did || err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(b.cfg.Idle):
			}
		}
	}
	return nil
}

type rawFile struct {
	id       int64
	key      string
	minute   time.Time
	rows     int
	sha256   string
	attempts int
}

// errPermanent marks a failure that writing again will not fix.
type errPermanent struct{ error }

// Next writes the oldest pending file. It reports whether it wrote one.
func (b *Bridge) Next(ctx context.Context) (bool, error) {
	if b.cfg.Now().After(b.rescan) {
		if err := b.findLow(ctx); err != nil {
			return false, err
		}
	}
	var f rawFile
	err := b.db.QueryRow(ctx, `
		SELECT r.id, r.key, r.minute, r.rows, r.sha256, COALESCE(f.attempts, 0)
		FROM tracks.raw_file r LEFT JOIN tracks.bridge_file f ON f.raw_file_id = r.id
		WHERE r.id > $1 AND r.minute >= $2 AND f.bridged_at IS NULL AND f.quarantined_at IS NULL
		ORDER BY r.minute, r.id LIMIT 1`, b.lowID, b.cfg.From).
		Scan(&f.id, &f.key, &f.minute, &f.rows, &f.sha256, &f.attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		b.gauges(ctx)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	start := time.Now()
	scrapes, sightings, err := b.write(ctx, f)
	if err != nil {
		if ctx.Err() != nil {
			return false, err
		}
		b.fail(ctx, f, err)
		return false, fmt.Errorf("%s: %w", f.key, err)
	}
	if b.m != nil {
		b.m.Files.WithLabelValues("written").Inc()
		b.m.Scrapes.Add(float64(scrapes))
		b.m.Sightings.Add(float64(sightings))
	}
	b.log.Info("raw file bridged", "key", f.key, "scrapes", scrapes, "sightings", sightings,
		"took_ms", time.Since(start).Milliseconds())
	b.gauges(ctx)
	return true, nil
}

// findLow moves lowID up to just before the first file still to do, so the
// pending query reads only the newest part of tracks.raw_file.
func (b *Bridge) findLow(ctx context.Context) error {
	var first *int64
	if err := b.db.QueryRow(ctx, `
		SELECT min(r.id) FROM tracks.raw_file r LEFT JOIN tracks.bridge_file f ON f.raw_file_id = r.id
		WHERE r.id > $1 AND r.minute >= $2 AND f.bridged_at IS NULL AND f.quarantined_at IS NULL`,
		b.lowID, b.cfg.From).Scan(&first); err != nil {
		return err
	}
	if first != nil {
		b.lowID = *first - 1
	} else if err := b.db.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM tracks.raw_file`).Scan(&b.lowID); err != nil {
		return err
	}
	b.rescan = b.cfg.Now().Add(b.cfg.Rescan)
	return nil
}

// write writes one raw file and records it. It returns the scrapes and
// sightings it handed the writer.
func (b *Bridge) write(ctx context.Context, f rawFile) (int, int, error) {
	rc, err := b.store.Get(ctx, f.key)
	if err != nil {
		if errors.Is(err, archive.ErrNotFound) {
			return 0, 0, errPermanent{fmt.Errorf("not in the archive %s", b.store)}
		}
		return 0, 0, err // the archive is down: not the file's fault
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return 0, 0, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != f.sha256 {
		return 0, 0, errPermanent{fmt.Errorf("sha256 %x does not match the recorded %s", sum, f.sha256)}
	}
	var scrapes []Scrape
	sightings := 0
	err = spool.ReadRecords(bytes.NewReader(data), func(n int, rec *spool.Record) error {
		s, err := parse.Parse(rec)
		if err != nil {
			b.log.Warn("record skipped", "key", f.key, "line", n, "err", err)
			return nil
		}
		if old, ok := Convert(s, b.cfg.Version); ok {
			scrapes = append(scrapes, old)
			sightings += len(old.Ads)
		}
		return nil
	})
	if err != nil {
		return 0, 0, errPermanent{err}
	}
	if _, err := b.w.Write(ctx, scrapes); err != nil {
		return 0, 0, err
	}
	_, err = b.db.Exec(ctx, `
		INSERT INTO tracks.bridge_file AS f (raw_file_id, bridged_at, scrapes, sightings, attempts)
		VALUES ($1, now(), $2, $3, $4)
		ON CONFLICT (raw_file_id) DO UPDATE SET bridged_at = now(), scrapes = $2, sightings = $3,
			attempts = $4, last_error = NULL`, f.id, len(scrapes), sightings, f.attempts)
	return len(scrapes), sightings, err
}

// Convert turns one parsed scrape into the collector's. Only answered scrapes
// the parser read (outcome ok, possibly without ads) are kept: the collector
// never stored a scrape that failed, came back empty or did not parse.
func Convert(s parse.Scrape, version string) (Scrape, bool) {
	if s.Outcome != parse.OutcomeOK {
		return Scrape{}, false
	}
	return Scrape{
		BatchID:         BatchUID(s.CaptureID),
		Network:         s.Network,
		ScrapedAt:       s.At,
		Publisher:       s.Publisher,
		Device:          s.Device,
		ProxyLine:       s.Line,
		LatencyMs:       s.LatencyMS,
		GeoCountry:      s.GeoCountry,
		TrcRoute:        s.TrcRoute,
		WorkerNode:      "tracks:" + s.Instance,
		PipelineVersion: "tracks-bridge " + version,
		PayloadChecksum: s.BodySHA256,
		Referer:         s.PageURL,
		Ads:             s.Ads,
	}, true
}

// BatchUID is the collector's batch id for a capture record: the ULID's 16
// bytes as a UUID, so the same record always gets the same id.
func BatchUID(captureID string) uuid.UUID {
	if id, err := ulid.ParseStrict(captureID); err == nil {
		return uuid.UUID(id)
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("tracks-capture:"+captureID))
}

// fail counts a failed write; a file that keeps failing, or can never be
// written, is quarantined so the bridge moves on. An archive or database
// outage does not count against the file.
func (b *Bridge) fail(ctx context.Context, f rawFile, cause error) {
	var perm errPermanent
	permanent := errors.As(cause, &perm)
	var pgErr interface{ SQLState() string }
	counts := permanent || errors.As(cause, &pgErr)
	if !counts {
		if b.m != nil {
			b.m.Files.WithLabelValues("failed").Inc()
		}
		return
	}
	attempts := f.attempts + 1
	quarantine := permanent || attempts >= b.cfg.MaxAttempts
	_, err := b.db.Exec(ctx, `
		INSERT INTO tracks.bridge_file AS f (raw_file_id, attempts, last_error, quarantined_at)
		VALUES ($1, $2, $3, CASE WHEN $4 THEN now() END)
		ON CONFLICT (raw_file_id) DO UPDATE SET attempts = $2, last_error = $3,
			quarantined_at = CASE WHEN $4 THEN now() END`, f.id, attempts, clip(cause.Error(), 2000), quarantine)
	if err != nil {
		b.log.Error("recording a failed write", "key", f.key, "err", err)
	}
	if b.m != nil {
		if quarantine {
			b.m.Files.WithLabelValues("quarantined").Inc()
		} else {
			b.m.Files.WithLabelValues("failed").Inc()
		}
	}
	if quarantine {
		b.log.Error("raw file quarantined by the bridge; fix the cause, then: tracks-bridge redo", "key", f.key, "err", cause)
	}
}

func (b *Bridge) gauges(ctx context.Context) {
	if b.m == nil {
		return
	}
	s, err := ReadStatus(ctx, b.db, b.cfg.From, b.lowID)
	if err != nil {
		return
	}
	b.m.Pending.Set(float64(s.Pending))
	b.m.Quarantined.Set(float64(s.Quarantined))
	lag := 0.0
	if s.OldestPending != nil {
		lag = max(0, b.cfg.Now().Sub(s.OldestPending.Add(time.Minute)).Seconds())
	}
	b.m.Lag.Set(lag)
	b.m.lagSeconds.Store(int64(lag))
}

// Status is the bridge's state, for people and for its gauges.
type Status struct {
	From                 time.Time
	Pending, Quarantined int64
	OldestPending        *time.Time
	LastBridgedMinute    *time.Time
}

// ReadStatus reads what is left to write from minute from on, among raw
// files with an id above lowID (0 reads them all).
func ReadStatus(ctx context.Context, db *pgxpool.Pool, from time.Time, lowID int64) (Status, error) {
	s := Status{From: from}
	err := db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE f.bridged_at IS NULL AND f.quarantined_at IS NULL),
		       count(*) FILTER (WHERE f.quarantined_at IS NOT NULL),
		       min(r.minute) FILTER (WHERE f.bridged_at IS NULL AND f.quarantined_at IS NULL),
		       max(r.minute) FILTER (WHERE f.bridged_at IS NOT NULL)
		FROM tracks.raw_file r LEFT JOIN tracks.bridge_file f ON f.raw_file_id = r.id
		WHERE r.id > $1 AND r.minute >= $2`, lowID, from).
		Scan(&s.Pending, &s.Quarantined, &s.OldestPending, &s.LastBridgedMinute)
	return s, err
}

// Redo forgets what was written from minutes [from, to), so the running
// bridge writes those files again within its rescan time (5 minutes).
// Scrapes already in the collector's database are skipped, so this only
// fills what is missing (after its database was restored, for example).
func Redo(ctx context.Context, db *pgxpool.Pool, from, to time.Time) (int64, error) {
	if !from.Before(to) {
		return 0, fmt.Errorf("redo: from %s is not before to %s", from, to)
	}
	tag, err := db.Exec(ctx, `
		DELETE FROM tracks.bridge_file WHERE raw_file_id IN (
			SELECT id FROM tracks.raw_file WHERE minute >= $1 AND minute < $2)`, from, to)
	return tag.RowsAffected(), err
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
