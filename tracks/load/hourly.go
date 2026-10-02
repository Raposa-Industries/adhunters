package load

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Raposa-Industries/adhunters/shared/archive"
)

// Hourly counts move to the archive after 35 days (decision 0023):
//
//   - WriteHourFiles writes each final day older than HourFilesAfter days to
//     one hour file per hourly table, reads the archive's copy back and
//     checks it hour by hour before recording it. It only adds files.
//   - DropMonth (tracks-loader hourly drop, run by a person) checks a whole
//     month again, every hour of every day against its hour files, and only
//     then drops the month's hourly partitions. Daily counts stay.
//   - BringBack loads an archived day a page asked for back into the hourly
//     tables, and KeepArchived drops it again once nobody keeps it, after
//     the same check.

const (
	maintainLock   = `SELECT pg_try_advisory_lock(hashtext('tracks-loader:maintain'))`
	maintainUnlock = `SELECT pg_advisory_unlock(hashtext('tracks-loader:maintain'))`
)

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// errBusy means another loader held the maintain lock; try again later.
var errBusy = errors.New("another loader is maintaining")

// WriteHourFiles writes the hour files of the oldest day that needs them: a
// final day older than HourFilesAfter days whose hourly counts have no hour
// file yet, or were closed again since theirs was written. It reports
// whether it wrote one.
func (l *Loader) WriteHourFiles(ctx context.Context) (bool, error) {
	before := truncDay(l.cfg.Now()).AddDate(0, 0, -l.cfg.HourFilesAfter)
	rows, err := l.db.Query(ctx, `
		WITH d AS (
			SELECT (hour AT TIME ZONE 'UTC')::date AS day, max(GREATEST(closed_at, imported_at)) AS as_of,
			       bool_or(dirty OR (closed_at IS NULL AND imported_at IS NULL)) AS open
			FROM tracks.hour_state WHERE hour < $1 GROUP BY 1
		)
		SELECT d.day, d.open FROM d
		LEFT JOIN tracks.archived_month m ON m.month = date_trunc('month', d.day::timestamp)::date
		WHERE d.as_of IS NOT NULL AND (m.month IS NULL OR d.as_of > m.dropped_at)
		  AND (SELECT count(DISTINCT f.tbl) FROM tracks.hour_file f WHERE f.day = d.day AND f.counts_as_of >= d.as_of) < 2
		ORDER BY d.day`, before)
	if err != nil {
		return false, err
	}
	type cand struct {
		day  time.Time
		open bool
	}
	cands, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cand, error) {
		var c cand
		err := r.Scan(&c.day, &c.open)
		c.day = truncDay(c.day)
		return c, err
	})
	if err != nil {
		return false, err
	}
	if l.m != nil {
		l.m.HourDaysWaiting.Set(float64(len(cands)))
	}
	for _, c := range cands {
		if c.open {
			continue
		}
		final, err := l.dayIsFinal(ctx, c.day)
		if err != nil {
			return false, err
		}
		if !final {
			continue
		}
		start := time.Now()
		files, err := l.writeDayFiles(ctx, c.day)
		if err != nil {
			if l.m != nil {
				l.m.HourFiles.WithLabelValues("failed").Inc()
			}
			return false, fmt.Errorf("hour files of %s: %w", c.day.Format("2006-01-02"), err)
		}
		if l.m != nil {
			l.m.HourFiles.WithLabelValues("written").Add(float64(len(files)))
			l.m.HourDaysWaiting.Set(float64(len(cands) - 1))
		}
		for _, f := range files {
			l.log.Info("hour file written", "key", f.key, "rows", f.rows, "sightings", f.sightings, "bytes", f.bytes,
				"took_ms", time.Since(start).Milliseconds())
		}
		return true, nil
	}
	return false, nil
}

type writtenFile struct {
	tbl                    string
	version                int
	key, sha256            string
	bytes, rows, sightings int64
	hours                  int
}

