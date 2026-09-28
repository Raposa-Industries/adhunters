// Package ship moves sealed raw files from capture's spool to the archive
// and records each one in tracks.raw_file, where the loader finds it.
//
// Every pass (10 s by default) takes the sealed files not shipped yet, oldest
// minute first. For each: upload, verify size and MD5 (the archive checks
// Content-MD5 and reads the object back), insert its raw_file row, then drop
// a "<file>.shipped" marker beside it. A crash anywhere in between redoes the
// file on the next pass, which is safe: the archive keeps one copy per key
// and the row is inserted once. If the database or the archive is down the
// pass fails, nothing is lost, and the spool simply grows until they return.
// Local files are deleted 48 hours after their marker.
package ship

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Raposa-Industries/adhunters/tracks/archive"
	"github.com/Raposa-Industries/adhunters/tracks/capture/spool"
)

// Marker is the suffix of the empty file that says a raw file was shipped.
const Marker = ".shipped"

// Config is one shipper.
type Config struct {
	Spool   string
	Store   archive.Store
	DB      *pgxpool.Pool
	Box     string        // this box's name, recorded as shipped_by
	Every   time.Duration // between passes
	KeepFor time.Duration // how long a shipped file stays in the spool
	Log     *slog.Logger
	Metrics *Metrics
	Now     func() time.Time
}

// Metrics are the shipper's live signals.
type Metrics struct {
	Files          *prometheus.CounterVec // by outcome
	Bytes          prometheus.Counter
	OldestUnsent   prometheus.Gauge
	Waiting        prometheus.Gauge
	LastPass       prometheus.Gauge
	lastPassUnix   atomic.Int64
	oldestUnsentAt atomic.Int64
}

// NewMetrics registers the shipper's metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Files: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tracks_shipper_files_total", Help: "Raw files handled, by outcome (shipped, failed, deleted).",
		}, []string{"outcome"}),
		Bytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tracks_shipper_bytes_total", Help: "Compressed bytes uploaded to the archive.",
		}),
		OldestUnsent: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_shipper_oldest_unshipped_seconds", Help: "Age of the oldest sealed raw file not archived yet (0 when none).",
		}),
		Waiting: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_shipper_files_waiting", Help: "Sealed raw files not archived yet.",
		}),
		LastPass: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tracks_shipper_last_pass_timestamp_seconds", Help: "When a pass last finished without errors.",
		}),
	}
	reg.MustRegister(m.Files, m.Bytes, m.OldestUnsent, m.Waiting, m.LastPass)
	return m
}

// Healthy fails when a pass has not succeeded for maxAge, or a sealed file
// has waited longer than maxAge.
func (m *Metrics) Healthy(started time.Time, maxAge time.Duration) error {
	now := time.Now()
	last := time.Unix(m.lastPassUnix.Load(), 0)
	if now.Sub(started) > maxAge && now.Sub(last) > maxAge {
		return fmt.Errorf("no successful pass for %s", now.Sub(last).Round(time.Second))
	}
	if at := m.oldestUnsentAt.Load(); at > 0 && now.Sub(time.Unix(at, 0)) > maxAge {
		return fmt.Errorf("a sealed raw file has waited %s", now.Sub(time.Unix(at, 0)).Round(time.Second))
	}
	return nil
}

