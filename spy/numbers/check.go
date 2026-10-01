package numbers

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// checkPart is one section of Check: a title and a query that returns
// (what, value) rows, both text. A query that reads the window has $1, its
// end, and $2, its start.
type checkPart struct {
	title string
	query string
}

var checkParts = []checkPart{
	{"Walker (Tracks' landing pages)", `
		WITH w AS (SELECT * FROM tracks_api.walk_page_v1 WHERE at > $2 AND at <= $1)
		SELECT 'walks with a page', count(DISTINCT walk_id)::text FROM w
		UNION ALL SELECT 'walks by outcome', COALESCE((SELECT string_agg(outcome || ' ' || n, ', ' ORDER BY n DESC)
			FROM (SELECT outcome, count(DISTINCT walk_id) n FROM w GROUP BY outcome) o), 'none')
		UNION ALL SELECT 'pages (landing page + next step)', count(*) || ' (' || count(*) FILTER (WHERE step > 0) || ' next steps)' FROM w
		UNION ALL SELECT 'pages by status', COALESCE((SELECT string_agg(s || ' ' || n, ', ' ORDER BY n DESC)
			FROM (SELECT CASE WHEN status BETWEEN 200 AND 299 THEN '2xx' WHEN status BETWEEN 400 AND 499 THEN '4xx'
			                  WHEN status >= 500 THEN '5xx' ELSE 'other ' || COALESCE(status::text, 'none') END s, count(*) n
			      FROM w GROUP BY 1) x), 'none')
		UNION ALL SELECT 'distinct hosts', count(DISTINCT host)::text FROM w
		UNION ALL SELECT 'checkout pages', COALESCE((SELECT string_agg(checkout_platform || ' ' || n, ', ' ORDER BY n DESC)
			FROM (SELECT checkout_platform, count(*) n FROM w WHERE checkout_platform IS NOT NULL GROUP BY 1) c), 'none')
		UNION ALL SELECT 'creatives walked / running', (SELECT count(DISTINCT creative_id) FROM w) || ' / '
			|| (SELECT count(DISTINCT creative_id) FROM spy.creative_recent)
		UNION ALL SELECT 'last walk', COALESCE((SELECT to_char(max(at), 'YYYY-MM-DD HH24:MI "UTC"') FROM tracks_api.walk_page_v1), 'never')`},
	{"Pages in Spy", `
		SELECT 'read to', COALESCE((SELECT to_char(read_to, 'YYYY-MM-DD HH24:MI "UTC"') FROM spy.page_mark), 'never')
		UNION ALL SELECT 'sites', count(*)::text FROM spy.site
		UNION ALL SELECT 'clues (strong)', count(*) || ' (' || count(*) FILTER (WHERE strong) || ')' FROM spy.clue
		UNION ALL SELECT 'sellers', count(*)::text FROM spy.seller
		UNION ALL SELECT 'creatives with a page', count(*)::text FROM spy.creative_page`},
	{"Classifier", `
		WITH c AS (SELECT * FROM spy.creative_class),
		     r AS (SELECT c.* FROM c JOIN (SELECT DISTINCT creative_id FROM spy.creative_recent) x USING (creative_id))
		SELECT 'creatives read', count(*)::text FROM c
		UNION ALL SELECT 'running now: with a vertical / unsure / none',
			count(*) FILTER (WHERE vertical_id IS NOT NULL) || ' / ' || count(*) FILTER (WHERE vertical_id IS NOT NULL AND unsure)
			|| ' / ' || count(*) FILTER (WHERE vertical_id IS NULL) FROM r
		UNION ALL SELECT 'running now by source', COALESCE((SELECT string_agg(s || ' ' || n, ', ' ORDER BY n DESC)
			FROM (SELECT COALESCE(source, 'none') s, count(*) n FROM r GROUP BY 1) x), 'none')
		UNION ALL SELECT 'running now, top verticals', COALESCE((SELECT string_agg(vertical_id || ' ' || n, ', ' ORDER BY n DESC)
			FROM (SELECT vertical_id, count(*) n FROM r WHERE vertical_id IS NOT NULL GROUP BY 1 ORDER BY 2 DESC LIMIT 8) x), 'none')
		UNION ALL SELECT 'read from a landing page', count(*) FILTER (WHERE source = 'page' OR rules_source = 'page')::text FROM c
		UNION ALL SELECT 'model', COALESCE((SELECT to_char(trained_at, 'YYYY-MM-DD HH24:MI') || ', ' || examples || ' examples, held out: '
			|| round(100 * (eval->>'accuracy')::numeric) || '% right, ' || round(100 * (eval->>'masked_accuracy')::numeric)
			|| '% without the keywords'
			FROM spy.class_model ORDER BY id DESC LIMIT 1), 'not trained yet')`},
	{"Grouping (Spy's own operators)", `
		WITH g AS (SELECT * FROM spy.grouping_run ORDER BY id DESC LIMIT 1),
		     now_op AS (SELECT ao.account_id, ao.operator_id, m.member_id IS NOT NULL AS seen, m.operator_id AS new_op
		                FROM spy.account_operator ao
		                LEFT JOIN spy.grouping_member m ON m.member = 'account' AND m.member_id = ao.account_id)
		SELECT 'operators come from', COALESCE((SELECT text_value FROM spy.setting WHERE name = 'operators_from'), 'import')
		UNION ALL SELECT 'last run', COALESCE((SELECT to_char(at, 'YYYY-MM-DD HH24:MI "UTC"') || CASE WHEN applied THEN ' (applied)'
			ELSE ' (proposed only)' END FROM g), 'never')
		UNION ALL SELECT 'groups / sites / accounts / agencies', COALESCE((SELECT groups || ' / ' || sites || ' / ' || accounts || ' / ' || agencies FROM g), '-')
		UNION ALL SELECT 'accounts with an operator now', count(*)::text FROM now_op
		UNION ALL SELECT '  on a landing page (grouped)', count(*) FILTER (WHERE seen) || ' ('
			|| COALESCE(round(100.0 * count(*) FILTER (WHERE seen) / NULLIF(count(*), 0)), 0) || '%)' FROM now_op
		UNION ALL SELECT '  of those, same operator', count(*) FILTER (WHERE seen AND new_op IS NOT DISTINCT FROM operator_id) || ' ('
			|| COALESCE(round(100.0 * count(*) FILTER (WHERE seen AND new_op IS NOT DISTINCT FROM operator_id)
			   / NULLIF(count(*) FILTER (WHERE seen), 0)), 0) || '%)' FROM now_op
		UNION ALL SELECT '  of those, moved or split out', count(*) FILTER (WHERE seen AND new_op IS DISTINCT FROM operator_id)::text FROM now_op
		UNION ALL SELECT '  not on a landing page yet (keep theirs)', count(*) FILTER (WHERE NOT seen)::text FROM now_op
		UNION ALL SELECT 'accounts with no operator now, grouped', count(*)::text
			FROM spy.grouping_member m WHERE m.member = 'account' AND m.grp IS NOT NULL
			  AND NOT EXISTS (SELECT 1 FROM spy.account_operator ao WHERE ao.account_id = m.member_id)`},
	{"Biggest differences (proposed group: accounts from today's operators)", `
		SELECT COALESCE(g.display_name, 'group ' || g.grp) || CASE WHEN g.operator_id IS NULL THEN ' (new)' ELSE ' (OP' || g.operator_id || ')' END,
		       string_agg(COALESCE('OP' || ao.operator_id, 'none') || ' ' || n, ', ' ORDER BY n DESC)
		FROM (SELECT m.grp, ao.operator_id, count(*) n
		      FROM spy.grouping_member m LEFT JOIN spy.account_operator ao ON ao.account_id = m.member_id
		      WHERE m.member = 'account' AND m.grp IS NOT NULL GROUP BY 1, 2) ao
		JOIN spy.grouping_group g ON g.grp = ao.grp
		GROUP BY g.grp, g.display_name, g.operator_id
		HAVING count(*) > 1 OR bool_or(ao.operator_id IS DISTINCT FROM g.operator_id)
		ORDER BY sum(n) DESC
		LIMIT 15`},
}

