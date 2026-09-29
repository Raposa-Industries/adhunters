package load

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ImportConfig says what ImportOld copies.
type ImportConfig struct {
	// Before is the switch-over: 00:00 UTC of the first day Tracks scraped
	// alone. Everything before it is the collector's; nothing from it on is
	// copied. It must be a UTC midnight, so no day is split between the two.
	Before time.Time
	// From is the first day copied; zero means the collector's first.
	From time.Time
	Log  *slog.Logger
}

// Imported says what ImportOld copied.
type Imported struct {
	Days       int
	Hours      int
	Sightings  int64 // in the hourly counts copied
	Lookups    map[string]int64
	Publishers map[string]string // the collector's publisher name -> Tracks' (only where they differ)
}

// ImportOld copies the collector's history into Tracks, so the new Spy's
// numbers (Direction's usual weeks, any range) reach back before the
// switch-over. It reads the collector's database (old) and never writes it.
//
// First the lookups: every creative, ad, account, campaign, brand,
// publisher, placement, link and network ad, matched to Tracks' by their
// natural keys (creative_key, creative and headline, network and external
// id, name, link key). Rows Tracks has already seen keep Tracks' latest
// values and widen their first and last seen.
//
// Then day by day, each in one transaction, the counts: hourly (ads, ads per
// account and brand, scrapes per publisher) and daily (ads, ads per account,
// placements, campaigns). Each day's rows replace whatever Tracks held for it,
// and its hours are marked closed and imported, so the loader never closes
// them again. The collector kept which links and campaigns each creative ran
// with only as running totals, so those become one row per pair, on the last
// day it was seen before the switch-over.
//
// Running it again is safe: every step replaces what the last run wrote.
func ImportOld(ctx context.Context, db, old *pgxpool.Pool, cfg ImportConfig) (Imported, error) {
	res := Imported{Lookups: map[string]int64{}, Publishers: map[string]string{}}
	before := cfg.Before.UTC()
	if !before.Equal(truncDay(before)) {
		return res, fmt.Errorf("import-old: -before %s must be 00:00 UTC, so no day is split between the collector and Tracks", before.Format(time.RFC3339))
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	dst, err := db.Acquire(ctx) // the staging and map tables are temporary: one session
	if err != nil {
		return res, err
	}
	defer dst.Release()
	src, err := old.Acquire(ctx)
	if err != nil {
		return res, err
	}
	defer src.Release()
	// A pooled session may hold an earlier run's tables; leave none behind.
	if _, err := dst.Exec(ctx, `DISCARD TEMP; SET timezone = 'UTC'`); err != nil {
		return res, err
	}
	defer dst.Exec(context.WithoutCancel(ctx), `DISCARD TEMP`)
	if _, err := src.Exec(ctx, `SET timezone = 'UTC'`); err != nil {
		return res, err
	}
	im := &importer{dst: dst.Conn(), src: src.Conn(), before: before, log: log, res: &res}
	if err := im.lookups(ctx); err != nil {
		return res, err
	}

	from := cfg.From.UTC()
	if from.IsZero() {
		var first *time.Time
		if err := im.src.QueryRow(ctx, `SELECT min(day)::timestamptz FROM spy.ad_daily`).Scan(&first); err != nil {
			return res, err
		}
		if first == nil {
			return res, errors.New("import-old: the collector has no daily counts")
		}
		from = *first
	}
	for d := truncDay(from); d.Before(before); d = d.AddDate(0, 0, 1) {
		start := time.Now()
		hours, sightings, err := im.day(ctx, d)
		if err != nil {
			return res, fmt.Errorf("import-old %s: %w", d.Format("2006-01-02"), err)
		}
		res.Days++
		res.Hours += hours
		res.Sightings += sightings
		log.Info("day imported", "day", d.Format("2006-01-02"), "hours", hours, "sightings", sightings,
			"took_ms", time.Since(start).Milliseconds())
	}
	if err := im.creativePairs(ctx); err != nil {
		return res, err
	}
	return res, nil
}

type importer struct {
	dst, src *pgx.Conn
	before   time.Time
	log      *slog.Logger
	res      *Imported
}

// stage copies the rows of query on the collector's database into a new
// temporary table on Tracks' (text COPY both ways, streamed).
func (im *importer) stage(ctx context.Context, table, columns, query string) (int64, error) {
	if _, err := im.dst.Exec(ctx, `DROP TABLE IF EXISTS `+table+`; CREATE TEMP TABLE `+table+` (`+columns+`)`); err != nil {
		return 0, fmt.Errorf("stage %s: %w", table, err)
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := im.src.PgConn().CopyTo(ctx, pw, `COPY (`+query+`) TO STDOUT`)
		pw.CloseWithError(err)
		done <- err
	}()
	tag, err := im.dst.PgConn().CopyFrom(ctx, pr, `COPY `+table+` FROM STDIN`)
	_ = pr.CloseWithError(err)
	if rerr := <-done; rerr != nil && err == nil {
		err = rerr
	}
	if err != nil {
		return 0, fmt.Errorf("stage %s: %w", table, err)
	}
	if _, err := im.dst.Exec(ctx, `ANALYZE `+table); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ts is t as a SQL literal, for COPY queries (which take no parameters).
func ts(t time.Time) string { return "'" + t.UTC().Format(time.RFC3339Nano) + "'::timestamptz" }

// lookups stages the collector's lookups, adds the ones Tracks lacks, and
// builds the map tables (m_*: old_id -> new_id) the counts go through.
func (im *importer) lookups(ctx context.Context) error {
	steps := []struct {
		name, table, columns, query string
		sql                         []string
	}{
		{"networks", "o_network", "id smallint, code text", `SELECT id, code FROM spy.network`, []string{
			`CREATE TEMP TABLE m_network AS SELECT o.id AS old_id, n.id AS new_id FROM o_network o JOIN tracks.network n ON n.code = o.code`,
		}},
		{"devices", "o_device", "id smallint, code text", `SELECT id, code FROM spy.device`, []string{
			`INSERT INTO tracks.device (code) SELECT code FROM o_device ON CONFLICT (code) DO NOTHING`,
			`CREATE TEMP TABLE m_device AS SELECT o.id AS old_id, d.id AS new_id FROM o_device o JOIN tracks.device d ON d.code = o.code`,
		}},
		{"publishers", "o_publisher", "id int, network_id smallint, name text, domain text, created_at timestamptz, aliases text[]", `
			SELECT p.id, p.network_id, p.name, p.domain, p.created_at,
			       COALESCE(array_agg(a.alias ORDER BY a.alias) FILTER (WHERE a.alias IS NOT NULL), '{}')
			FROM spy.publisher p LEFT JOIN spy.publisher_alias a ON a.publisher_id = p.id
			GROUP BY p.id`, []string{
			// A publisher Tracks knows under its name, or under one of the
			// collector's other names for it (targets name pages as the
			// collector's publishers.yaml did), is the same publisher.
			`INSERT INTO tracks.publisher (network_id, name, domain, first_seen_at, last_seen_at)
			 SELECT m.new_id, o.name, o.domain, o.created_at, o.created_at
			 FROM o_publisher o JOIN m_network m ON m.old_id = o.network_id
			 WHERE NOT EXISTS (SELECT 1 FROM tracks.publisher t WHERE t.name = o.name OR t.name = ANY (o.aliases))
			 ON CONFLICT (name) DO NOTHING`,
			`CREATE TEMP TABLE m_publisher AS
			 SELECT o.id AS old_id, o.name AS old_name, t.id AS new_id, t.name AS new_name
			 FROM o_publisher o
			 CROSS JOIN LATERAL (SELECT t.id, t.name FROM tracks.publisher t
			                     WHERE t.name = o.name OR t.name = ANY (o.aliases)
			                     ORDER BY t.name = o.name DESC, t.id LIMIT 1) t`,
		}},
		{"placements", "o_placement", "id int, name text", `SELECT id, name FROM spy.placement`, []string{
			`INSERT INTO tracks.placement (name) SELECT name FROM o_placement ON CONFLICT (name) DO NOTHING`,
			`CREATE TEMP TABLE m_placement AS SELECT o.id AS old_id, t.id AS new_id FROM o_placement o JOIN tracks.placement t ON t.name = o.name`,
		}},
		{"brands", "o_brand", "id int, name text", `SELECT id, name FROM spy.brand`, []string{
			`INSERT INTO tracks.brand (name) SELECT name FROM o_brand ON CONFLICT (name) DO NOTHING`,
			`CREATE TEMP TABLE m_brand AS SELECT o.id AS old_id, t.id AS new_id FROM o_brand o JOIN tracks.brand t ON t.name = o.name`,
		}},
		{"accounts", "o_account", "id int, network_id smallint, external_id text, org_external_id text, first_seen_at timestamptz, last_seen_at timestamptz", `
			SELECT id, network_id, external_id, org_external_id, first_seen_at, last_seen_at FROM spy.account`, []string{
			`INSERT INTO tracks.account AS t (network_id, external_id, org_external_id, first_seen_at, last_seen_at)
			 SELECT m.new_id, o.external_id, o.org_external_id, o.first_seen_at, o.last_seen_at
			 FROM o_account o JOIN m_network m ON m.old_id = o.network_id
			 ON CONFLICT (network_id, external_id) DO UPDATE SET
			     org_external_id = COALESCE(t.org_external_id, EXCLUDED.org_external_id),
			     first_seen_at = LEAST(t.first_seen_at, EXCLUDED.first_seen_at),
			     last_seen_at = GREATEST(t.last_seen_at, EXCLUDED.last_seen_at)`,
			`CREATE TEMP TABLE m_account AS SELECT o.id AS old_id, t.id AS new_id
			 FROM o_account o JOIN m_network m ON m.old_id = o.network_id
			 JOIN tracks.account t ON t.network_id = m.new_id AND t.external_id = o.external_id`,
		}},
		{"campaigns", "o_campaign", "id int, network_id smallint, external_id text, name text, account_id int, parent_external_id text, parent_name text, objective text, first_seen_at timestamptz, last_seen_at timestamptz", `
			SELECT id, network_id, external_id, name, account_id, parent_external_id, parent_name, objective, first_seen_at, last_seen_at
			FROM spy.campaign`, []string{
			`INSERT INTO tracks.campaign AS t (network_id, external_id, name, account_id, parent_external_id, parent_name,
			                                   objective, first_seen_at, last_seen_at)
			 SELECT m.new_id, o.external_id, o.name, a.new_id, o.parent_external_id, o.parent_name, o.objective,
			        o.first_seen_at, o.last_seen_at
			 FROM o_campaign o JOIN m_network m ON m.old_id = o.network_id LEFT JOIN m_account a ON a.old_id = o.account_id
			 ON CONFLICT (network_id, external_id) DO UPDATE SET
			     name = COALESCE(t.name, EXCLUDED.name),
			     account_id = COALESCE(t.account_id, EXCLUDED.account_id),
			     parent_external_id = COALESCE(t.parent_external_id, EXCLUDED.parent_external_id),
			     parent_name = COALESCE(t.parent_name, EXCLUDED.parent_name),
			     objective = COALESCE(t.objective, EXCLUDED.objective),
			     first_seen_at = LEAST(t.first_seen_at, EXCLUDED.first_seen_at),
			     last_seen_at = GREATEST(t.last_seen_at, EXCLUDED.last_seen_at)`,
			`CREATE TEMP TABLE m_campaign AS SELECT o.id AS old_id, t.id AS new_id
			 FROM o_campaign o JOIN m_network m ON m.old_id = o.network_id
			 JOIN tracks.campaign t ON t.network_id = m.new_id AND t.external_id = o.external_id`,
		}},
		{"creatives", "o_creative", "id int, creative_key text, image_url text, format_type text, video_duration int, thumb_dimensions text, language text, first_seen_at timestamptz, last_seen_at timestamptz", `
			SELECT id, creative_key, image_url, format_type, video_duration, thumb_dimensions, language, first_seen_at, last_seen_at
			FROM spy.creative`, []string{
			`INSERT INTO tracks.creative AS t (creative_key, image_url, format_type, video_duration, thumb_dimensions,
			                                   language, first_seen_at, last_seen_at)
			 SELECT creative_key, image_url, format_type, video_duration, thumb_dimensions, language, first_seen_at, last_seen_at
			 FROM o_creative
			 ON CONFLICT (creative_key) DO UPDATE SET
			     format_type = COALESCE(t.format_type, EXCLUDED.format_type),
			     video_duration = COALESCE(t.video_duration, EXCLUDED.video_duration),
			     thumb_dimensions = COALESCE(t.thumb_dimensions, EXCLUDED.thumb_dimensions),
			     language = COALESCE(t.language, EXCLUDED.language),
			     first_seen_at = LEAST(t.first_seen_at, EXCLUDED.first_seen_at),
			     last_seen_at = GREATEST(t.last_seen_at, EXCLUDED.last_seen_at)`,
			`CREATE TEMP TABLE m_creative AS SELECT o.id AS old_id, t.id AS new_id
			 FROM o_creative o JOIN tracks.creative t ON t.creative_key = o.creative_key`,
		}},
		{"ads", "o_ad", "id int, creative_id int, headline text, description text, cta text, brand_id int, account_id int, first_seen_at timestamptz, last_seen_at timestamptz", `
			SELECT id, creative_id, headline, description, cta, brand_id, account_id, first_seen_at, last_seen_at FROM spy.ad`, []string{
			`INSERT INTO tracks.ad AS t (creative_id, headline, description, cta, brand_id, account_id, first_seen_at, last_seen_at)
			 SELECT c.new_id, o.headline, o.description, o.cta, b.new_id, a.new_id, o.first_seen_at, o.last_seen_at
			 FROM o_ad o JOIN m_creative c ON c.old_id = o.creative_id
			 LEFT JOIN m_brand b ON b.old_id = o.brand_id LEFT JOIN m_account a ON a.old_id = o.account_id
			 ON CONFLICT (creative_id, md5(headline)) DO UPDATE SET
			     description = COALESCE(t.description, EXCLUDED.description),
			     cta = COALESCE(t.cta, EXCLUDED.cta),
			     brand_id = COALESCE(t.brand_id, EXCLUDED.brand_id),
			     account_id = COALESCE(t.account_id, EXCLUDED.account_id),
			     first_seen_at = LEAST(t.first_seen_at, EXCLUDED.first_seen_at),
			     last_seen_at = GREATEST(t.last_seen_at, EXCLUDED.last_seen_at)`,
			`CREATE TEMP TABLE m_ad AS SELECT o.id AS old_id, t.id AS new_id
			 FROM o_ad o JOIN m_creative c ON c.old_id = o.creative_id
			 JOIN tracks.ad t ON t.creative_id = c.new_id AND md5(t.headline) = md5(o.headline) AND t.headline = o.headline`,
		}},
		{"links", "o_link", "id int, link_key uuid, host text, path text, item_id text, tracker text, affiliate_network text, params jsonb, sample_url text, first_seen_at timestamptz, last_seen_at timestamptz", `
			SELECT id, link_key, host, path, item_id, tracker, affiliate_network, params, sample_url, first_seen_at, last_seen_at
			FROM spy.link`, []string{
			`INSERT INTO tracks.link AS t (link_key, host, path, item_id, tracker, affiliate_network, params, sample_url,
			                               first_seen_at, last_seen_at)
			 SELECT link_key, host, path, item_id, tracker, affiliate_network, params, sample_url, first_seen_at, last_seen_at
			 FROM o_link
			 ON CONFLICT (link_key) DO UPDATE SET
			     first_seen_at = LEAST(t.first_seen_at, EXCLUDED.first_seen_at),
			     last_seen_at = GREATEST(t.last_seen_at, EXCLUDED.last_seen_at)`,
			`CREATE TEMP TABLE m_link AS SELECT o.id AS old_id, t.id AS new_id
			 FROM o_link o JOIN tracks.link t ON t.link_key = o.link_key`,
		}},
		{"network ads", "o_network_ad", "network_id smallint, external_id text, ad_id int, campaign_id int, creative_external_id text, name text, started_at timestamptz, icon_url text, layout text, media_aspect text, launch_option text, iab_tier1 text, iab_tier2 text, landing_domain text, disclaimer text, extra jsonb, first_seen_at timestamptz, last_seen_at timestamptz", `
			SELECT network_id, external_id, ad_id, campaign_id, creative_external_id, name, started_at, icon_url, layout,
			       media_aspect, launch_option, iab_tier1, iab_tier2, landing_domain, disclaimer, extra, first_seen_at, last_seen_at
			FROM spy.network_ad`, []string{
			`INSERT INTO tracks.network_ad AS t (network_id, external_id, ad_id, campaign_id, creative_external_id, name,
			                                     started_at, icon_url, layout, media_aspect, launch_option, iab_tier1, iab_tier2,
			                                     landing_domain, disclaimer, extra, first_seen_at, last_seen_at)
			 SELECT m.new_id, o.external_id, a.new_id, c.new_id, o.creative_external_id, o.name, o.started_at, o.icon_url,
			        o.layout, o.media_aspect, o.launch_option, o.iab_tier1, o.iab_tier2, o.landing_domain, o.disclaimer,
			        o.extra, o.first_seen_at, o.last_seen_at
			 FROM o_network_ad o JOIN m_network m ON m.old_id = o.network_id JOIN m_ad a ON a.old_id = o.ad_id
			 LEFT JOIN m_campaign c ON c.old_id = o.campaign_id
			 ON CONFLICT (network_id, external_id) DO UPDATE SET
			     first_seen_at = LEAST(t.first_seen_at, EXCLUDED.first_seen_at),
			     last_seen_at = GREATEST(t.last_seen_at, EXCLUDED.last_seen_at)`,
		}},
	}
	for _, s := range steps {
		n, err := im.stage(ctx, s.table, s.columns, s.query)
		if err != nil {
			return err
		}
		for _, q := range s.sql {
			if _, err := im.dst.Exec(ctx, q); err != nil {
				return fmt.Errorf("import-old %s: %w", s.name, err)
			}
		}
		im.res.Lookups[s.name] = n
		im.log.Info("lookups imported", "what", s.name, "rows", n)
	}
	for _, t := range []string{"m_network", "m_device", "m_publisher", "m_placement", "m_brand", "m_account", "m_campaign", "m_creative", "m_ad", "m_link"} {
		if _, err := im.dst.Exec(ctx, `CREATE UNIQUE INDEX ON `+t+` (old_id); ANALYZE `+t); err != nil {
			return fmt.Errorf("import-old %s: %w", t, err)
		}
	}
	rows, err := im.dst.Query(ctx, `SELECT old_name, new_name FROM m_publisher WHERE old_name <> new_name ORDER BY 1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var o, n string
		if err := rows.Scan(&o, &n); err != nil {
			return err
		}
		im.res.Publishers[o] = n
	}
	return rows.Err()
}

// day copies one day's counts in one transaction. It returns the hours
// marked imported and the sightings in them.
func (im *importer) day(ctx context.Context, d time.Time) (int, int64, error) {
	next := d.AddDate(0, 0, 1)
	staged := []struct{ table, columns, query string }{
		{"o_ad_hourly", "hour timestamptz, ad_id int, publisher_id int, device_id smallint, feed_position_min smallint, feed_position_max smallint, sightings int, scrapes int, feed_position_sum bigint", `
			SELECT hour, ad_id, publisher_id, device_id, feed_position_min, feed_position_max, sightings, scrapes, feed_position_sum
			FROM spy.ad_hourly WHERE hour >= ` + ts(d) + ` AND hour < ` + ts(next)},
		{"o_aab_hourly", "hour timestamptz, ad_id int, account_id int, brand_id int, publisher_id int, device_id smallint, sightings int", `
			SELECT hour, ad_id, account_id, brand_id, publisher_id, device_id, sightings
			FROM spy.ad_account_brand_hourly WHERE hour >= ` + ts(d) + ` AND hour < ` + ts(next)},
		{"o_publisher_hourly", "hour timestamptz, publisher_id int, device_id smallint, scrapes int, sightings int", `
			SELECT hour, publisher_id, device_id, scrapes, sightings
			FROM spy.publisher_hourly WHERE hour >= ` + ts(d) + ` AND hour < ` + ts(next)},
		{"o_ad_daily", "ad_id int, publisher_id int, device_id smallint, sightings int, scrapes int, feed_position_sum bigint, first_seen_at timestamptz, last_seen_at timestamptz", `
			SELECT ad_id, publisher_id, device_id, sightings, scrapes, feed_position_sum, first_seen_at, last_seen_at
			FROM spy.ad_daily WHERE day = ` + ts(d) + `::date`},
		{"o_ad_account_daily", "ad_id int, account_id int, brand_id int, publisher_id int, device_id smallint, sightings int, first_seen_at timestamptz, last_seen_at timestamptz", `
			SELECT ad_id, account_id, brand_id, publisher_id, device_id, sightings, first_seen_at, last_seen_at
			FROM spy.ad_account_daily WHERE day = ` + ts(d) + `::date`},
		{"o_placement_daily", "ad_id int, publisher_id int, placement_id int, sightings int", `
			SELECT ad_id, publisher_id, placement_id, sightings FROM spy.placement_daily WHERE day = ` + ts(d) + `::date`},
		{"o_campaign_daily", "campaign_id int, publisher_id int, sightings int, scrapes int, first_seen_at timestamptz, last_seen_at timestamptz", `
			SELECT campaign_id, publisher_id, sightings, scrapes, first_seen_at, last_seen_at
			FROM spy.campaign_daily WHERE day = ` + ts(d) + `::date`},
	}
	for _, s := range staged {
		if _, err := im.stage(ctx, s.table, s.columns, s.query); err != nil {
			return 0, 0, err
		}
	}

	var hours int
	var sightings int64
	err := pgx.BeginFunc(ctx, im.dst, func(tx pgx.Tx) error {
		// The day's counts are the collector's: whatever Tracks held for it
		// (a test capture, an earlier import) goes.
		stmts := []string{
			`SELECT tracks.ensure_month_partitions($1::date, $1::date)`,
			`DELETE FROM tracks.ad_hourly WHERE hour >= $1 AND hour < $2`,
			`DELETE FROM tracks.ad_account_brand_hourly WHERE hour >= $1 AND hour < $2`,
			`DELETE FROM tracks.publisher_hourly WHERE hour >= $1 AND hour < $2`,
			`DELETE FROM tracks.ad_hourly_open WHERE hour >= $1 AND hour < $2`,
			`DELETE FROM tracks.ad_daily WHERE day = $1::date`,
			`DELETE FROM tracks.ad_account_daily WHERE day = $1::date`,
			`DELETE FROM tracks.placement_daily WHERE day = $1::date`,
			`DELETE FROM tracks.campaign_daily WHERE day = $1::date`,
			// The collector's hourly counts have no first and last sighting:
			// the whole hour stands for both.
			`INSERT INTO tracks.ad_hourly (hour, ad_id, publisher_id, device_id, sightings, scrapes, feed_position_sum,
			                               feed_position_min, feed_position_max, first_seen_at, last_seen_at)
			 SELECT o.hour, a.new_id, p.new_id, dv.new_id, sum(o.sightings), sum(o.scrapes), sum(o.feed_position_sum),
			        min(o.feed_position_min), max(o.feed_position_max), o.hour, o.hour + interval '1 hour' - interval '1 microsecond'
			 FROM o_ad_hourly o JOIN m_ad a ON a.old_id = o.ad_id JOIN m_publisher p ON p.old_id = o.publisher_id
			 JOIN m_device dv ON dv.old_id = o.device_id
			 GROUP BY o.hour, a.new_id, p.new_id, dv.new_id`,
			`INSERT INTO tracks.ad_account_brand_hourly (hour, ad_id, account_id, brand_id, publisher_id, device_id, sightings)
			 SELECT o.hour, a.new_id, ac.new_id, b.new_id, p.new_id, dv.new_id, sum(o.sightings)
			 FROM o_aab_hourly o JOIN m_ad a ON a.old_id = o.ad_id JOIN m_publisher p ON p.old_id = o.publisher_id
			 JOIN m_device dv ON dv.old_id = o.device_id
			 LEFT JOIN m_account ac ON ac.old_id = o.account_id LEFT JOIN m_brand b ON b.old_id = o.brand_id
			 GROUP BY o.hour, a.new_id, ac.new_id, b.new_id, p.new_id, dv.new_id`,
			// The collector stored answered scrapes only.
			`INSERT INTO tracks.publisher_hourly (hour, publisher_id, device_id, scrapes, answered, failed, sightings)
			 SELECT o.hour, p.new_id, dv.new_id, sum(o.scrapes), sum(o.scrapes), 0, sum(o.sightings)
			 FROM o_publisher_hourly o JOIN m_publisher p ON p.old_id = o.publisher_id JOIN m_device dv ON dv.old_id = o.device_id
			 GROUP BY o.hour, p.new_id, dv.new_id`,
			`INSERT INTO tracks.ad_daily (day, ad_id, publisher_id, device_id, creative_id, sightings, scrapes,
			                              feed_position_sum, first_seen_at, last_seen_at)
			 SELECT $1::date, a.new_id, p.new_id, dv.new_id, t.creative_id, sum(o.sightings), sum(o.scrapes),
			        sum(o.feed_position_sum), min(o.first_seen_at), max(o.last_seen_at)
			 FROM o_ad_daily o JOIN m_ad a ON a.old_id = o.ad_id JOIN tracks.ad t ON t.id = a.new_id
			 JOIN m_publisher p ON p.old_id = o.publisher_id JOIN m_device dv ON dv.old_id = o.device_id
			 GROUP BY a.new_id, p.new_id, dv.new_id, t.creative_id`,
			`INSERT INTO tracks.ad_account_daily (day, ad_id, account_id, brand_id, publisher_id, device_id, creative_id,
			                                      sightings, first_seen_at, last_seen_at)
			 SELECT $1::date, a.new_id, ac.new_id, b.new_id, p.new_id, dv.new_id, t.creative_id, sum(o.sightings),
			        min(o.first_seen_at), max(o.last_seen_at)
			 FROM o_ad_account_daily o JOIN m_ad a ON a.old_id = o.ad_id JOIN tracks.ad t ON t.id = a.new_id
			 JOIN m_publisher p ON p.old_id = o.publisher_id JOIN m_device dv ON dv.old_id = o.device_id
			 LEFT JOIN m_account ac ON ac.old_id = o.account_id LEFT JOIN m_brand b ON b.old_id = o.brand_id
			 GROUP BY a.new_id, ac.new_id, b.new_id, p.new_id, dv.new_id, t.creative_id`,
			`INSERT INTO tracks.placement_daily (day, ad_id, publisher_id, placement_id, sightings)
			 SELECT $1::date, a.new_id, p.new_id, pl.new_id, sum(o.sightings)
			 FROM o_placement_daily o JOIN m_ad a ON a.old_id = o.ad_id JOIN m_publisher p ON p.old_id = o.publisher_id
			 JOIN m_placement pl ON pl.old_id = o.placement_id
			 GROUP BY a.new_id, p.new_id, pl.new_id`,
			`INSERT INTO tracks.campaign_daily (day, campaign_id, publisher_id, sightings, scrapes, first_seen_at, last_seen_at)
			 SELECT $1::date, c.new_id, p.new_id, sum(o.sightings), sum(o.scrapes), min(o.first_seen_at), max(o.last_seen_at)
			 FROM o_campaign_daily o JOIN m_campaign c ON c.old_id = o.campaign_id JOIN m_publisher p ON p.old_id = o.publisher_id
			 GROUP BY c.new_id, p.new_id`,
		}
		for _, q := range stmts {
			var args []any
			if strings.Contains(q, "$1") {
				args = append(args, d)
			}
			if strings.Contains(q, "$2") {
				args = append(args, next)
			}
			if _, err := tx.Exec(ctx, q, args...); err != nil {
				return fmt.Errorf("%.60s…: %w", q, err)
			}
		}
		// Every hour of the day with the collector's counts is closed and
		// imported: the loader never closes it again, and a raw file for it
		// is refused. Hours the collector has nothing for are left alone.
		return tx.QueryRow(ctx, `
			WITH h AS (
				SELECT hour, sum(scrapes) AS scrapes,
				       (SELECT COALESCE(sum(a.sightings), 0) FROM tracks.ad_hourly a WHERE a.hour = p.hour) AS sightings
				FROM tracks.publisher_hourly p WHERE hour >= $1 AND hour < $2 GROUP BY hour
			), up AS (
				INSERT INTO tracks.hour_state AS s (hour, dirty, closed_at, closes, scrapes, sightings, imported_at)
				SELECT hour, FALSE, now(), 0, scrapes, sightings, now() FROM h
				ON CONFLICT (hour) DO UPDATE SET dirty = FALSE, closed_at = now(), scrapes = EXCLUDED.scrapes,
				    sightings = EXCLUDED.sightings, imported_at = now()
				RETURNING sightings
			)
			SELECT count(*), COALESCE(sum(sightings), 0) FROM up`, d, next).Scan(&hours, &sightings)
	})
	return hours, sightings, err
}

// creativePairs copies which links and campaigns each creative ran with.
// The collector kept only running totals per pair, so each pair becomes one
// row on the last day it was seen before the switch-over, with its total
// and first and last seen. What Tracks holds for days before the switch-over
// is replaced.
func (im *importer) creativePairs(ctx context.Context) error {
	last := ts(im.before.Add(-time.Microsecond))
	for _, p := range []struct{ old, col, table, m string }{
		{"creative_link", "link_id", "creative_link_daily", "m_link"},
		{"creative_campaign", "campaign_id", "creative_campaign_daily", "m_campaign"},
	} {
		n, err := im.stage(ctx, "o_"+p.old, "creative_id int, other_id int, sightings bigint, first_seen_at timestamptz, last_seen_at timestamptz", `
			SELECT creative_id, `+p.col+`, sightings, first_seen_at, LEAST(last_seen_at, `+last+`)
			FROM spy.`+p.old+` WHERE first_seen_at < `+ts(im.before))
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, im.dst, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM tracks.`+p.table+` WHERE day < $1::date`, im.before); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO tracks.`+p.table+` (day, creative_id, `+p.col+`, sightings, first_seen_at, last_seen_at)
				SELECT (max(o.last_seen_at) AT TIME ZONE 'UTC')::date, c.new_id, x.new_id,
				       LEAST(sum(o.sightings), 2147483647)::int, min(o.first_seen_at), max(o.last_seen_at)
				FROM o_`+p.old+` o JOIN m_creative c ON c.old_id = o.creative_id JOIN `+p.m+` x ON x.old_id = o.other_id
				GROUP BY c.new_id, x.new_id`)
			return err
		})
		if err != nil {
			return fmt.Errorf("import-old %s: %w", p.old, err)
		}
		im.res.Lookups[p.old] = n
		im.log.Info("creative pairs imported", "what", p.old, "rows", n)
	}
	return nil
}
