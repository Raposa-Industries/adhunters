package importold

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pages is what the collector knew about who runs the sites ads send people
// to, copied so nothing it gathered is lost when its database goes: sites,
// clues (its certificate and name-server clues cannot be found again from
// Tracks' walks), sellers, which accounts' clicks reached which sites, hand
// grouping fixes, agencies and the history of Direction changes.
type Pages struct {
	Sites        Counts
	Clues        Counts
	SiteClues    Counts
	Sellers      Counts
	SiteSellers  Counts
	AccountSites Counts
	Fixes        Counts
	Agencies     Counts
	Events       Counts
}

// part is one table read from the collector into a temporary table of the
// same shape. Big ones stream; the rest are read whole.
type part struct {
	table string
	cols  string
	query string
}

var pageParts = []part{
	{"old_site", "id INTEGER, domain TEXT, operator_id INTEGER, group_reason TEXT, first_seen_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ", `
		SELECT id, domain, operator_id, group_reason, first_seen_at, last_seen_at FROM spy.site`},
	{"old_clue", "id INTEGER, kind TEXT, value TEXT, strong BOOLEAN", `
		SELECT id, kind, value, strong FROM spy.clue`},
	{"old_site_clue", "site_id INTEGER, clue_id INTEGER, first_seen_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ", `
		SELECT site_id, clue_id, first_seen_at, last_seen_at FROM spy.site_clue`},
	{"old_seller", "id INTEGER, platform TEXT, account TEXT", `
		SELECT id, platform, account FROM spy.seller`},
	// The collector kept a page version's seller; a site's sellers are those
	// of its pages' versions.
	{"old_site_seller", "site_id INTEGER, seller_id INTEGER, first_seen_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ", `
		SELECT lp.site_id, v.seller_id, min(v.first_seen_at), max(v.last_seen_at)
		FROM spy.landing_page_version v JOIN spy.landing_page lp ON lp.id = v.landing_page_id
		WHERE v.seller_id IS NOT NULL
		GROUP BY 1, 2`},
	{"old_account_site", "network TEXT, external_id TEXT, site_id INTEGER, first_seen_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ", `
		SELECT n.code, a.external_id, lp.site_id, min(x.first_seen_at), max(x.last_seen_at)
		FROM spy.account_landing_page x
		JOIN spy.account a ON a.id = x.account_id
		JOIN spy.network n ON n.id = a.network_id
		JOIN spy.landing_page lp ON lp.id = x.landing_page_id
		GROUP BY 1, 2, 3`},
	{"old_fix", "id INTEGER, action TEXT, domain TEXT, network TEXT, external_id TEXT, operator_id INTEGER, note TEXT, made_by TEXT, made_at TIMESTAMPTZ", `
		SELECT f.id, f.action, s.domain, n.code, a.external_id, f.operator_id, f.note, f.created_by, f.created_at
		FROM spy.grouping_fix f
		LEFT JOIN spy.site s ON s.id = f.site_id
		LEFT JOIN spy.account a ON a.id = f.account_id
		LEFT JOIN spy.network n ON n.id = a.network_id`},
	{"old_agency", "name_root TEXT, name TEXT, operator_count INTEGER", `
		SELECT name_root, name, operator_count FROM spy.agency`},
	// Vertical events name the collector's verticals, which verticals.yaml
	// replaced (decision 0017): they stay in the archive.
	{"old_event", "id BIGINT, at TIMESTAMPTZ, kind TEXT, key TEXT, creative_key TEXT, headline TEXT, from_direction TEXT, to_direction TEXT, reason JSONB, reason_text TEXT", `
		SELECT e.id, e.at, e.kind, e.key, COALESCE(c.creative_key, ac.creative_key), ad.headline,
		       e.from_direction, e.to_direction, e.reason, e.reason_text
		FROM spy.direction_event e
		LEFT JOIN spy.creative c ON e.kind = 'creative' AND c.id = e.subject_id
		LEFT JOIN spy.ad ad ON e.kind = 'ad' AND ad.id = e.subject_id
		LEFT JOIN spy.creative ac ON ac.id = ad.creative_id
		WHERE e.kind <> 'vertical'`},
}