// writeDayFiles writes, archives and checks one day's hour files, then
// records them.
func (l *Loader) writeDayFiles(ctx context.Context, day time.Time) ([]writtenFile, error) {
	dir, err := os.MkdirTemp("", "tracks-hour-files-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// One snapshot for both tables and the day's last close.
	type made struct {
		t     hourTable
		path  string
		tally dayTally
	}
	var mades []made
	var asOf time.Time
	err = pgx.BeginTxFunc(ctx, l.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT max(GREATEST(closed_at, imported_at)) FROM tracks.hour_state WHERE hour >= $1 AND hour < $2`,
			day, day.AddDate(0, 0, 1)).Scan(&asOf); err != nil {
			return err
		}
		for _, t := range hourTables {
			path := filepath.Join(dir, t.name+".parquet")
			f, err := os.Create(path)
			if err != nil {
				return err
			}
			tally, err := t.writeDay(ctx, tx, day, f)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return fmt.Errorf("write %s: %w", t.name, err)
			}
			counted, err := sqlTally(ctx, tx, t, day)
			if err != nil {
				return err
			}
			if d := differ(tally, counted, true); len(d) > 0 {
				return fmt.Errorf("%s: the rows read add up to other totals than the database counts, at %s", t.name, hourList(d))
			}
			mades = append(mades, made{t, path, tally})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var out []writtenFile
	for _, m := range mades {
		sum, md, size, err := fileSums(m.path)
		if err != nil {
			return nil, err
		}
		var version int
		if err := l.db.QueryRow(ctx, `SELECT COALESCE(max(version), 0) FROM tracks.hour_file WHERE tbl = $1 AND day = $2`,
			m.t.name, day).Scan(&version); err != nil {
			return nil, err
		}
		var key string
		for tries := 0; ; tries++ {
			version++
			key = m.t.key(day, version)
			err = putFile(ctx, l.store, key, m.path, size, md)
			// A key holding other bytes was written by a run cut off before it
			// recorded the file: it stays, and this one takes the next version.
			if errors.Is(err, archive.ErrDifferent) && tries < 5 {
				continue
			}
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archive %s: %w", key, err)
		}
		back := filepath.Join(dir, m.t.name+".back.parquet")
		if err := getFile(ctx, l.store, key, back); err != nil {
			return nil, fmt.Errorf("read back %s: %w", key, err)
		}
		got, err := m.t.tallyFile(back)
		if err != nil {
			return nil, fmt.Errorf("read back %s: %w", key, err)
		}
		if d := differ(m.tally, got, false); len(d) > 0 {
			return nil, fmt.Errorf("the archive's copy of %s differs from the database at %s", key, hourList(d))
		}
		rows, sightings := m.tally.totals()
		out = append(out, writtenFile{tbl: m.t.name, version: version, key: key, sha256: sum, bytes: size,
			rows: rows, sightings: sightings, hours: len(m.tally)})
	}
	err = pgx.BeginFunc(ctx, l.db, func(tx pgx.Tx) error {
		for _, f := range out {
			if _, err := tx.Exec(ctx, `
				INSERT INTO tracks.hour_file (tbl, day, version, key, sha256, bytes, rows, sightings, hours, counts_as_of)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				f.tbl, day, f.version, f.key, f.sha256, f.bytes, f.rows, f.sightings, f.hours, asOf); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// MonthCheck is how a month's hourly counts in the database compare with its
// hour files, day by day and hour by hour, before its partitions may go.
type MonthCheck struct {
	Month    time.Time
	Archived bool      // its partitions were dropped before
	DropFrom time.Time // the first day it may be dropped (the keep time after it ends)
	Days     []DayCheck
	Daily    []DailyCheck
	Bytes    int64 // the partitions' size in the database
	Problems []string
	started  time.Time
}

// OK reports whether every day matched and nothing stands in the way.
func (c MonthCheck) OK() bool { return len(c.Problems) == 0 }

// DayCheck is one day of one table: the database against its hour file.
type DayCheck struct {
	Day           time.Time
	Table         string
	Key           string // the hour file read, "" when there is none
	DBRows        int64
	FileRows      int64
	DBSightings   int64
	FileSightings int64
	Hours         int // hours with rows
	HoursDiffer   int // hours whose rows, sightings or any value differ
}

// DailyCheck sets a day's daily counts, which stay in the database, against
// its hourly sightings.
type DailyCheck struct {
	Day             time.Time
	HourlySightings int64
	DailySightings  int64
}

// CheckMonth compares the hourly counts of month (its first day, UTC) in the
// database with their hour files. For a month not archived yet it checks
// every day with closed hours or rows; for an archived one, the days back in
// the database. keep is the keep time in days, for DropFrom.
func (l *Loader) CheckMonth(ctx context.Context, month time.Time, keep int) (MonthCheck, error) {
	month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	next := month.AddDate(0, 1, 0)
	c := MonthCheck{Month: month, DropFrom: next.AddDate(0, 0, keep)}
	problem := func(f string, a ...any) { c.Problems = append(c.Problems, fmt.Sprintf(f, a...)) }
	// The database's clock, which stamps every close.
	if err := l.db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&c.started); err != nil {
		return c, err
	}

	var dropped *time.Time
	if err := l.db.QueryRow(ctx, `SELECT dropped_at FROM tracks.archived_month WHERE month = $1`, month).Scan(&dropped); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	c.Archived = dropped != nil

	days := map[time.Time]bool{}
	for d := month; d.Before(next); d = d.AddDate(0, 0, 1) {
		var present bool
		if err := l.db.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM tracks.ad_hourly WHERE hour >= $1 AND hour < $2)
			    OR EXISTS (SELECT 1 FROM tracks.ad_account_brand_hourly WHERE hour >= $1 AND hour < $2)
			    OR EXISTS (SELECT 1 FROM tracks.hour_state WHERE hour >= $1 AND hour < $2
			               AND ($3::timestamptz IS NULL OR GREATEST(closed_at, imported_at) > $3))`,
			d, d.AddDate(0, 0, 1), dropped).Scan(&present); err != nil {
			return c, err
		}
		if present {
			days[d] = true
		}
	}
	for _, t := range hourTables {
		var bytes int64
		if err := l.db.QueryRow(ctx, `SELECT COALESCE(pg_total_relation_size(to_regclass($1)), 0)`,
			"tracks."+t.partition(month)).Scan(&bytes); err != nil {
			return c, err
		}
		c.Bytes += bytes
	}

	dir, err := os.MkdirTemp("", "tracks-hour-check-")
	if err != nil {
		return c, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	for d := month; d.Before(next); d = d.AddDate(0, 0, 1) {
		if !days[d] {
			continue
		}
		label := d.Format("2006-01-02")
		final, err := l.dayIsFinal(ctx, d)
		if err != nil {
			return c, err
		}
		if !final {
			problem("%s is not final: an hour is open or dirty, or a raw file waits to load", label)
		}
		var asOf *time.Time
		if err := l.db.QueryRow(ctx, `SELECT max(GREATEST(closed_at, imported_at)) FROM tracks.hour_state WHERE hour >= $1 AND hour < $2`,
			d, d.AddDate(0, 0, 1)).Scan(&asOf); err != nil {
			return c, err
		}
		var dbTallies [2]dayTally
		err = pgx.BeginTxFunc(ctx, l.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			for i, t := range hourTables {
				read, err := t.readDay(ctx, tx, d)
				if err != nil {
					return err
				}
				counted, err := sqlTally(ctx, tx, t, d)
				if err != nil {
					return err
				}
				if diff := differ(read, counted, true); len(diff) > 0 {
					problem("%s %s: the rows read add up to other totals than the database counts, at %s", label, t.name, hourList(diff))
				}
				dbTallies[i] = read
			}
			return nil
		})
		if err != nil {
			return c, err
		}
		for i, t := range hourTables {
			dc := DayCheck{Day: d, Table: t.name}
			dc.DBRows, dc.DBSightings = dbTallies[i].totals()
			dc.Hours = len(dbTallies[i])
			var key string
			var rows, sightings int64
			var asOfFile time.Time
			err := l.db.QueryRow(ctx, `
				SELECT key, rows, sightings, counts_as_of FROM tracks.hour_file
				WHERE tbl = $1 AND day = $2 ORDER BY version DESC LIMIT 1`, t.name, d).Scan(&key, &rows, &sightings, &asOfFile)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				problem("%s %s has no hour file yet", label, t.name)
				dc.HoursDiffer = dc.Hours
				c.Days = append(c.Days, dc)
				continue
			case err != nil:
				return c, err
			}
			dc.Key = key
			if asOf != nil && asOfFile.Before(*asOf) {
				problem("%s %s closed again after its hour file was written; the loader writes it again", label, t.name)
			}
			path := filepath.Join(dir, t.name+".parquet")
			if err := getFile(ctx, l.store, key, path); err != nil {
				return c, fmt.Errorf("read %s: %w", key, err)
			}
			got, err := t.tallyFile(path)
			if err != nil {
				return c, fmt.Errorf("read %s: %w", key, err)
			}
			dc.FileRows, dc.FileSightings = got.totals()
			dc.HoursDiffer = len(differ(dbTallies[i], got, false))
			if dc.HoursDiffer > 0 {
				problem("%s %s differs from %s at %d hours", label, t.name, key, dc.HoursDiffer)
			}
			if dc.FileRows != rows || dc.FileSightings != sightings {
				problem("%s holds %d rows and %d sightings, but %d and %d were recorded when it was written",
					key, dc.FileRows, dc.FileSightings, rows, sightings)
			}
			c.Days = append(c.Days, dc)
		}
		var daily int64
		if err := l.db.QueryRow(ctx, `SELECT COALESCE(sum(sightings), 0) FROM tracks.ad_daily WHERE day = $1`, d).Scan(&daily); err != nil {
			return c, err
		}
		_, hourly := dbTallies[0].totals()
		c.Daily = append(c.Daily, DailyCheck{Day: d, HourlySightings: hourly, DailySightings: daily})
	}
	if len(days) == 0 && !c.Archived {
		problem("%s has no hourly counts in the database", month.Format("2006-01"))
	}
	return c, nil
}

// DropMonth checks a month and, only when every day matches its hour files,
// drops its hourly partitions and records it as archived. A month not
// archived yet must have ended keep days ago. It returns the check either way.
func (l *Loader) DropMonth(ctx context.Context, month time.Time, keep int) (MonthCheck, error) {
	c, err := l.CheckMonth(ctx, month, keep)
	if err != nil {
		return c, err
	}
	if !c.Archived && l.cfg.Now().Before(c.DropFrom) {
		return c, fmt.Errorf("%s keeps its hourly counts in the database until %s (%d days after it ends)",
			c.Month.Format("2006-01"), c.DropFrom.Format("2006-01-02"), keep)
	}
	if !c.OK() {
		return c, fmt.Errorf("nothing dropped: %s", strings.Join(c.Problems, "; "))
	}
	return c, l.dropChecked(ctx, c)
}

func (l *Loader) dropChecked(ctx context.Context, c MonthCheck) error {
	next := c.Month.AddDate(0, 1, 0)
	conn, err := l.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	// The maintain lock: no hour closes and no day comes back while the
	// partitions go. A close holds it for seconds.
	var got bool
	for i := 0; i < 60 && !got; i++ {
		if err := conn.QueryRow(ctx, maintainLock).Scan(&got); err != nil {
			return err
		}
		if !got {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if !got {
		return errBusy
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), maintainUnlock) }()

	var hourlyRows, brandRows, sightings int64
	for _, dc := range c.Days {
		if dc.Table == hourlyTable.name {
			hourlyRows += dc.DBRows
			sightings += dc.DBSightings
		} else {
			brandRows += dc.DBRows
		}
	}
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var changed bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM tracks.hour_state WHERE hour >= $1 AND hour < $2
			               AND (dirty OR GREATEST(closed_at, imported_at) > $3))`, c.Month, next, c.started).Scan(&changed); err != nil {
			return err
		}
		if changed {
			return errors.New("an hour of the month closed again during the check; nothing dropped, check again")
		}
		rows, err := tx.Query(ctx, `
			SELECT loaded_at IS NULL AND attempts < 3 OR keep_until > now() FROM tracks.hour_bring_back
			WHERE day >= $1 AND day < $2 FOR UPDATE`, c.Month, next)
		if err != nil {
			return err
		}
		kept, err := pgx.CollectRows(rows, pgx.RowTo[bool])
		if err != nil {
			return err
		}
		for _, k := range kept {
			if k {
				return errors.New("a day of the month was asked for again; it goes once nobody keeps it")
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM tracks.hour_bring_back WHERE day >= $1 AND day < $2`, c.Month, next); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO tracks.archived_month (month, days, hourly_rows, brand_rows, sightings)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (month) DO UPDATE SET dropped_at = now()`,
			c.Month, len(c.Daily), hourlyRows, brandRows, sightings)
		return err
	})
	if err != nil {
		return err
	}
	for _, t := range hourTables {
		name := t.partition(c.Month)
		exists, detached, pending, err := l.partState(ctx, name)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := l.dropPartition(ctx, t.name, name, detached, pending); err != nil {
			return err
		}
		l.log.Info("hourly partition dropped: its days are in hour files", "partition", name)
	}
	return nil
}

