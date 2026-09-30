package walk

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Due is an ad to walk.
type Due struct {
	AdID        int
	CreativeID  int
	AccountID   *int
	LinkID      int
	URL         string
	PublisherID *int
	Referer     *string
	SeenAt      time.Time
}

// Dues lists up to limit ads to walk now (tracks.walks_due).
func Dues(ctx context.Context, db *pgxpool.Pool, limit int, now time.Time) ([]Due, error) {
	rows, err := db.Query(ctx, `
		SELECT ad_id, creative_id, account_id, link_id, url, publisher_id, referer, seen_at
		FROM tracks.walks_due($1, $2)`, limit, now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Due, error) {
		var d Due
		err := r.Scan(&d.AdID, &d.CreativeID, &d.AccountID, &d.LinkID, &d.URL, &d.PublisherID, &d.Referer, &d.SeenAt)
		return d, err
	})
}

// Save writes one walk and what its pages said. A walk saved again (a
// replay) replaces its pages; a page version already known only gets its
// times widened.
func Save(ctx context.Context, db *pgxpool.Pool, rec *Record, parsed []Parsed) error {
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO tracks.walk (record_id, at, ad_id, creative_id, account_id, link_id, publisher_id, line, outcome, error, ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11)
			ON CONFLICT (record_id) DO UPDATE SET outcome = EXCLUDED.outcome, error = EXCLUDED.error
			RETURNING id`,
			rec.ID, rec.At, rec.AdID, rec.CreativeID, rec.AccountID, rec.LinkID, rec.PublisherID, rec.Line,
			rec.Outcome(), rec.Error(), rec.LatencyMS).Scan(&id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM tracks.walk_page WHERE walk_id = $1`, id); err != nil {
			return err
		}
		for _, p := range parsed {
			var hash any
			if v := p.Version; v != nil {
				hash = v.Hash
				if err := saveVersion(ctx, tx, v, rec.At); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO tracks.walk_page (walk_id, step, url, final_url, host, status, hops, version_hash, page_type,
				                              checkout_platform, seller_account)
				VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, 0), $7, $8, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''))`,
				id, p.Step, p.URL, p.FinalURL, p.Host, p.Status, p.Hops, hash, p.PageType, p.Checkout, p.Seller); err != nil {
				return err
			}
		}
		return nil
	})
}

func saveVersion(ctx context.Context, tx pgx.Tx, v *Version, at time.Time) error {
	j := func(x any) []byte { b, _ := json.Marshal(x); return b }
	_, err := tx.Exec(ctx, `
		INSERT INTO tracks.page_version (hash, title, word_count, headings, meta, favicon_url, pixels, emails, phones,
		                                 companies, disclaimers, vsl, text, first_seen_at, last_seen_at)
		VALUES ($1, NULLIF($2, ''), $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10, $11, $12, $13, $14, $14)
		ON CONFLICT (hash) DO UPDATE SET
		    first_seen_at = LEAST(tracks.page_version.first_seen_at, EXCLUDED.first_seen_at),
		    last_seen_at = GREATEST(tracks.page_version.last_seen_at, EXCLUDED.last_seen_at)`,
		v.Hash, v.Title, v.WordCount, j(v.Headings), j(v.Meta), v.FaviconURL, j(v.Pixels), v.Emails, v.Phones,
		v.Companies, v.Disclaimers, j(v.VSL), v.Text, at)
	return err
}

// Walked records when an ad was walked and when to walk it again: after
// rewalk when the landing page answered, else after a backoff that starts at
// 15 minutes and doubles up to rewalk, as the collector's queue did.
func Walked(ctx context.Context, db *pgxpool.Pool, adID int, at time.Time, ok bool, rewalk time.Duration) error {
	_, err := db.Exec(ctx, `
		INSERT INTO tracks.walk_state AS s (ad_id, walked_at, next_at, failures)
		VALUES ($1, $2::timestamptz, $2::timestamptz + CASE WHEN $3::boolean THEN $4::interval ELSE LEAST(interval '15 minutes', $4::interval) END,
		        CASE WHEN $3::boolean THEN 0 ELSE 1 END)
		ON CONFLICT (ad_id) DO UPDATE SET
		    walked_at = EXCLUDED.walked_at,
		    failures = CASE WHEN $3::boolean THEN 0 ELSE LEAST(s.failures + 1, 10) END,
		    next_at = EXCLUDED.walked_at + CASE WHEN $3::boolean THEN $4::interval
		              ELSE LEAST(interval '15 minutes' * power(2, LEAST(s.failures, 10)), $4::interval) END`,
		adID, at, ok, rewalk)
	return err
}
