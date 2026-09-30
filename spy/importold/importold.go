// Package importold copies Spy's groupings from the collector's database:
// which operator each account belongs to, the operators themselves, and the
// vertical of each creative. The collector keeps the operators until Spy has
// its own screens for them, so the copy is repeatable: each run replaces what
// the last one wrote. The verticals go to spy.creative_vertical_old, only to
// compare with Spy's own classifier (decision 0014); no number reads them.
//
// It only reads the old database (open it with a read-only login). Accounts
// and creatives are matched to Tracks by what both sides know: an account
// by its network's code and external id, a creative by its creative key.
// Rows Tracks has not seen yet are skipped and counted, and come in on a
// later run.
package importold

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Counts says what one grouping's copy did.
type Counts struct {
	Copied  int
	Skipped int
}

// Result holds the counts of each grouping.
type Result struct {
	Operators Counts
	Accounts  Counts
	Verticals Counts
}

// Run copies the three groupings from old into db in one transaction and
// records each in spy.import_mark.
func Run(ctx context.Context, old, db *pgxpool.Pool, log *slog.Logger) (Result, error) {
	var res Result
	ops, err := read(ctx, old, `SELECT id, name, display_name, kind, vertical FROM spy.operator`, 5)
	if err != nil {
		return res, fmt.Errorf("old operators: %w", err)
	}
	accs, err := read(ctx, old, `
		SELECT n.code, a.external_id, a.operator_id
		FROM spy.account a JOIN spy.network n ON n.id = a.network_id
		WHERE a.operator_id IS NOT NULL`, 3)
	if err != nil {
		return res, fmt.Errorf("old accounts: %w", err)
	}
	verts, err := read(ctx, old, `
		SELECT c.creative_key, cv.vertical, sv.name,
		       CASE WHEN c.vertical = 'Pets' THEN 'Pets'
		            WHEN cv.vertical = 'health' THEN COALESCE(sv.old_vertical, sv.name)
		            ELSE sv.name END,
		       cv.confidence, cv.unsure, cv.source, cv.health_from_funnel
		FROM spy.creative_vertical cv
		JOIN spy.creative c ON c.id = cv.creative_id
		LEFT JOIN spy.subvertical sv ON sv.id = cv.subvertical_id`, 8)
	if err != nil {
		return res, fmt.Errorf("old verticals: %w", err)
	}

	// An empty grouping means the wrong database or a broken collector;
	// copying it would wipe Spy's.
	if len(ops) == 0 || len(accs) == 0 || len(verts) == 0 {
		return res, fmt.Errorf("the old database has %d operators, %d grouped accounts and %d verticals; refusing to copy an empty grouping",
			len(ops), len(accs), len(verts))
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE old_operator (id INTEGER, name TEXT, display_name TEXT, kind TEXT, vertical TEXT) ON COMMIT DROP;
		CREATE TEMP TABLE old_account (network TEXT, external_id TEXT, operator_id INTEGER) ON COMMIT DROP;
		CREATE TEMP TABLE old_vertical (creative_key TEXT, vertical TEXT, subvertical TEXT, shown_vertical TEXT,
		    confidence NUMERIC, unsure BOOLEAN, source TEXT, health_from_funnel BOOLEAN) ON COMMIT DROP`); err != nil {
		return res, err
	}
	for _, c := range []struct {
		table string
		cols  []string
		rows  [][]any
	}{
		{"old_operator", []string{"id", "name", "display_name", "kind", "vertical"}, ops},
		{"old_account", []string{"network", "external_id", "operator_id"}, accs},
		{"old_vertical", []string{"creative_key", "vertical", "subvertical", "shown_vertical", "confidence", "unsure", "source", "health_from_funnel"}, verts},
	} {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{c.table}, c.cols, pgx.CopyFromRows(c.rows)); err != nil {
			return res, fmt.Errorf("load %s: %w", c.table, err)
		}
	}

	// Operators keep their ids, so OP123 stays OP123. One the collector no
	// longer has is removed with its accounts.
	var n int64
	if _, err := tx.Exec(ctx, `
		INSERT INTO spy.operator (id, name, display_name, kind, vertical, updated_at)
		SELECT id, COALESCE(name, 'OP' || id), display_name,
		       CASE WHEN kind IN ('direct', 'affiliate', 'arbitrage') THEN kind ELSE 'direct' END, vertical, now()
		FROM old_operator WHERE id IS NOT NULL
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, display_name = EXCLUDED.display_name,
		    kind = EXCLUDED.kind, vertical = EXCLUDED.vertical, updated_at = now()
		WHERE (spy.operator.name, spy.operator.display_name, spy.operator.kind, spy.operator.vertical)
		      IS DISTINCT FROM (EXCLUDED.name, EXCLUDED.display_name, EXCLUDED.kind, EXCLUDED.vertical)`); err != nil {
		return res, fmt.Errorf("operators: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM spy.operator o WHERE NOT EXISTS (SELECT 1 FROM old_operator x WHERE x.id = o.id)`); err != nil {
		return res, fmt.Errorf("operators: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		SELECT setval(pg_get_serial_sequence('spy.operator', 'id'), GREATEST((SELECT max(id) FROM spy.operator), 1))`); err != nil {
		return res, fmt.Errorf("operator ids: %w", err)
	}
	res.Operators = Counts{Copied: len(ops)}

	// Accounts: replaced whole. An account Tracks has not seen, or whose
	// operator is gone, is skipped.
	if _, err := tx.Exec(ctx, `DELETE FROM spy.account_operator`); err != nil {
		return res, fmt.Errorf("accounts: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		WITH ins AS (
		    INSERT INTO spy.account_operator (account_id, operator_id)
		    SELECT DISTINCT ON (a.id) a.id, x.operator_id
		    FROM old_account x
		    JOIN tracks_api.network_v1 nw ON nw.code = x.network
		    JOIN tracks_api.account_v1 a ON a.network_id = nw.id AND a.external_id = x.external_id
		    JOIN spy.operator o ON o.id = x.operator_id
		    ORDER BY a.id, x.operator_id
		    RETURNING 1)
		SELECT count(*) FROM ins`).Scan(&n); err != nil {
		return res, fmt.Errorf("accounts: %w", err)
	}
	res.Accounts = Counts{Copied: int(n), Skipped: len(accs) - int(n)}

	// Verticals: replaced whole, matched by creative key.
	if _, err := tx.Exec(ctx, `DELETE FROM spy.creative_vertical_old`); err != nil {
		return res, fmt.Errorf("verticals: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		WITH ins AS (
		    INSERT INTO spy.creative_vertical_old (creative_id, vertical, subvertical, shown_vertical, confidence, unsure,
		                                       source, health_from_funnel)
		    SELECT DISTINCT ON (c.id) c.id, x.vertical, x.subvertical, x.shown_vertical,
		           LEAST(COALESCE(x.confidence, 0), 9.99), COALESCE(x.unsure, TRUE), x.source, COALESCE(x.health_from_funnel, FALSE)
		    FROM old_vertical x
		    JOIN tracks_api.creative_v1 c ON c.creative_key = x.creative_key
		    ORDER BY c.id, x.confidence DESC NULLS LAST
		    RETURNING 1)
		SELECT count(*) FROM ins`).Scan(&n); err != nil {
		return res, fmt.Errorf("verticals: %w", err)
	}
	res.Verticals = Counts{Copied: int(n), Skipped: len(verts) - int(n)}

	for what, c := range map[string]Counts{"operators": res.Operators, "accounts": res.Accounts, "verticals": res.Verticals} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO spy.import_mark (what, done_at, copied, skipped) VALUES ($1, now(), $2, $3)
			ON CONFLICT (what) DO UPDATE SET done_at = now(), copied = $2, skipped = $3`, what, c.Copied, c.Skipped); err != nil {
			return res, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}
	log.Info("old groupings copied",
		"operators", res.Operators.Copied,
		"accounts", res.Accounts.Copied, "accounts_skipped", res.Accounts.Skipped,
		"verticals", res.Verticals.Copied, "verticals_skipped", res.Verticals.Skipped)
	return res, nil
}

func read(ctx context.Context, db *pgxpool.Pool, query string, cols int) ([][]any, error) {
	rows, err := db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		if len(vals) != cols {
			return nil, fmt.Errorf("expected %d columns, got %d", cols, len(vals))
		}
		out = append(out, vals)
	}
	return out, rows.Err()
}
