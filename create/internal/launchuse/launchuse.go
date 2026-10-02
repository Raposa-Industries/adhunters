// Package launchuse counts the ads Launch made with each library creative,
// for Create's library pages ("no Launch: N anúncios"). It reads only
// Launch's published view, launch_api.item_v1, whose ad id starts with the
// first 10 hex characters of the creative's SHA-256 (GLOSSARY: ad id). The
// create_app login needs the launch_api_read role for it; without it the
// counts are off and the pages leave them out.
package launchuse

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxPrefixes is the most creatives one call counts.
const MaxPrefixes = 500

var prefixRe = regexp.MustCompile(`^[0-9a-f]{10}$`)

// Reader reads launch_api.
type Reader struct{ db *pgxpool.Pool }

// New returns a reader on db (Create's own pool).
func New(db *pgxpool.Pool) *Reader { return &Reader{db: db} }

// Prefixes keeps the valid, distinct creative prefixes (10 lower-case hex
// characters of a SHA-256; a whole SHA-256 is cut to its first 10).
func Prefixes(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.ToLower(strings.TrimSpace(p))
		if len(p) > 10 {
			p = p[:10]
		}
		if prefixRe.MatchString(p) && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) > MaxPrefixes {
		out = out[:MaxPrefixes]
	}
	return out
}

// Ads counts Launch's ads by creative prefix. ok is false when Create
// cannot read launch_api (no grant, or no such view): the counts are
// unknown then, not zero.
func (r *Reader) Ads(ctx context.Context, prefixes []string) (counts map[string]int, ok bool, err error) {
	counts = map[string]int{}
	prefixes = Prefixes(prefixes)
	if len(prefixes) == 0 {
		return counts, true, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT substr(ad_id, 4, 10), count(*)::int FROM launch_api.item_v1
		WHERE ad_id LIKE 'ah-%' AND substr(ad_id, 4, 10) = ANY ($1)
		GROUP BY 1`, prefixes)
	if err != nil {
		if unreadable(err) {
			return counts, false, nil
		}
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			return nil, false, err
		}
		counts[p] = n
	}
	if err := rows.Err(); err != nil {
		if unreadable(err) {
			return map[string]int{}, false, nil
		}
		return nil, false, err
	}
	return counts, true, nil
}

// unreadable is a refusal that means Create may not, or cannot, read the
// view: no schema, no view, no grant.
func unreadable(err error) bool {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return false
	}
	switch pe.Code {
	case "3F000", "42P01", "42501":
		return true
	}
	return false
}
