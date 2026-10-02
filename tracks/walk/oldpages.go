package walk

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OldPages is tracks.walk_page, where walk pages were kept with their URLs in
// full, against its copy in walk_step and page_url (decision 0025). Every
// value is compared, the URLs as text.
type OldPages struct {
	Gone           bool  // walk_page was dropped already
	Rows           int64 // walk_page rows
	Copied         int64 // of them, with a walk_step row
	Differ         int64 // of the copied, with any value different
	ViewReadsSteps bool  // tracks_api.walk_page_v1 reads walk_step (tracks migration 13)
	OldBytes       int64 // walk_page, its indexes and TOAST
	Steps          int64 // walk_step rows, new walks included
	StepBytes      int64
	URLs           int64 // page_url rows
	URLBytes       int64
}

// OK says whether walk_page may go: every row copied, none different, and
// the published view reading the copy.
func (o OldPages) OK() bool {
	return !o.Gone && o.Rows == o.Copied && o.Differ == 0 && o.ViewReadsSteps
}

// Problems says what holds walk_page back.
func (o OldPages) Problems() []string {
	var out []string
	if o.Gone {
		return []string{"walk_page was dropped already"}
	}
	if o.Rows != o.Copied {
		out = append(out, fmt.Sprintf("%d walk_page rows have no copy in walk_step", o.Rows-o.Copied))
	}
	if o.Differ > 0 {
		out = append(out, fmt.Sprintf("%d walk_page rows differ from their copy", o.Differ))
	}
	if !o.ViewReadsSteps {
		out = append(out, "tracks_api.walk_page_v1 still reads walk_page: update tracks-loader (tracks migration 13)")
	}
	return out
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CheckOldPages compares walk_page with walk_step, row by row and value by
// value. It reads every walk page once: a minute or so on a year of walks.
func CheckOldPages(ctx context.Context, db queryRower) (OldPages, error) {
	var o OldPages
	var exists bool
	if err := db.QueryRow(ctx, `SELECT to_regclass('tracks.walk_page') IS NOT NULL`).Scan(&exists); err != nil {
		return o, err
	}
	if err := db.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM tracks.walk_step), pg_total_relation_size('tracks.walk_step'),
		       (SELECT count(*) FROM tracks.page_url), pg_total_relation_size('tracks.page_url'),
		       pg_get_viewdef('tracks_api.walk_page_v1'::regclass) LIKE '%walk_step%'`).
		Scan(&o.Steps, &o.StepBytes, &o.URLs, &o.URLBytes, &o.ViewReadsSteps); err != nil {
		return o, err
	}
	if !exists {
		o.Gone = true
		return o, nil
	}
	err := db.QueryRow(ctx, `
		SELECT count(*), count(s.walk_id),
		       count(*) FILTER (WHERE s.walk_id IS NOT NULL AND (
		           u.url IS DISTINCT FROM p.url OR f.url IS DISTINCT FROM p.final_url
		           OR s.host IS DISTINCT FROM p.host OR s.status IS DISTINCT FROM p.status OR s.hops IS DISTINCT FROM p.hops
		           OR s.version_hash IS DISTINCT FROM p.version_hash OR s.page_type IS DISTINCT FROM p.page_type
		           OR s.checkout_platform IS DISTINCT FROM p.checkout_platform
		           OR s.seller_account IS DISTINCT FROM p.seller_account)),
		       pg_total_relation_size('tracks.walk_page')
		FROM tracks.walk_page p
		LEFT JOIN tracks.walk_step s ON s.walk_id = p.walk_id AND s.step = p.step
		LEFT JOIN tracks.page_url u ON u.id = s.url_id
		LEFT JOIN tracks.page_url f ON f.id = s.final_url_id`).
		Scan(&o.Rows, &o.Copied, &o.Differ, &o.OldBytes)
	return o, err
}

// DropOldPages drops walk_page, and the trigger that copied into walk_step,
// only when CheckOldPages finds nothing wrong. Nothing may write walk_page
// between the check and the drop: both run in one transaction that holds
// its writers back.
func DropOldPages(ctx context.Context, db *pgxpool.Pool) (OldPages, error) {
	var o OldPages
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '10s'`); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass('tracks.walk_page') IS NOT NULL`).Scan(&exists); err != nil {
			return err
		}
		if exists {
			if _, err := tx.Exec(ctx, `LOCK TABLE tracks.walk_page IN SHARE MODE`); err != nil {
				return err
			}
		}
		var err error
		if o, err = CheckOldPages(ctx, tx); err != nil {
			return err
		}
		if o.Gone {
			return errors.New("walk_page was dropped already")
		}
		if !o.OK() {
			return errors.New("walk_page does not match its copy; nothing was dropped")
		}
		if _, err := tx.Exec(ctx, `DROP TABLE tracks.walk_page`); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DROP FUNCTION tracks.walk_page_to_step()`)
		return err
	})
	return o, err
}