// Check writes what the walker, the classifier and the grouping did, the
// walker over the 24 hours to now: the report to read before switching
// operators_from to 'grouping'. It only reads.
func Check(ctx context.Context, db *pgxpool.Pool, w io.Writer, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for i, p := range checkParts {
		if i > 0 {
			fmt.Fprintln(tw)
		}
		fmt.Fprintln(tw, p.title)
		var args []any
		if strings.Contains(p.query, "$1") {
			args = []any{now, now.Add(-24 * time.Hour)}
		}
		rows, err := db.Query(ctx, p.query, args...)
		if err != nil {
			return fmt.Errorf("%s: %w", p.title, err)
		}
		n := 0
		for rows.Next() {
			var what, value *string
			if err := rows.Scan(&what, &value); err != nil {
				rows.Close()
				return err
			}
			fmt.Fprintf(tw, "  %s\t%s\n", deref(what), deref(value))
			n++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("%s: %w", p.title, err)
		}
		if n == 0 {
			fmt.Fprintln(tw, "  none")
		}
	}
	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "To use Spy's own grouping (the next run applies it; an account on no landing page keeps its operator):")
	fmt.Fprintln(tw, "  UPDATE spy.setting SET text_value = 'grouping' WHERE name = 'operators_from';")
	fmt.Fprintln(tw, "Back to the old collector's operators (then run spy-numbers import-old to copy them again):")
	fmt.Fprintln(tw, "  UPDATE spy.setting SET text_value = 'import' WHERE name = 'operators_from';")
	return tw.Flush()
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}