// copyPages reads the collector's pages, grouping fixes, agencies and
// Direction history into tx. Each run replaces what the last one wrote and
// keeps what Spy found itself: sites, clues and sellers are merged (first
// and last seen widen), copied fixes and events are replaced, Spy's own are
// left alone. A site's operator is copied only while operators come from the
// collector (copyOps).
func copyPages(ctx context.Context, old *pgxpool.Pool, tx pgx.Tx, copyOps bool) (Pages, error) {
	var p Pages
	read := map[string]int{}
	for _, pt := range pageParts {
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE `+pt.table+` (`+pt.cols+`) ON COMMIT DROP`); err != nil {
			return p, err
		}
		n, err := stream(ctx, old, tx, pt)
		if err != nil {
			return p, fmt.Errorf("old %s: %w", pt.table[4:], err)
		}
		read[pt.table] = int(n)
	}

	count := func(what string, q string) (int, error) {
		var n int64
		if err := tx.QueryRow(ctx, q).Scan(&n); err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
		return int(n), nil
	}
	var err error
	var n int

	// Sites, matched by domain.
	if n, err = count("sites", `
		WITH ins AS (
		    INSERT INTO spy.site (domain, first_seen_at, last_seen_at)
		    SELECT domain, min(first_seen_at), max(last_seen_at) FROM old_site GROUP BY domain
		    ON CONFLICT (domain) DO UPDATE SET
		        first_seen_at = LEAST(spy.site.first_seen_at, EXCLUDED.first_seen_at),
		        last_seen_at = GREATEST(spy.site.last_seen_at, EXCLUDED.last_seen_at)
		    RETURNING 1)
		SELECT count(*) FROM ins`); err != nil {
		return p, err
	}
	p.Sites = Counts{Copied: n, Skipped: read["old_site"] - n}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE m_site ON COMMIT DROP AS
		SELECT o.id AS old_id, s.id AS new_id FROM old_site o JOIN spy.site s ON s.domain = o.domain`); err != nil {
		return p, err
	}
	if copyOps {
		if _, err := tx.Exec(ctx, `
			UPDATE spy.site s SET operator_id = o.operator_id, group_reason = o.group_reason
			FROM old_site o JOIN spy.operator op ON op.id = o.operator_id
			WHERE s.domain = o.domain
			  AND (s.operator_id, s.group_reason) IS DISTINCT FROM (o.operator_id, o.group_reason)`); err != nil {
			return p, fmt.Errorf("site operators: %w", err)
		}
	}

	// Clues and sellers, matched by what they say. A clue Spy also found
	// keeps Spy's strength.
	if _, err = count("clues", `
		WITH ins AS (
		    INSERT INTO spy.clue (kind, value, strong)
		    SELECT DISTINCT ON (kind, value) kind, value, strong FROM old_clue ORDER BY kind, value, strong DESC
		    ON CONFLICT (kind, value) DO NOTHING
		    RETURNING 1)
		SELECT count(*) FROM ins`); err != nil {
		return p, err
	}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE m_clue ON COMMIT DROP AS
		SELECT o.id AS old_id, c.id AS new_id FROM old_clue o JOIN spy.clue c ON c.kind = o.kind AND c.value = o.value`); err != nil {
		return p, err
	}
	if n, err = count("clues", `SELECT count(*) FROM m_clue`); err != nil {
		return p, err
	}
	p.Clues = Counts{Copied: n, Skipped: read["old_clue"] - n}
	if n, err = count("site clues", `
		WITH ins AS (
		    INSERT INTO spy.site_clue (site_id, clue_id, first_seen_at, last_seen_at)
		    SELECT s.new_id, c.new_id, min(o.first_seen_at), max(o.last_seen_at)
		    FROM old_site_clue o JOIN m_site s ON s.old_id = o.site_id JOIN m_clue c ON c.old_id = o.clue_id
		    GROUP BY 1, 2
		    ON CONFLICT (site_id, clue_id) DO UPDATE SET
		        first_seen_at = LEAST(spy.site_clue.first_seen_at, EXCLUDED.first_seen_at),
		        last_seen_at = GREATEST(spy.site_clue.last_seen_at, EXCLUDED.last_seen_at)
		    RETURNING 1)
		SELECT count(*) FROM ins`); err != nil {
		return p, err
	}
	p.SiteClues = Counts{Copied: n, Skipped: read["old_site_clue"] - n}
	if _, err = count("sellers", `
		WITH ins AS (
		    INSERT INTO spy.seller (platform, account)
		    SELECT DISTINCT platform, account FROM old_seller
		    ON CONFLICT (platform, account) DO NOTHING
		    RETURNING 1)
		SELECT count(*) FROM ins`); err != nil {
		return p, err
	}
	if n, err = count("sellers", `
		SELECT count(*) FROM old_seller o JOIN spy.seller se ON se.platform = o.platform AND se.account = o.account`); err != nil {
		return p, err
	}
	p.Sellers = Counts{Copied: n, Skipped: read["old_seller"] - n}
	if n, err = count("site sellers", `
		WITH ins AS (
		    INSERT INTO spy.site_seller (site_id, seller_id, first_seen_at, last_seen_at)
		    SELECT s.new_id, se.id, min(o.first_seen_at), max(o.last_seen_at)
		    FROM old_site_seller o
		    JOIN m_site s ON s.old_id = o.site_id
		    JOIN old_seller os ON os.id = o.seller_id
		    JOIN spy.seller se ON se.platform = os.platform AND se.account = os.account
		    GROUP BY 1, 2
		    ON CONFLICT (site_id, seller_id) DO UPDATE SET
		        first_seen_at = LEAST(spy.site_seller.first_seen_at, EXCLUDED.first_seen_at),
		        last_seen_at = GREATEST(spy.site_seller.last_seen_at, EXCLUDED.last_seen_at)
		    RETURNING 1)
		SELECT count(*) FROM ins`); err != nil {
		return p, err
	}
	p.SiteSellers = Counts{Copied: n, Skipped: read["old_site_seller"] - n}

	// Which accounts' clicks reached which sites; an account Tracks has not
	// seen is skipped.
	if n, err = count("account sites", `
		WITH ins AS (
		    INSERT INTO spy.account_site (account_id, site_id, first_seen_at, last_seen_at)
		    SELECT a.id, s.new_id, min(o.first_seen_at), max(o.last_seen_at)
		    FROM old_account_site o
		    JOIN tracks_api.network_v1 nw ON nw.code = o.network
		    JOIN tracks_api.account_v1 a ON a.network_id = nw.id AND a.external_id = o.external_id
		    JOIN m_site s ON s.old_id = o.site_id
		    GROUP BY 1, 2
		    ON CONFLICT (account_id, site_id) DO UPDATE SET
		        first_seen_at = LEAST(spy.account_site.first_seen_at, EXCLUDED.first_seen_at),
		        last_seen_at = GREATEST(spy.account_site.last_seen_at, EXCLUDED.last_seen_at)
		    RETURNING 1)
		SELECT count(*) FROM ins`); err != nil {
		return p, err
	}
	p.AccountSites = Counts{Copied: n, Skipped: read["old_account_site"] - n}

	// Hand fixes: the copied ones are replaced. A join needs its operator.
	if _, err := tx.Exec(ctx, `DELETE FROM spy.grouping_fix WHERE old_id IS NOT NULL`); err != nil {
		return p, fmt.Errorf("fixes: %w", err)
	}
	if n, err = count("fixes", `
		WITH ins AS (
		    INSERT INTO spy.grouping_fix (old_id, site_id, account_id, action, operator_id, note, made_by, made_at)
		    SELECT o.id, s.id, a.id, o.action, op.id, o.note, o.made_by, o.made_at
		    FROM old_fix o
		    LEFT JOIN spy.site s ON s.domain = o.domain
		    LEFT JOIN tracks_api.network_v1 nw ON nw.code = o.network
		    LEFT JOIN tracks_api.account_v1 a ON a.network_id = nw.id AND a.external_id = o.external_id
		    LEFT JOIN spy.operator op ON op.id = o.operator_id
		    WHERE (s.id IS NULL) <> (a.id IS NULL)
		      AND (o.action = 'split' OR op.id IS NOT NULL)
		    RETURNING 1)
		SELECT count(*) FROM ins`); err != nil {
		return p, err
	}
	p.Fixes = Counts{Copied: n, Skipped: read["old_fix"] - n}

	// Agencies: the collector's are kept as agencies (regroup_operators).
	if n, err = count("agencies", `
		WITH ins AS (
		    INSERT INTO spy.agency (name_root, name, operator_count, imported, updated_at)
		    SELECT DISTINCT ON (name_root) name_root, name, operator_count, TRUE, now() FROM old_agency
		    ORDER BY name_root, operator_count DESC
		    ON CONFLICT (name_root) DO UPDATE SET name = EXCLUDED.name, imported = TRUE,
		        operator_count = GREATEST(spy.agency.operator_count, EXCLUDED.operator_count)
		    RETURNING 1)
		SELECT count(*) FROM ins`); err != nil {
		return p, err
	}
	p.Agencies = Counts{Copied: n, Skipped: read["old_agency"] - n}

	// Direction history before Spy's own: the copied events are replaced,
	// with Tracks' ids for ads and creatives. An operator's id is the same.
	if _, err := tx.Exec(ctx, `DELETE FROM spy.direction_event WHERE old_id IS NOT NULL`); err != nil {
		return p, fmt.Errorf("events: %w", err)
	}
	if n, err = count("events", `
		WITH ins AS (
		    INSERT INTO spy.direction_event (old_id, at, kind, key, from_direction, to_direction, reason, reason_text)
		    SELECT o.id, o.at, o.kind,
		           CASE o.kind WHEN 'creative' THEN c.id::text WHEN 'ad' THEN ad.id::text ELSE o.key END,
		           o.from_direction, o.to_direction, o.reason, o.reason_text
		    FROM old_event o
		    LEFT JOIN tracks_api.creative_v1 c ON c.creative_key = o.creative_key
		    LEFT JOIN tracks_api.ad_v1 ad ON o.kind = 'ad' AND ad.creative_id = c.id AND ad.headline = o.headline
		    WHERE o.at < COALESCE((SELECT min(at) FROM spy.direction_event WHERE old_id IS NULL), 'infinity')
		      AND CASE o.kind WHEN 'creative' THEN c.id IS NOT NULL
		                      WHEN 'ad' THEN ad.id IS NOT NULL
		                      WHEN 'operator' THEN EXISTS (SELECT 1 FROM spy.operator op WHERE op.id::text = o.key)
		                      ELSE o.kind = 'network' END
		    RETURNING 1)
		SELECT count(*) FROM ins`); err != nil {
		return p, err
	}
	p.Events = Counts{Copied: n, Skipped: read["old_event"] - n}
	return p, nil
}

// stream copies the rows of pt's query on old into its temporary table,
// without holding them all in memory.
func stream(ctx context.Context, old *pgxpool.Pool, tx pgx.Tx, pt part, args ...any) (int64, error) {
	rows, err := old.Query(ctx, pt.query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols := make([]string, 0, len(rows.FieldDescriptions()))
	for _, f := range rows.FieldDescriptions() {
		cols = append(cols, f.Name)
	}
	names, err := tempColumns(ctx, tx, pt.table)
	if err != nil {
		return 0, err
	}
	if len(names) != len(cols) {
		return 0, fmt.Errorf("expected %d columns, got %d", len(names), len(cols))
	}
	n, err := tx.CopyFrom(ctx, pgx.Identifier{pt.table}, names, pgx.CopyFromFunc(func() ([]any, error) {
		if !rows.Next() {
			return nil, rows.Err()
		}
		return rows.Values()
	}))
	if err != nil {
		return n, err
	}
	return n, rows.Err()
}

// tempColumns lists a temporary table's columns in order.
func tempColumns(ctx context.Context, tx pgx.Tx, table string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT attname FROM pg_attribute
		WHERE attrelid = ('pg_temp.' || $1)::regclass AND attnum > 0 AND NOT attisdropped
		ORDER BY attnum`, table)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
