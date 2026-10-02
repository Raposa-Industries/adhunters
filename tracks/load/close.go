package load

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// closeLock keeps two loaders from closing, rewriting or dropping at once.
const closeLock = `SELECT pg_try_advisory_xact_lock(hashtext('tracks-loader:maintain'))`

// Maintain does the loop's work between files: close ready hours and rebuild
// their days, rewrite the open hours, prune live links, once an hour make
// partitions ahead and drop old ones, bring back archived days a page asked
// for, and write hour files.
func (l *Loader) Maintain(ctx context.Context) error {
	now := l.cfg.Now()
	if _, err := l.CloseReady(ctx); err != nil {
		return err
	}
	if now.Sub(l.lastOpen) >= l.cfg.OpenEvery {
		if err := l.RefreshOpen(ctx); err != nil {
			return err
		}
		l.lastOpen = now
	}
	if now.Sub(l.lastPrune) >= time.Minute {
		if _, err := l.db.Exec(ctx, `SELECT tracks.prune_live_links($1, $2)`, l.cfg.LiveLinkTTL, l.cfg.LiveLinksMax); err != nil {
			return fmt.Errorf("prune live links: %w", err)
		}
		l.lastPrune = now
	}
	// The archive's work never holds up closing: each step runs, and its
	// error is reported with the others.
	var errs []error
	if now.Sub(l.lastKeep) >= time.Hour {
		if err := l.Keep(ctx); err != nil {
			errs = append(errs, err)
		}
		l.lastKeep = now
	}
	if now.Sub(l.lastBringBack) >= 10*time.Second {
		if _, err := l.BringBack(ctx); err != nil {
			errs = append(errs, err)
		}
		l.lastBringBack = now
	}
	if now.Sub(l.lastHourFiles) >= l.cfg.HourFilesEvery {
		if _, err := l.WriteHourFiles(ctx); err != nil {
			errs = append(errs, err)
		}
		l.lastHourFiles = now
	}
	return errors.Join(errs...)
}

// CloseReady closes every dirty hour whose raw files are all loaded and that
// ended CloseAfter ago, oldest first, then rebuilds the days they belong to.
// It returns the hours closed.
func (l *Loader) CloseReady(ctx context.Context) ([]time.Time, error) {
	now := l.cfg.Now()
	ripe := now.Add(-l.cfg.CloseAfter - time.Hour) // an hour starting at or before this is ripe

	// Hours with no scrapes at all (capture was down) still close, with no
	// counts, so "closed" never waits on an hour that has nothing to load.
	if _, err := l.db.Exec(ctx, `
		INSERT INTO tracks.hour_state (hour)
		SELECT generate_series(
		           GREATEST((SELECT min(hour) FROM tracks.hour_state), date_trunc('hour', $1::timestamptz) - interval '7 days'),
		           date_trunc('hour', $1::timestamptz), interval '1 hour')
		WHERE EXISTS (SELECT 1 FROM tracks.hour_state)
		ON CONFLICT (hour) DO NOTHING`, ripe); err != nil {
		return nil, fmt.Errorf("add empty hours: %w", err)
	}

	rows, err := l.db.Query(ctx, `
		SELECT h.hour FROM tracks.hour_state h
		WHERE h.dirty AND h.hour <= $1
		  AND NOT EXISTS (SELECT 1 FROM tracks.raw_file f
		                  WHERE f.loaded_at IS NULL AND f.quarantined_at IS NULL
		                    AND f.minute >= h.hour AND f.minute < h.hour + interval '61 minutes')
		  AND NOT EXISTS (SELECT 1 FROM tracks.day_state d
		                  WHERE d.day = (h.hour AT TIME ZONE 'UTC')::date AND d.sightings_dropped_at IS NOT NULL)
		ORDER BY h.hour LIMIT 48`, ripe)
	if err != nil {
		return nil, err
	}
	hours, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		return nil, err
	}
	var closed []time.Time
	days := map[time.Time]bool{}
	for _, h := range hours {
		start := time.Now()
		var n int64
		var got bool
		err := pgx.BeginFunc(ctx, l.db, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, closeLock).Scan(&got); err != nil || !got {
				return err
			}
			return tx.QueryRow(ctx, `SELECT tracks.close_hour($1)`, h).Scan(&n)
		})
		if err != nil {
			return closed, fmt.Errorf("close %s: %w", h.UTC().Format(time.RFC3339), err)
		}
		if !got {
			return closed, nil // another loader is closing
		}
		closed = append(closed, h)
		days[truncDay(h)] = true
		l.log.Info("hour closed", "hour", h.UTC().Format(time.RFC3339), "sightings", n, "took_ms", time.Since(start).Milliseconds())
		if l.m != nil {
			l.m.CloseTook.Observe(time.Since(start).Seconds())
			if h.After(time.Unix(int64(0), 0)) {
				l.m.LastClosed.Set(float64(h.Unix()))
			}
		}
	}
	dayList := make([]time.Time, 0, len(days))
	for d := range days {
		dayList = append(dayList, d)
	}
	sort.Slice(dayList, func(i, j int) bool { return dayList[i].Before(dayList[j]) })
	for _, d := range dayList {
		if err := l.closeDay(ctx, d); err != nil {
			return closed, err
		}
	}
	return closed, nil
}