// partState says whether tracks.<name> exists and how it hangs off its parent.
func (l *Loader) partState(ctx context.Context, name string) (exists, detached, pending bool, err error) {
	var p *bool
	err = l.db.QueryRow(ctx, `
		SELECT i.inhdetachpending FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_inherits i ON i.inhrelid = c.oid
		WHERE n.nspname = 'tracks' AND c.relname = $1 AND c.relkind = 'r'`, name).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, false, nil
	}
	if err != nil {
		return false, false, false, err
	}
	return true, p == nil, p != nil && *p, nil
}

// BringBack loads the oldest day a page asked for back into the hourly
// tables, from its hour files. It reports whether it loaded one.
func (l *Loader) BringBack(ctx context.Context) (bool, error) {
	var day time.Time
	err := l.db.QueryRow(ctx, `
		SELECT day FROM tracks.hour_bring_back WHERE loaded_at IS NULL AND attempts < 3
		ORDER BY asked_at, day LIMIT 1`).Scan(&day)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	day = truncDay(day)
	start := time.Now()
	rows, err := l.bringBack(ctx, day)
	if errors.Is(err, errBusy) {
		return false, nil
	}
	if err != nil {
		if _, uerr := l.db.Exec(ctx, `UPDATE tracks.hour_bring_back SET attempts = attempts + 1, last_error = $2 WHERE day = $1`,
			day, err.Error()); uerr != nil {
			err = errors.Join(err, uerr)
		}
		if l.m != nil {
			l.m.BroughtBack.WithLabelValues("failed").Inc()
		}
		return false, fmt.Errorf("bring back %s: %w", day.Format("2006-01-02"), err)
	}
	if l.m != nil {
		l.m.BroughtBack.WithLabelValues("loaded").Inc()
	}
	l.log.Info("archived hours brought back", "day", day.Format("2006-01-02"), "rows", rows, "took_ms", time.Since(start).Milliseconds())
	return true, nil
}

