-- Size counts a subject's sightings inside its market only (its vertical, or
-- the network for verticals and ads without one). An operator's market is the
-- vertical most of its first ads had, but its sightings counted every
-- vertical: an operator big elsewhere took more than 100% of a small vertical,
-- and past 1000% share_24h_pct overflowed NUMERIC(7, 4). The whole Direction
-- job failed with it, every 5 minutes, until the numbers moved (25 times on 1
-- Oct 2026, 07:00 to 09:03 UTC). A creative whose ads changed vertical had
-- the same problem on a smaller scale. Now each subject counts the sightings of
-- its ads that are members of its market, so subjects of one kind split the
-- market between them, every share is at most 100 and the shares of those
-- ranked above add up to at most 100. A subject with no sightings in its
-- market has no size.
CREATE OR REPLACE FUNCTION spy.refresh_size(p_now TIMESTAMPTZ) RETURNS INTEGER
LANGUAGE plpgsql AS $$
DECLARE
    cfg JSONB := spy.cfg();
    today DATE := (p_now AT TIME ZONE 'UTC')::date;
    cur_hour TIMESTAMPTZ := date_trunc('hour', p_now, 'UTC');
    n INTEGER;
BEGIN
    CREATE TEMP TABLE IF NOT EXISTS spy_size_new (LIKE spy.size_stats) ON COMMIT DROP;
    TRUNCATE spy_size_new;

    INSERT INTO spy_size_new (kind, key, market_kind, market_key, sightings_24h, share_24h_pct, rank_24h,
                              sightings_7d, share_7d_pct, rank_7d, share_7d_before_pct, scaled, refreshed_at)
    WITH d7 AS (
        SELECT ad_id, sum(sightings) AS s FROM tracks_api.ad_daily_v1 WHERE day > today - 7 GROUP BY 1
    ),
    h24 AS (
        SELECT ad_id, sum(sightings) AS s FROM tracks_api.ad_hourly_v1 WHERE hour > cur_hour - interval '24 hours' GROUP BY 1
    ),
    per_ad AS (
        SELECT COALESCE(d7.ad_id, h24.ad_id) AS ad_id, COALESCE(d7.s, 0) AS s7, COALESCE(h24.s, 0) AS s24
        FROM d7 FULL JOIN h24 ON h24.ad_id = d7.ad_id
    ),
    -- Each market's sightings: all its members'.
    market AS (
        SELECT m.kind, m.key, sum(p.s7) AS s7, sum(p.s24) AS s24
        FROM per_ad p JOIN spy.direction_member m ON m.ad_id = p.ad_id
        WHERE m.kind IN ('vertical', 'network')
        GROUP BY 1, 2
    ),
    -- Each subject's sightings inside its market: its ads that are members
    -- of the market too.
    per AS (
        SELECT m.kind, m.key, s.market_kind, s.market_key, sum(p.s7) AS s7, sum(p.s24) AS s24
        FROM per_ad p
        JOIN spy.direction_member m ON m.ad_id = p.ad_id
        JOIN spy.direction_subject s ON s.kind = m.kind AND s.key = m.key
        JOIN spy.direction_member mm ON mm.ad_id = p.ad_id AND mm.kind = s.market_kind AND mm.key = s.market_key
        WHERE m.kind <> 'network'
        GROUP BY 1, 2, 3, 4
    ),
    withm AS (
        SELECT p.kind, p.key, p.market_kind, p.market_key, p.s7, p.s24,
               GREATEST(mk.s7, 1) AS m7, GREATEST(mk.s24, 1) AS m24
        FROM per p
        JOIN market mk ON mk.kind = p.market_kind AND mk.key = p.market_key
    ),
    ranked AS (
        SELECT w.*,
               CASE WHEN s24 > 0 THEN rank() OVER (PARTITION BY kind, market_kind, market_key ORDER BY s24 DESC) END AS r24,
               CASE WHEN s7 > 0 THEN rank() OVER (PARTITION BY kind, market_kind, market_key ORDER BY s7 DESC) END AS r7,
               COALESCE(sum(s7) OVER (PARTITION BY kind, market_kind, market_key ORDER BY s7 DESC, key
                                      ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING), 0) AS before7
        FROM withm w
    )
    SELECT kind, key, market_kind, market_key,
           s24, round(100.0 * s24 / m24, 4), r24,
           s7, round(100.0 * s7 / m7, 4), r7, round(100.0 * before7 / m7, 4),
           s7 >= (cfg->>'scaled_min_sightings_7d')::numeric
               AND r7 <= (cfg->>'scaled_max_rank')::numeric
               AND 100.0 * before7 / m7 < (cfg->>'scaled_cum_share_pct')::numeric,
           p_now
    FROM ranked;

    DELETE FROM spy.size_stats d
    WHERE NOT EXISTS (SELECT 1 FROM spy_size_new s WHERE s.kind = d.kind AND s.key = d.key);
    INSERT INTO spy.size_stats AS d (kind, key, market_kind, market_key, sightings_24h, share_24h_pct, rank_24h,
                                     sightings_7d, share_7d_pct, rank_7d, share_7d_before_pct, scaled, refreshed_at)
    SELECT kind, key, market_kind, market_key, sightings_24h, share_24h_pct, rank_24h,
           sightings_7d, share_7d_pct, rank_7d, share_7d_before_pct, scaled, refreshed_at
    FROM spy_size_new
    ON CONFLICT (kind, key) DO UPDATE SET
        market_kind = EXCLUDED.market_kind,
        market_key = EXCLUDED.market_key,
        sightings_24h = EXCLUDED.sightings_24h,
        share_24h_pct = EXCLUDED.share_24h_pct,
        rank_24h = EXCLUDED.rank_24h,
        sightings_7d = EXCLUDED.sightings_7d,
        share_7d_pct = EXCLUDED.share_7d_pct,
        rank_7d = EXCLUDED.rank_7d,
        share_7d_before_pct = EXCLUDED.share_7d_before_pct,
        scaled = EXCLUDED.scaled,
        refreshed_at = EXCLUDED.refreshed_at;
    GET DIAGNOSTICS n = ROW_COUNT;

    INSERT INTO spy.direction_mark (job, done_at) VALUES ('size', p_now)
    ON CONFLICT (job) DO UPDATE SET done_at = EXCLUDED.done_at;
    RETURN n;
END;
$$;