// closeDay rebuilds one day's daily counts up to the end of its last closed hour.
func (l *Loader) closeDay(ctx context.Context, day time.Time) error {
	start := time.Now()
	var n int64
	err := pgx.BeginFunc(ctx, l.db, func(tx pgx.Tx) error {
		var got bool
		if err := tx.QueryRow(ctx, closeLock).Scan(&got); err != nil || !got {
			return err
		}
		var upto *time.Time
		if err := tx.QueryRow(ctx, `
			SELECT max(hour) + interval '1 hour' FROM tracks.hour_state
			WHERE closed_at IS NOT NULL AND hour >= $1::timestamptz AND hour < $1::timestamptz + interval '1 day'`, day).Scan(&upto); err != nil || upto == nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT tracks.close_day($1::date, $2)`, day, *upto).Scan(&n)
	})
	if err != nil {
		return fmt.Errorf("close day %s: %w", day.Format("2006-01-02"), err)
	}
	l.log.Info("day counts rebuilt", "day", day.Format("2006-01-02"), "rows", n, "took_ms", time.Since(start).Milliseconds())
	return nil
}

// RefreshOpen rewrites the counts of the hours not closed yet.
func (l *Loader) RefreshOpen(ctx context.Context) error {
	return pgx.BeginFunc(ctx, l.db, func(tx pgx.Tx) error {
		var got bool
		if err := tx.QueryRow(ctx, closeLock).Scan(&got); err != nil || !got {
			return err
		}
		_, err := tx.Exec(ctx, `SELECT tracks.refresh_open_hours($1)`, l.cfg.Now().Add(-3*time.Hour))
		return err
	})
}

// ensureDay makes the daily partitions of the fact tables for day.
func (l *Loader) ensureDay(ctx context.Context, day time.Time) error {
	for _, t := range []string{"scrape", "sighting", "auction"} {
		k := t + " " + day.Format("20060102")
		if l.parts[k] {
			continue
		}
		if _, err := l.db.Exec(ctx, `SELECT tracks.ensure_day_partitions($1, $2::date, $2::date)`, t, day); err != nil {
			return fmt.Errorf("partition %s: %w", k, err)
		}
		l.parts[k] = true
	}
	return nil
}

var partRe = regexp.MustCompile(`^(scrape|sighting|auction)_(\d{8})$`)

// Keep makes partitions for the next days and months, and drops the daily
// partitions past their keep time: sightings after KeepSightings days,
// scrapes after KeepScrapes, auctions after KeepAuctions. A day goes only
// once every hour of it is closed and no file for it waits to load, so its
// counts are final; anything dropped can be rebuilt from the raw archive by
// a replay (decision 0007).
func (l *Loader) Keep(ctx context.Context) error {
	today := truncDay(l.cfg.Now())
	for d := today.AddDate(0, 0, -1); !d.After(today.AddDate(0, 0, 2)); d = d.AddDate(0, 0, 1) {
		if err := l.ensureDay(ctx, d); err != nil {
			return err
		}
	}
	if _, err := l.db.Exec(ctx, `SELECT tracks.ensure_month_partitions($1::date, $2::date)`, today, today.AddDate(0, 1, 0)); err != nil {
		return fmt.Errorf("month partitions: %w", err)
	}
	if l.cfg.DisableRetention {
		return nil
	}
	if err := l.KeepArchived(ctx); err != nil {
		return err
	}
	keep := map[string]int{"scrape": l.cfg.KeepScrapes, "sighting": l.cfg.KeepSightings, "auction": l.cfg.KeepAuctions}

	// Every table in the schema named like a daily partition: attached,
	// detaching, or detached by a run that stopped before dropping it.
	rows, err := l.db.Query(ctx, `
		SELECT c.relname, i.inhdetachpending
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_inherits i ON i.inhrelid = c.oid
		WHERE n.nspname = 'tracks' AND c.relkind = 'r' AND c.relname ~ '^(scrape|sighting|auction)_[0-9]{8}$'
		ORDER BY c.relname`)
	if err != nil {
		return err
	}
	type part struct {
		name     string
		detached bool // not attached at all
		pending  bool
	}
	var parts []part
	for rows.Next() {
		var p part
		var pending *bool
		if err := rows.Scan(&p.name, &pending); err != nil {
			rows.Close()
			return err
		}
		p.detached = pending == nil
		p.pending = pending != nil && *pending
		parts = append(parts, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range parts {
		m := partRe.FindStringSubmatch(p.name)
		day, err := time.Parse("20060102", m[2])
		if err != nil {
			continue
		}
		table := m[1]
		if !day.Before(today.AddDate(0, 0, -keep[table])) {
			continue
		}
		final, err := l.dayIsFinal(ctx, day)
		if err != nil {
			return err
		}
		if !final {
			l.log.Warn("partition past its keep time kept: the day is not final", "partition", p.name)
			continue
		}
		if err := l.dropPartition(ctx, table, p.name, p.detached, p.pending); err != nil {
			return err
		}
		if table == "sighting" {
			if _, err := l.db.Exec(ctx, `
				INSERT INTO tracks.day_state (day, sightings_dropped_at) VALUES ($1::date, now())
				ON CONFLICT (day) DO UPDATE SET sightings_dropped_at = now()`, day); err != nil {
				return err
			}
		}
		delete(l.parts, table+" "+m[2])
		l.log.Info("partition dropped after its keep time", "partition", p.name)
	}
	return nil
}

// dayIsFinal reports whether every hour of day is closed and clean and no raw
// file that can hold its scrapes waits to load.
func (l *Loader) dayIsFinal(ctx context.Context, day time.Time) (bool, error) {
	var open bool
	err := l.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM tracks.hour_state
		               WHERE hour >= $1::timestamptz AND hour < $1::timestamptz + interval '1 day' AND (dirty OR closed_at IS NULL))
		    OR EXISTS (SELECT 1 FROM tracks.raw_file
		               WHERE loaded_at IS NULL AND minute >= $1::timestamptz AND minute < $1::timestamptz + interval '1 day 1 minute')`, day).Scan(&open)
	return !open, err
}

func (l *Loader) dropPartition(ctx context.Context, table, name string, detached, pending bool) error {
	ident := pgx.Identifier{"tracks", name}.Sanitize()
	parent := pgx.Identifier{"tracks", table}.Sanitize()
	conn, err := l.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET lock_timeout = '3s'`); err != nil {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `RESET lock_timeout`)
	switch {
	case pending:
		if _, err := conn.Exec(ctx, `ALTER TABLE `+parent+` DETACH PARTITION `+ident+` FINALIZE`); err != nil {
			return fmt.Errorf("finish detaching %s: %w", name, err)
		}
	case !detached:
		// CONCURRENTLY: readers and the loader's COPY never wait on it.
		if _, err := conn.Exec(ctx, `ALTER TABLE `+parent+` DETACH PARTITION `+ident+` CONCURRENTLY`); err != nil {
			return fmt.Errorf("detach %s: %w", name, err)
		}
	}
	if _, err := conn.Exec(ctx, `DROP TABLE `+ident); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	return nil
}