func (l *Loader) bringBack(ctx context.Context, day time.Time) (int64, error) {
	rows, err := l.db.Query(ctx, `
		SELECT DISTINCT ON (tbl) tbl, key, rows, sightings FROM tracks.hour_file WHERE day = $1 ORDER BY tbl, version DESC`, day)
	if err != nil {
		return 0, err
	}
	type file struct {
		tbl, key        string
		rows, sightings int64
	}
	files := map[string]file{}
	for rows.Next() {
		var f file
		if err := rows.Scan(&f.tbl, &f.key, &f.rows, &f.sightings); err != nil {
			rows.Close()
			return 0, err
		}
		files[f.tbl] = f
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	dir, err := os.MkdirTemp("", "tracks-bring-back-")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, t := range hourTables {
		f, ok := files[t.name]
		if !ok {
			return 0, fmt.Errorf("%s has no hour file", t.name)
		}
		if err := getFile(ctx, l.store, f.key, filepath.Join(dir, t.name+".parquet")); err != nil {
			return 0, fmt.Errorf("read %s: %w", f.key, err)
		}
	}
	if _, err := l.db.Exec(ctx, `SELECT tracks.ensure_month_partitions($1::date, $1::date)`, day); err != nil {
		return 0, err
	}
	var total int64
	err = pgx.BeginFunc(ctx, l.db, func(tx pgx.Tx) error {
		var got bool
		if err := tx.QueryRow(ctx, closeLock).Scan(&got); err != nil {
			return err
		}
		if !got {
			return errBusy
		}
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM tracks.hourly_day_state($1::date, $1::date)`, day).Scan(&state); err != nil {
			return err
		}
		if state == "coming" {
			for _, t := range hourTables {
				f := files[t.name]
				if _, err := tx.Exec(ctx, "DELETE FROM "+pgx.Identifier{"tracks", t.name}.Sanitize()+" WHERE hour >= $1 AND hour < $2",
					day, day.AddDate(0, 0, 1)); err != nil {
					return err
				}
				copied, err := t.copyFile(ctx, tx, filepath.Join(dir, t.name+".parquet"))
				if err != nil {
					return fmt.Errorf("copy %s: %w", f.key, err)
				}
				n, s := copied.totals()
				if n != f.rows || s != f.sightings {
					return fmt.Errorf("%s gave %d rows and %d sightings, not the %d and %d recorded", f.key, n, s, f.rows, f.sightings)
				}
				total += n
			}
		}
		// A day that is in the database already (a replay closed it again)
		// keeps its newer counts: nothing to bring back.
		_, err := tx.Exec(ctx, `
			UPDATE tracks.hour_bring_back SET loaded_at = now(), keep_until = GREATEST(keep_until, now() + interval '1 day'),
			    last_error = NULL
			WHERE day = $1`, day)
		return err
	})
	return total, err
}

// KeepArchived drops again the hourly rows back in an archived month: days
// brought back once nobody keeps them, and days a replay closed again once
// their new hour files are written. Each drop runs the same check as the
// first.
func (l *Loader) KeepArchived(ctx context.Context) error {
	rows, err := l.db.Query(ctx, `
		SELECT m.month FROM tracks.archived_month m
		WHERE (to_regclass('tracks.ad_hourly_' || to_char(m.month, 'YYYYMM')) IS NOT NULL
		       OR to_regclass('tracks.ad_account_brand_hourly_' || to_char(m.month, 'YYYYMM')) IS NOT NULL)
		  AND NOT EXISTS (SELECT 1 FROM tracks.hour_bring_back b
		                  WHERE b.day >= m.month AND b.day < m.month + interval '1 month'
		                    AND (b.loaded_at IS NULL AND b.attempts < 3 OR b.keep_until > now()))
		ORDER BY 1`)
	if err != nil {
		return err
	}
	months, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		return err
	}
	for _, m := range months {
		c, err := l.CheckMonth(ctx, m, 0)
		if err != nil {
			return err
		}
		if !c.OK() {
			l.log.Info("archived month keeps hours in the database for now", "month", m.Format("2006-01"),
				"why", strings.Join(c.Problems, "; "))
			continue
		}
		if err := l.dropChecked(ctx, c); err != nil {
			if errors.Is(err, errBusy) {
				continue
			}
			return fmt.Errorf("drop %s again: %w", m.Format("2006-01"), err)
		}
		l.log.Info("archived month dropped again", "month", m.Format("2006-01"), "days", len(c.Daily))
	}
	return nil
}

// MonthStatus is where one month's hourly counts are.
type MonthStatus struct {
	Month       time.Time `json:"month"`
	Days        int       `json:"days"`         // days with closed hours
	Written     int       `json:"written"`      // days whose hour files are current
	Archived    bool      `json:"archived"`     // its partitions were dropped
	InDatabase  bool      `json:"in_database"`  // a partition exists
	BroughtBack int       `json:"brought_back"` // days back or coming
	Bytes       int64     `json:"bytes"`
	DropFrom    time.Time `json:"drop_from"`
}

// HourlyStatus lists every month with hourly counts or hour files.
func HourlyStatus(ctx context.Context, db querier, keep int) ([]MonthStatus, error) {
	rows, err := db.Query(ctx, `
		WITH d AS (
			SELECT (hour AT TIME ZONE 'UTC')::date AS day, max(GREATEST(closed_at, imported_at)) AS as_of
			FROM tracks.hour_state WHERE closed_at IS NOT NULL OR imported_at IS NOT NULL GROUP BY 1
		), m AS (
			SELECT date_trunc('month', day::timestamp)::date AS month, count(*) AS days,
			       count(*) FILTER (WHERE (SELECT count(DISTINCT f.tbl) FROM tracks.hour_file f
			                               WHERE f.day = d.day AND f.counts_as_of >= d.as_of) = 2) AS written
			FROM d GROUP BY 1
		)
		SELECT m.month, m.days, m.written, a.month IS NOT NULL,
		       to_regclass('tracks.ad_hourly_' || to_char(m.month, 'YYYYMM')) IS NOT NULL,
		       (SELECT count(*) FROM tracks.hour_bring_back b WHERE b.day >= m.month AND b.day < m.month + interval '1 month'),
		       COALESCE(pg_total_relation_size(to_regclass('tracks.ad_hourly_' || to_char(m.month, 'YYYYMM'))), 0)
		         + COALESCE(pg_total_relation_size(to_regclass('tracks.ad_account_brand_hourly_' || to_char(m.month, 'YYYYMM'))), 0)
		FROM m LEFT JOIN tracks.archived_month a ON a.month = m.month
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (MonthStatus, error) {
		var s MonthStatus
		err := r.Scan(&s.Month, &s.Days, &s.Written, &s.Archived, &s.InDatabase, &s.BroughtBack, &s.Bytes)
		s.Month = truncDay(s.Month)
		s.DropFrom = s.Month.AddDate(0, 1, keep)
		return s, err
	})
}

func hourList(hours []time.Time) string {
	var b strings.Builder
	for i, h := range hours {
		if i == 3 {
			fmt.Fprintf(&b, " and %d more", len(hours)-3)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(h.UTC().Format("2006-01-02 15:00"))
	}
	return b.String()
}

func fileSums(path string) (sha, md string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0, err
	}
	defer func() { _ = f.Close() }()
	s, m := sha256.New(), md5.New()
	size, err = io.Copy(io.MultiWriter(s, m), f)
	return hex.EncodeToString(s.Sum(nil)), hex.EncodeToString(m.Sum(nil)), size, err
}

func putFile(ctx context.Context, store archive.Store, key, path string, size int64, md string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return store.Put(ctx, key, f, size, md)
}

func getFile(ctx context.Context, store archive.Store, key, path string) error {
	r, err := store.Get(ctx, key)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