// Run passes until ctx is done. A pass in flight when ctx ends finishes its
// current file first, so a stop never leaves a half-recorded file (and the
// next start would redo it anyway).
func Run(ctx context.Context, c Config) error {
	if c.Every <= 0 {
		c.Every = 10 * time.Second
	}
	for {
		if _, err := Pass(context.WithoutCancel(ctx), ctx, c); err != nil {
			c.Log.Error("shipping pass", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(c.Every):
		}
	}
}

type sealed struct {
	path string
	key  string
	k    spool.Key
}

// Pass ships every sealed file waiting and deletes old shipped ones. work is
// the context for each file's work; stop ends the pass between files.
func Pass(work, stop context.Context, c Config) (int, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	waiting, err := c.scan(now())
	if err != nil {
		return 0, err
	}
	c.gauge(waiting, now())
	shipped := 0
	var firstErr error
	for i, f := range waiting {
		if stop.Err() != nil {
			break
		}
		fctx, cancel := context.WithTimeout(work, 20*time.Second)
		err := c.ship(fctx, f)
		cancel()
		if err != nil {
			if c.Metrics != nil {
				c.Metrics.Files.WithLabelValues("failed").Inc()
			}
			c.Log.Error("shipping raw file", "key", f.key, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			// The database or the archive is down: the rest would fail too.
			break
		}
		shipped++
		c.gauge(waiting[i+1:], now())
	}
	if firstErr == nil && c.Metrics != nil {
		c.Metrics.LastPass.SetToCurrentTime()
		c.Metrics.lastPassUnix.Store(time.Now().Unix())
	}
	return shipped, firstErr
}

func (c Config) gauge(waiting []sealed, now time.Time) {
	if c.Metrics == nil {
		return
	}
	c.Metrics.Waiting.Set(float64(len(waiting)))
	if len(waiting) == 0 {
		c.Metrics.OldestUnsent.Set(0)
		c.Metrics.oldestUnsentAt.Store(0)
		return
	}
	// A file is sealed once its minute ends.
	sealedAt := waiting[0].k.Minute.Add(time.Minute)
	c.Metrics.OldestUnsent.Set(max(0, now.Sub(sealedAt).Seconds()))
	c.Metrics.oldestUnsentAt.Store(sealedAt.Unix())
}

// scan lists sealed files without a marker, oldest minute first, and deletes
// shipped files older than KeepFor.
func (c Config) scan(now time.Time) ([]sealed, error) {
	var out []sealed
	err := filepath.WalkDir(c.Spool, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch {
		case strings.HasSuffix(p, ".ndjson.zst"+Marker):
			info, err := d.Info()
			if err != nil {
				return err
			}
			if c.KeepFor > 0 && now.Sub(info.ModTime()) > c.KeepFor {
				data := strings.TrimSuffix(p, Marker)
				if err := os.Remove(data); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				if err := os.Remove(p); err != nil {
					return err
				}
				if c.Metrics != nil {
					c.Metrics.Files.WithLabelValues("deleted").Inc()
				}
			}
		case strings.HasSuffix(p, ".ndjson.zst"):
			if _, err := os.Stat(p + Marker); err == nil {
				return nil
			}
			rel, err := filepath.Rel(c.Spool, p)
			if err != nil {
				return err
			}
			key := filepath.ToSlash(rel)
			k, err := spool.ParseKey(key)
			if err != nil {
				c.Log.Warn("not a raw file; left alone", "file", p, "err", err)
				return nil
			}
			out = append(out, sealed{path: p, key: key, k: k})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if !out[i].k.Minute.Equal(out[j].k.Minute) {
			return out[i].k.Minute.Before(out[j].k.Minute)
		}
		return out[i].key < out[j].key
	})
	return out, err
}

func (c Config) ship(ctx context.Context, f sealed) error {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return err
	}
	rows := 0
	if err := spool.ReadRecords(bytes.NewReader(data), func(int, *spool.Record) error { rows++; return nil }); err != nil {
		// A sealed file that does not read back is kept, never archived, and
		// alerted on: the loader could not use it either.
		return fmt.Errorf("%s does not read back: %w", f.key, err)
	}
	m := md5.Sum(data)
	s := sha256.Sum256(data)
	md5hex, shahex := hex.EncodeToString(m[:]), hex.EncodeToString(s[:])

	if err := c.Store.Put(ctx, f.key, bytes.NewReader(data), int64(len(data)), md5hex); err != nil {
		return err
	}

	var stored string
	err = c.DB.QueryRow(ctx, `
		INSERT INTO tracks.raw_file (key, network, instance, minute, rows, bytes, sha256, shipped_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (key) DO UPDATE SET key = EXCLUDED.key
		RETURNING sha256`,
		f.key, f.k.Network, f.k.Instance, f.k.Minute, rows, len(data), shahex, c.Box).Scan(&stored)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("record %s: %w", f.key, err)
	}
	if stored != shahex {
		return fmt.Errorf("record %s: already recorded with sha256 %s, this file has %s", f.key, stored, shahex)
	}

	if err := os.WriteFile(f.path+Marker, nil, 0o640); err != nil {
		return err
	}
	if c.Metrics != nil {
		c.Metrics.Files.WithLabelValues("shipped").Inc()
		c.Metrics.Bytes.Add(float64(len(data)))
	}
	c.Log.Debug("raw file shipped", "key", f.key, "rows", rows, "bytes", len(data))
	return nil
}
