package importold

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Prices says what the copy of the collector's prices did. Auctions counts
// the rows of its Taboola auction log; NewsBreak counts its days of one ad
// on one publisher and device with a bid or second price. Copied ones were
// matched to an ad, publisher and device here. Rows is how many daily rows
// they made in spy.price_day; Kept is how many of those Spy already had
// from Tracks, which stand.
type Prices struct {
	Auctions  Counts
	NewsBreak Counts
	Rows      int
	Kept      int
	// Why auctions were skipped: no creative here for the auction's item
	// or campaign, no publisher here for its domain, or a device not known.
	NoCreative, NoPublisher, NoDevice int
	// How the copied ones found their creative: by the campaign item
	// (exact) or by the campaign (its most seen creative).
	ByItem, ByCampaign int
}

var priceParts = []part{
	{"old_publisher", "id INTEGER, name TEXT, domain TEXT, aliases TEXT[]", `
		SELECT p.id, p.name, regexp_replace(lower(p.domain), '^www[.]', ''), COALESCE(array_agg(a.alias) FILTER (WHERE a.alias IS NOT NULL), '{}')
		FROM spy.publisher p LEFT JOIN spy.publisher_alias a ON a.publisher_id = p.id
		GROUP BY p.id`},
	// The collector logged each Taboola card with auction values, but not
	// which ad it was: raw_item_id is the campaign item id from the click
	// link (else the card's own id), campaign_id the campaign id from it.
	// It kept no RTB flag either; its winning seat says RTB when the card
	// named a competing seat or "taboola-rtb".
	{"old_auction", "day DATE, item_id TEXT, campaign_id TEXT, domain TEXT, device TEXT, clearing DOUBLE PRECISION, bid DOUBLE PRECISION, cap DOUBLE PRECISION, rtb BOOLEAN", `
		SELECT (intercepted_at AT TIME ZONE 'UTC')::date, NULLIF(raw_item_id, ''), NULLIF(campaign_id, ''),
		       regexp_replace(lower(publisher_domain), '^www[.]', ''), lower(COALESCE(NULLIF(device, ''), 'desktop')),
		       clearing_price::float8, bid_value::float8, cap_auction_price::float8,
		       COALESCE(winning_seat ~* '(googleadx|rtb|seat)', FALSE)
		FROM public.adhunters_rtb_auction_log`},
}

// oldNewsBreak sums one UTC day of the collector's sightings with a
// NewsBreak bid or second price, per ad, publisher and device. One day at a
// time, so each read stays within one daily partition.
var oldNewsBreak = part{"old_nb_price",
	"day DATE, creative_key TEXT, headline TEXT, publisher_id INTEGER, device TEXT, bid_n INTEGER, bid_sum DOUBLE PRECISION, " +
		"bid_p50 DOUBLE PRECISION, second_n INTEGER, second_sum DOUBLE PRECISION, second_p50 DOUBLE PRECISION", `
	SELECT $1::date, c.creative_key, a.headline, s.publisher_id, d.code, s.bid_n, s.bid_sum, s.bid_p50,
	       s.second_n, s.second_sum, s.second_p50
	FROM (SELECT ad_id, publisher_id, device_id,
	             count(bid_price)::int AS bid_n, COALESCE(sum(bid_price), 0)::float8 AS bid_sum,
	             percentile_cont(0.5) WITHIN GROUP (ORDER BY bid_price) AS bid_p50,
	             count(second_price)::int AS second_n, COALESCE(sum(second_price), 0)::float8 AS second_sum,
	             percentile_cont(0.5) WITHIN GROUP (ORDER BY second_price) AS second_p50
	      FROM spy.sighting
	      WHERE seen_at >= $1::date::timestamp AT TIME ZONE 'UTC' AND seen_at < ($1::date + 1)::timestamp AT TIME ZONE 'UTC'
	        AND (bid_price IS NOT NULL OR second_price IS NOT NULL)
	      GROUP BY 1, 2, 3) s
	JOIN spy.ad a ON a.id = s.ad_id
	JOIN spy.creative c ON c.id = a.creative_id
	JOIN spy.device d ON d.id = s.device_id`}

