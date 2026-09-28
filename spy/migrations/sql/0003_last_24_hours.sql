-- lint: new-table
-- The last 24 hours: the lists count the 24 hours up to the last closed hour
-- and compare them with the 24 hours before. Ported from the collector's
-- spy.refresh_recent() (032, 034), reading tracks_api.
--
-- A window is 24 hours ending at the same clock hour, so every window holds
-- every hour of the day once. Counts are raw sightings. Rates are sightings
-- per 1,000 scrapes: each publisher and device gets its own rate in the
-- window, and the rates are averaged with equal weight, so a publisher that
-- was scraped ten times more does not read as an ad scaling.
--
-- The windows end where Tracks' closed hours stop: at the first hour since
-- the oldest closed hour of the last 15 days that is not closed yet. They
-- are rebuilt when that moves, or when an hour inside them closes again
-- (a late raw file or a replay).

CREATE TABLE spy.recent_window (
    one BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (one),
    window_end TIMESTAMPTZ NOT NULL,             -- every window ends here
    network_sightings_24h BIGINT NOT NULL,       -- all sightings in the 24 hours before window_end
    refreshed_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE spy.creative_recent (
    creative_id INTEGER PRIMARY KEY,
    sightings_24h INTEGER NOT NULL,
    sightings_prev_24h INTEGER NOT NULL,         -- the 24 hours before those
    rate_24h DOUBLE PRECISION NOT NULL,          -- sightings per 1,000 scrapes
    rate_prev_24h DOUBLE PRECISION NOT NULL,
    peak_end TIMESTAMPTZ,                        -- end of its highest-rate 24 hours in the last 14 days
    peak_sightings INTEGER,
    peak_rate DOUBLE PRECISION
);

CREATE TABLE spy.operator_recent (
    operator_id INTEGER PRIMARY KEY REFERENCES spy.operator(id) ON DELETE CASCADE,
    sightings_24h INTEGER NOT NULL,
    sightings_prev_24h INTEGER NOT NULL,
    rate_24h DOUBLE PRECISION NOT NULL,
    rate_prev_24h DOUBLE PRECISION NOT NULL
);

-- Where the windows end as of p_now: the first hour not closed yet, counting
-- from the oldest closed hour of the last 15 days. NULL before any closed hour.
CREATE FUNCTION spy.recent_end(p_now TIMESTAMPTZ) RETURNS TIMESTAMPTZ
LANGUAGE plpgsql STABLE AS $$
DECLARE
    cur TIMESTAMPTZ := date_trunc('hour', p_now, 'UTC');
    first_closed TIMESTAMPTZ;
BEGIN
    SELECT min(hour) INTO first_closed FROM tracks_api.closed_hour_v1
    WHERE hour >= cur - interval '15 days' AND hour < cur;
    IF first_closed IS NULL THEN
        RETURN NULL;
    END IF;
    RETURN (SELECT min(g) FROM generate_series(first_closed, cur, interval '1 hour') g
            WHERE NOT EXISTS (SELECT 1 FROM tracks_api.closed_hour_v1 c WHERE c.hour = g));
END;
$$;

-- Returns the creatives written, or 0 when the windows are already current or
-- another refresh is running.
CREATE FUNCTION spy.refresh_recent(p_now TIMESTAMPTZ DEFAULT now()) RETURNS BIGINT
LANGUAGE plpgsql AS $$
DECLARE
    v_end TIMESTAMPTZ := spy.recent_end(p_now);
    v_stable TIMESTAMPTZ := COALESCE((spy.cfg()->>'recent_from')::timestamptz, '-infinity');
    v_rows BIGINT;
BEGIN
    IF v_end IS NULL THEN
        RETURN 0;
    END IF;
    -- Current: same end, and no hour of the 14 days closed again since.
    IF EXISTS (SELECT 1 FROM spy.recent_window w
               WHERE w.window_end = v_end
                 AND NOT EXISTS (SELECT 1 FROM tracks_api.closed_hour_v1 c
                                 WHERE c.hour >= v_end - interval '14 days' AND c.hour < v_end
                                   AND c.closed_at > w.refreshed_at)) THEN
        RETURN 0;
    END IF;
    IF NOT pg_try_advisory_xact_lock(hashtext('spy.refresh_recent')) THEN
        RETURN 0;
    END IF;

    -- One row per creative, operator and window. k = 1 is the last 24 hours,
    -- k = 2 the 24 before, up to 14 days back.
    CREATE TEMP TABLE spy_recent_block ON COMMIT DROP AS
    WITH blk AS (
        SELECT k, v_end - k * interval '24 hours' AS b_start, v_end - (k - 1) * interval '24 hours' AS b_end
        FROM generate_series(1, 14) k
        WHERE v_end - k * interval '24 hours' >= v_stable
    ), w AS (
        SELECT z.k, z.publisher_id, z.device_id, 1000.0 / (z.sc * count(*) OVER (PARTITION BY z.k)) AS w
        FROM (
            SELECT b.k, ph.publisher_id, ph.device_id, sum(ph.scrapes)::float8 AS sc
            FROM blk b
            JOIN tracks_api.scrape_coverage_v2 ph ON ph.hour >= b.b_start AND ph.hour < b.b_end AND ph.closed
            GROUP BY 1, 2, 3
            HAVING sum(ph.scrapes) > 0
        ) z
    ), h AS (
        -- Each sighting counts for the account that paid for it.
        SELECT x.ad_id, x.account_id, x.publisher_id, x.device_id,
               ceil(extract(epoch FROM (v_end - x.hour)) / 86400)::int AS k,
               sum(x.sightings) AS s
        FROM tracks_api.ad_account_brand_hourly_v1 x
        WHERE x.hour >= (SELECT min(b_start) FROM blk) AND x.hour < v_end
        GROUP BY 1, 2, 3, 4, 5
    )
    SELECT a.creative_id, ao.operator_id, h.k, sum(h.s)::bigint AS s, sum(h.s * w.w)::float8 AS n
    FROM h
    JOIN w USING (k, publisher_id, device_id)
    JOIN tracks_api.ad_v1 a ON a.id = h.ad_id
    LEFT JOIN spy.account_operator ao ON ao.account_id = h.account_id
    GROUP BY 1, 2, 3;

    DELETE FROM spy.creative_recent;
    INSERT INTO spy.creative_recent (creative_id, sightings_24h, sightings_prev_24h, rate_24h, rate_prev_24h,
                                     peak_end, peak_sightings, peak_rate)
    SELECT creative_id,
           COALESCE(sum(s) FILTER (WHERE k = 1), 0),
           COALESCE(sum(s) FILTER (WHERE k = 2), 0),
           COALESCE(sum(n) FILTER (WHERE k = 1), 0),
           COALESCE(sum(n) FILTER (WHERE k = 2), 0),
           v_end - ((max(ARRAY[n, k, s]))[2]::int - 1) * interval '24 hours',
           (max(ARRAY[n, k, s]))[3]::int,
           (max(ARRAY[n, k, s]))[1]
    FROM (
        SELECT creative_id, k, sum(s)::float8 AS s, sum(n) AS n
        FROM spy_recent_block GROUP BY 1, 2
    ) b
    GROUP BY creative_id;
    GET DIAGNOSTICS v_rows = ROW_COUNT;

    DELETE FROM spy.operator_recent;
    INSERT INTO spy.operator_recent (operator_id, sightings_24h, sightings_prev_24h, rate_24h, rate_prev_24h)
    SELECT b.operator_id,
           COALESCE(sum(b.s) FILTER (WHERE b.k = 1), 0),
           COALESCE(sum(b.s) FILTER (WHERE b.k = 2), 0),
           COALESCE(sum(b.n) FILTER (WHERE b.k = 1), 0),
           COALESCE(sum(b.n) FILTER (WHERE b.k = 2), 0)
    FROM spy_recent_block b
    JOIN spy.operator o ON o.id = b.operator_id
    WHERE b.k <= 2
    GROUP BY b.operator_id;

    INSERT INTO spy.recent_window (one, window_end, network_sightings_24h, refreshed_at)
    SELECT TRUE, v_end, COALESCE(sum(sightings), 0), clock_timestamp()
    FROM tracks_api.scrape_coverage_v2
    WHERE hour >= v_end - interval '24 hours' AND hour < v_end AND closed
    ON CONFLICT (one) DO UPDATE
        SET window_end = EXCLUDED.window_end,
            network_sightings_24h = EXCLUDED.network_sightings_24h,
            refreshed_at = EXCLUDED.refreshed_at;

    RETURN v_rows;
END;
$$;
