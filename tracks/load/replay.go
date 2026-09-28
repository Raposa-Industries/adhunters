package load

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Replayed says what a replay marked.
type Replayed struct {
	Files int64
	// WholeDays are days widened to whole because their sightings had been
	// dropped: a day's counts are rebuilt from all of its sightings, so all of
	// its files load again, whatever the network asked for.
	WholeDays []time.Time
}

// Replay marks the raw files of minutes [from, to) pending again (of one
// network, or all when network is empty). The running loader then loads them
// like new files: each load replaces what the file stored before, and the
// hours they touch close again. Nothing is deleted up front, so the numbers
// stay whole while the replay runs.
func Replay(ctx context.Context, db *pgxpool.Pool, from, to time.Time, network string) (Replayed, error) {
	var r Replayed
	if !from.Before(to) {
		return r, fmt.Errorf("replay: from %s is not before to %s", from, to)
	}
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT day::timestamptz FROM tracks.day_state
			WHERE sightings_dropped_at IS NOT NULL
			  AND day >= ($1::timestamptz AT TIME ZONE 'UTC')::date AND day <= ($2::timestamptz AT TIME ZONE 'UTC')::date
			ORDER BY day`, from, to.Add(-time.Nanosecond))
		if err != nil {
			return err
		}
		if r.WholeDays, err = pgx.CollectRows(rows, pgx.RowTo[time.Time]); err != nil {
			return err
		}
		n := func(tag interface{ RowsAffected() int64 }) { r.Files += tag.RowsAffected() }
		const reset = `UPDATE tracks.raw_file SET loaded_at = NULL, attempts = 0, last_error = NULL, quarantined_at = NULL `
		tag, err := tx.Exec(ctx, reset+`WHERE minute >= $1 AND minute < $2 AND ($3 = '' OR network = $3)`, from, to, network)
		if err != nil {
			return err
		}
		n(tag)
		for _, d := range r.WholeDays {
			// Files from one minute before the day can hold its first scrapes.
			tag, err := tx.Exec(ctx, reset+`WHERE minute >= $1::timestamptz - interval '1 minute' AND minute < $1::timestamptz + interval '1 day 1 minute'
				AND NOT (minute >= $2 AND minute < $3 AND ($4 = '' OR network = $4))`, d, from, to, network)
			if err != nil {
				return err
			}
			n(tag)
			if _, err := tx.Exec(ctx, `UPDATE tracks.day_state SET sightings_dropped_at = NULL WHERE day = ($1::timestamptz AT TIME ZONE 'UTC')::date`, d); err != nil {
				return err
			}
		}
		return nil
	})
	return r, err
}

// Status is the loader's state, for people and for the books-balance check.
type Status struct {
	Pending, Quarantined int64
	OldestPending        *time.Time
	LastClosedHour       *time.Time
	DirtyHours           int64
	// Unbalanced lists loaded files whose scrapes differ from their records,
	// and closed hours whose counts differ from their sightings.
	UnbalancedFiles []string
	UnbalancedHours []time.Time
}

// ReadStatus reads the loader's state. books also checks that the books
// balance for the last day, which reads a day of sightings.
func ReadStatus(ctx context.Context, db *pgxpool.Pool, books bool) (Status, error) {
	var s Status
	if err := db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE quarantined_at IS NULL), count(*) FILTER (WHERE quarantined_at IS NOT NULL),
		       min(minute) FILTER (WHERE quarantined_at IS NULL)
		FROM tracks.raw_file WHERE loaded_at IS NULL`).Scan(&s.Pending, &s.Quarantined, &s.OldestPending); err != nil {
		return s, err
	}
	if err := db.QueryRow(ctx, `
		SELECT max(hour) FILTER (WHERE closed_at IS NOT NULL AND NOT dirty), count(*) FILTER (WHERE dirty)
		FROM tracks.hour_state`).Scan(&s.LastClosedHour, &s.DirtyHours); err != nil {
		return s, err
	}
	if !books {
		return s, nil
	}
	rows, err := db.Query(ctx, `
		SELECT key FROM tracks.raw_file
		WHERE loaded_at > now() - interval '1 day' AND scrapes IS DISTINCT FROM rows ORDER BY minute`)
	if err != nil {
		return s, err
	}
	if s.UnbalancedFiles, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return s, err
	}
	rows, err = db.Query(ctx, `
		SELECT h.hour FROM tracks.hour_state h
		WHERE h.closed_at IS NOT NULL AND NOT h.dirty AND h.hour > now() - interval '1 day'
		  AND h.sightings IS DISTINCT FROM (SELECT COALESCE(sum(sightings), 0) FROM tracks.ad_hourly a WHERE a.hour = h.hour)
		ORDER BY h.hour`)
	if err != nil {
		return s, err
	}
	s.UnbalancedHours, err = pgx.CollectRows(rows, pgx.RowTo[time.Time])
	return s, err
}