// copyPrices copies the collector's auction prices into spy.price_day, in
// its own transaction. The copied rows are marked imported and replaced on
// each run. Where Spy already has a row of its own (an ad on a publisher
// and device, one day) from Tracks, Spy's stands, and spy.refresh_prices
// replaces a copied row when Tracks has one for the same day.
//
// A Taboola auction reaches an ad through its campaign item: the link Tracks
// knows with that item id, the creative that link was clicked from most,
// and that creative's most seen ad that day on that publisher and device
// (else its most seen ad). Prices per creative are exact; a creative that
// ran several headlines puts its prices on its leading ad. An auction whose
// item Tracks does not know goes to its campaign's most seen creative (a
// campaign's creatives share its bid), and the count of each way is kept.
func copyPrices(ctx context.Context, old *pgxpool.Pool, db *pgxpool.Pool) (Prices, error) {
	var p Prices
	days, err := readDays(ctx, old)
	if err != nil {
		return p, fmt.Errorf("old sighting days: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	read := map[string]int{}
	for _, pt := range append(priceParts, oldNewsBreak) {
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE `+pt.table+` (`+pt.cols+`) ON COMMIT DROP`); err != nil {
			return p, err
		}
	}
	for _, pt := range priceParts {
		n, err := stream(ctx, old, tx, pt)
		if err != nil {
			return p, fmt.Errorf("%s: %w", pt.table, err)
		}
		read[pt.table] = int(n)
	}
	for _, d := range days {
		n, err := stream(ctx, old, tx, oldNewsBreak, d)
		if err != nil {
			return p, fmt.Errorf("old NewsBreak prices of %s: %w", d.Format(time.DateOnly), err)
		}
		read[oldNewsBreak.table] += int(n)
	}

	steps := []string{
		`ANALYZE old_auction`,
		// The collector's publishers, as Tracks' import-old matched them:
		// by name, or by one of the collector's other names for it.
		`CREATE TEMP TABLE m_old_pub ON COMMIT DROP AS
		 SELECT o.id AS old_id, t.id AS new_id
		 FROM old_publisher o
		 CROSS JOIN LATERAL (SELECT t.id FROM tracks_api.publisher_v1 t
		                     WHERE t.name = o.name OR t.name = ANY (o.aliases)
		                     ORDER BY t.name = o.name DESC, t.id LIMIT 1) t`,
		// An auction names its publisher's domain (without www.): a Taboola
		// publisher here by that name or domain, else the one the collector's
		// publisher with that name, domain or alias became. Two publishers on
		// one domain (NBC News and NBC News Select) cannot be told apart: the
		// first one takes the domain's auctions.
		`CREATE TEMP TABLE m_domain ON COMMIT DROP AS
		 SELECT d.domain, COALESCE(
		     (SELECT t.id FROM tracks_api.publisher_v1 t JOIN tracks_api.network_v1 nw ON nw.id = t.network_id
		      WHERE nw.code = 'taboola'
		        AND (lower(t.name) = d.domain OR regexp_replace(lower(t.domain), '^www[.]', '') = d.domain)
		      ORDER BY t.id LIMIT 1),
		     (SELECT m.new_id FROM old_publisher o JOIN m_old_pub m ON m.old_id = o.id
		      WHERE lower(o.name) = d.domain OR o.domain = d.domain
		         OR d.domain = ANY (SELECT regexp_replace(lower(x), '^www[.]', '') FROM unnest(o.aliases) x)
		      ORDER BY o.id LIMIT 1)) AS publisher_id
		 FROM (SELECT DISTINCT domain FROM old_auction) d`,
		// The creative: the one most clicked through the Tracks link with the
		// auction's campaign item id; else its campaign's most seen creative.
		`CREATE TEMP TABLE m_item ON COMMIT DROP AS
		 SELECT DISTINCT ON (l.item_id) l.item_id, cl.creative_id
		 FROM tracks_api.link_v1 l JOIN tracks_api.creative_link_daily_v1 cl ON cl.link_id = l.id
		 WHERE l.item_id IN (SELECT DISTINCT item_id FROM old_auction)
		 GROUP BY l.item_id, cl.creative_id
		 ORDER BY l.item_id, sum(cl.sightings) DESC, cl.creative_id`,
		`CREATE TEMP TABLE m_campaign ON COMMIT DROP AS
		 SELECT DISTINCT ON (c.external_id) c.external_id, cc.creative_id
		 FROM tracks_api.campaign_v1 c
		 JOIN tracks_api.network_v1 nw ON nw.id = c.network_id AND nw.code = 'taboola'
		 JOIN tracks_api.creative_campaign_daily_v1 cc ON cc.campaign_id = c.id
		 WHERE c.external_id IN (SELECT DISTINCT campaign_id FROM old_auction)
		 GROUP BY c.external_id, cc.creative_id
		 ORDER BY c.external_id, sum(cc.sightings) DESC, cc.creative_id`,
		`CREATE TEMP TABLE old_mapped ON COMMIT DROP AS
		 SELECT a.*, COALESCE(i.creative_id, k.creative_id) AS creative_id, i.creative_id IS NOT NULL AS by_item,
		        m.publisher_id, dv.id AS device_id
		 FROM old_auction a
		 LEFT JOIN m_item i ON i.item_id = a.item_id
		 LEFT JOIN m_campaign k ON k.external_id = a.campaign_id
		 LEFT JOIN m_domain m ON m.domain = a.domain
		 LEFT JOIN tracks_api.device_v1 dv ON dv.code = CASE WHEN a.device = 'mobile' THEN 'phone' ELSE a.device END`,
		`CREATE TEMP TABLE old_tab ON COMMIT DROP AS
		 SELECT a.day, a.creative_id, a.publisher_id, a.device_id,
		        count(*) AS auctions, count(*) FILTER (WHERE a.rtb) AS rtb,
		        count(a.clearing) AS clearing_n, COALESCE(sum(a.clearing), 0) AS clearing_sum,
		        percentile_cont(0.25) WITHIN GROUP (ORDER BY a.clearing) AS clearing_p25,
		        percentile_cont(0.5) WITHIN GROUP (ORDER BY a.clearing) AS clearing_p50,
		        percentile_cont(0.75) WITHIN GROUP (ORDER BY a.clearing) AS clearing_p75,
		        count(a.bid) AS bid_n, COALESCE(sum(a.bid), 0) AS bid_sum,
		        percentile_cont(0.5) WITHIN GROUP (ORDER BY a.bid) AS bid_p50,
		        percentile_cont(0.5) WITHIN GROUP (ORDER BY a.cap) AS cap_p50
		 FROM old_mapped a
		 WHERE a.creative_id IS NOT NULL AND a.publisher_id IS NOT NULL AND a.device_id IS NOT NULL
		 GROUP BY 1, 2, 3, 4`,
		`CREATE TEMP TABLE m_day_ad ON COMMIT DROP AS
		 SELECT DISTINCT ON (d.day, d.creative_id, d.publisher_id, d.device_id)
		        d.day, d.creative_id, d.publisher_id, d.device_id, d.ad_id
		 FROM tracks_api.ad_daily_v1 d
		 WHERE d.creative_id IN (SELECT DISTINCT creative_id FROM old_tab)
		   AND d.day >= (SELECT min(day) FROM old_tab)
		 ORDER BY d.day, d.creative_id, d.publisher_id, d.device_id, d.sightings DESC, d.ad_id`,
		`CREATE TEMP TABLE m_lead_ad ON COMMIT DROP AS
		 SELECT DISTINCT ON (ad.creative_id) ad.creative_id, ad.id AS ad_id
		 FROM tracks_api.ad_v1 ad
		 LEFT JOIN (SELECT ad_id, sum(sightings) AS n FROM tracks_api.ad_daily_v1
		            WHERE creative_id IN (SELECT DISTINCT creative_id FROM old_tab) GROUP BY 1) s ON s.ad_id = ad.id
		 WHERE ad.creative_id IN (SELECT DISTINCT creative_id FROM old_tab)
		 ORDER BY ad.creative_id, s.n DESC NULLS LAST, ad.id`,
		// NewsBreak: an ad by its creative key and headline. Two of the
		// collector's publishers that became one here are summed; their
		// medians are weighted.
		`CREATE TEMP TABLE old_nb ON COMMIT DROP AS
		 SELECT o.day, ad.id AS ad_id, m.new_id AS publisher_id, dv.id AS device_id,
		        sum(o.bid_n) AS bid_n, sum(o.bid_sum) AS bid_sum,
		        sum(o.bid_p50 * o.bid_n) / NULLIF(sum(o.bid_n) FILTER (WHERE o.bid_p50 IS NOT NULL), 0) AS bid_p50,
		        sum(o.second_n) AS second_n, sum(o.second_sum) AS second_sum,
		        sum(o.second_p50 * o.second_n) / NULLIF(sum(o.second_n) FILTER (WHERE o.second_p50 IS NOT NULL), 0) AS second_p50
		 FROM old_nb_price o
		 JOIN tracks_api.creative_v1 c ON c.creative_key = o.creative_key
		 JOIN tracks_api.ad_v1 ad ON ad.creative_id = c.id AND ad.headline = o.headline
		 JOIN m_old_pub m ON m.old_id = o.publisher_id
		 JOIN tracks_api.device_v1 dv ON dv.code = o.device
		 GROUP BY 1, 2, 3, 4`,
		`DELETE FROM spy.price_day WHERE imported`,
	}
	for _, q := range steps {
		if _, err := tx.Exec(ctx, q); err != nil {
			return p, fmt.Errorf("prices: %w", err)
		}
	}

	if err := tx.QueryRow(ctx, `
		SELECT COALESCE((SELECT sum(auctions) FROM old_tab t
		                 WHERE EXISTS (SELECT 1 FROM m_lead_ad l WHERE l.creative_id = t.creative_id)), 0)::int,
		       (SELECT count(*) FROM old_nb_price o
		        WHERE EXISTS (SELECT 1 FROM tracks_api.creative_v1 c JOIN tracks_api.ad_v1 ad ON ad.creative_id = c.id
		                      WHERE c.creative_key = o.creative_key AND ad.headline = o.headline)
		          AND EXISTS (SELECT 1 FROM m_old_pub m WHERE m.old_id = o.publisher_id)
		          AND EXISTS (SELECT 1 FROM tracks_api.device_v1 dv WHERE dv.code = o.device))::int`).
		Scan(&p.Auctions.Copied, &p.NewsBreak.Copied); err != nil {
		return p, fmt.Errorf("prices: %w", err)
	}
	p.Auctions.Skipped = read["old_auction"] - p.Auctions.Copied
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE creative_id IS NULL), count(*) FILTER (WHERE publisher_id IS NULL),
		       count(*) FILTER (WHERE device_id IS NULL),
		       count(*) FILTER (WHERE by_item AND publisher_id IS NOT NULL AND device_id IS NOT NULL),
		       count(*) FILTER (WHERE NOT by_item AND creative_id IS NOT NULL AND publisher_id IS NOT NULL AND device_id IS NOT NULL)
		FROM old_mapped`).Scan(&p.NoCreative, &p.NoPublisher, &p.NoDevice, &p.ByItem, &p.ByCampaign); err != nil {
		return p, fmt.Errorf("prices: %w", err)
	}
	p.NewsBreak.Skipped = read["old_nb_price"] - p.NewsBreak.Copied

	if err := tx.QueryRow(ctx, `
		WITH tab AS (
		    SELECT t.*, COALESCE(d.ad_id, l.ad_id) AS ad_id
		    FROM old_tab t
		    LEFT JOIN m_day_ad d ON d.day = t.day AND d.creative_id = t.creative_id
		                        AND d.publisher_id = t.publisher_id AND d.device_id = t.device_id
		    LEFT JOIN m_lead_ad l ON l.creative_id = t.creative_id),
		merged AS (
		    SELECT COALESCE(t.day, b.day) AS day, COALESCE(t.ad_id, b.ad_id) AS ad_id,
		           COALESCE(t.publisher_id, b.publisher_id) AS publisher_id, COALESCE(t.device_id, b.device_id) AS device_id,
		           COALESCE(t.auctions, 0) AS auctions, COALESCE(t.rtb, 0) AS rtb,
		           COALESCE(t.clearing_n, 0) AS clearing_n, COALESCE(t.clearing_sum, 0) AS clearing_sum,
		           t.clearing_p25, t.clearing_p50, t.clearing_p75,
		           COALESCE(t.bid_n, 0) + COALESCE(b.bid_n, 0) AS bid_n, COALESCE(t.bid_sum, 0) + COALESCE(b.bid_sum, 0) AS bid_sum,
		           COALESCE(t.bid_p50, b.bid_p50) AS bid_p50, t.cap_p50,
		           COALESCE(b.second_n, 0) AS second_n, COALESCE(b.second_sum, 0) AS second_sum, b.second_p50
		    FROM (SELECT * FROM tab WHERE ad_id IS NOT NULL) t
		    FULL JOIN old_nb b ON b.day = t.day AND b.ad_id = t.ad_id AND b.publisher_id = t.publisher_id AND b.device_id = t.device_id),
		ins AS (
		    INSERT INTO spy.price_day (day, ad_id, creative_id, publisher_id, device_id, auctions, rtb,
		                               clearing_n, clearing_sum, clearing_p25, clearing_p50, clearing_p75,
		                               bid_n, bid_sum, bid_p50, cap_p50, second_n, second_sum, second_p50, imported)
		    SELECT r.day, r.ad_id, ad.creative_id, r.publisher_id, r.device_id, r.auctions, r.rtb,
		           r.clearing_n, r.clearing_sum, r.clearing_p25, r.clearing_p50, r.clearing_p75,
		           r.bid_n, r.bid_sum, r.bid_p50, r.cap_p50, r.second_n, r.second_sum, r.second_p50, TRUE
		    FROM merged r JOIN tracks_api.ad_v1 ad ON ad.id = r.ad_id
		    ON CONFLICT (day, ad_id, publisher_id, device_id) DO NOTHING
		    RETURNING 1)
		SELECT (SELECT count(*) FROM ins)::int, (SELECT count(*) FROM merged)::int`).Scan(&p.Rows, &p.Kept); err != nil {
		return p, fmt.Errorf("prices: %w", err)
	}
	p.Kept -= p.Rows
	for what, c := range map[string]Counts{"auction_prices": p.Auctions, "newsbreak_prices": p.NewsBreak} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO spy.import_mark (what, done_at, copied, skipped) VALUES ($1, now(), $2, $3)
			ON CONFLICT (what) DO UPDATE SET done_at = now(), copied = $2, skipped = $3`, what, c.Copied, c.Skipped); err != nil {
			return p, err
		}
	}
	return p, tx.Commit(ctx)
}

// readDays lists the days the collector still keeps sightings of: one
// partition of spy.sighting per UTC day.
func readDays(ctx context.Context, old *pgxpool.Pool) ([]time.Time, error) {
	rows, err := old.Query(ctx, `
		SELECT to_date(substring(c.relname FROM '^sighting_([0-9]{8})$'), 'YYYYMMDD')
		FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = to_regclass('spy.sighting') AND c.relname ~ '^sighting_[0-9]{8}$'
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[time.Time])
}
