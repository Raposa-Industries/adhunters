-- Numbers for any date range: sightings, rate, momentum, share and rank of
-- creatives, operators and publishers between any start and end, compared
-- with the period of the same length just before it. See range, rate and
-- momentum in GLOSSARY.md.
--
-- Precision. Tracks keeps hourly counts for the last 35 days (older ones go
-- to Parquet) and daily counts forever. A range whose period before starts
-- inside the last 35 days is read to the hour: both ends round to the
-- nearest hour. An older one is read in whole UTC days: both ends round to
-- the nearest UTC midnight. Either way the whole days in the middle come
-- from the daily counts and only the partial hours at the edges from the
-- hourly counts, so a long range costs about what its days cost. The range
-- actually used is in spy.range_info().
--
-- Rates and momentum divide by scrapes (Tracks' scrape coverage, per hour),
-- so a collection outage or a publisher scraped more often does not read as
-- an ad scaling. Momentum compares only the publishers and devices scraped
-- in both periods: it is how many sightings the range had against how many
-- the period before would have given at the range's scrapes. It is "sure"
-- when that gap is at least min_z square roots of the expected count and
-- either count reaches min_expected (the Direction settings).

INSERT INTO spy.setting (name, value, text_value, note) VALUES
    ('hourly_days', 35, NULL,
     'Ranges: hourly counts are read for the last this many days (Tracks keeps them that long); older ranges are read in whole UTC days.');

-- How a range is read. Two rows: period 'now' (the range) and 'before' (the
-- same length just before it). Each is [from_at, to_at): whole days
-- [day_from, day_to) from the daily counts, the hours [head_from, head_to)
-- and [tail_from, tail_to) from the hourly ones.
CREATE FUNCTION spy.range_plan(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_now TIMESTAMPTZ DEFAULT now())
RETURNS TABLE (period TEXT, prec TEXT, from_at TIMESTAMPTZ, to_at TIMESTAMPTZ, day_from DATE, day_to DATE,
               head_from TIMESTAMPTZ, head_to TIMESTAMPTZ, tail_from TIMESTAMPTZ, tail_to TIMESTAMPTZ)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    cur_hour TIMESTAMPTZ := date_trunc('hour', p_now, 'UTC');
    hourly_from TIMESTAMPTZ := cur_hour - make_interval(days => COALESCE((spy.cfg()->>'hourly_days')::int, 35));
    -- Daily counts are whole only for days whose hours are all closed.
    days_upto TIMESTAMPTZ := date_trunc('day', LEAST(COALESCE(spy.recent_end(p_now), p_now), p_now), 'UTC');
    f TIMESTAMPTZ;
    t TIMESTAMPTZ;
    len INTERVAL;
    pr TEXT := 'hour';
    d0 TIMESTAMPTZ;
    d1 TIMESTAMPTZ;
    k INTEGER;
BEGIN
    IF p_from IS NULL OR p_to IS NULL OR p_to <= p_from THEN
        RAISE EXCEPTION 'a range needs a start before its end (got % to %)', p_from, p_to;
    END IF;
    t := LEAST(p_to, cur_hour + interval '1 hour');
    f := date_trunc('hour', p_from + interval '30 minutes', 'UTC');
    t := date_trunc('hour', t + interval '30 minutes', 'UTC');
    IF t <= f THEN
        t := f + interval '1 hour';
    END IF;
    IF f - (t - f) < hourly_from THEN
        pr := 'day';
        f := date_trunc('day', p_from + interval '12 hours', 'UTC');
        t := date_trunc('day', LEAST(p_to, cur_hour + interval '1 hour') + interval '12 hours', 'UTC');
        IF t <= f THEN
            t := f + interval '1 day';
        END IF;
    END IF;
    len := t - f;
    FOR k IN 0..1 LOOP
        period := CASE k WHEN 0 THEN 'now' ELSE 'before' END;
        prec := pr;
        from_at := f - k * len;
        to_at := t - k * len;
        d0 := date_trunc('day', from_at, 'UTC');
        IF d0 < from_at THEN
            d0 := d0 + interval '1 day';
        END IF;
        d1 := LEAST(date_trunc('day', to_at, 'UTC'), days_upto);
        IF d1 <= d0 THEN
            -- No whole day: every hour from the hourly counts.
            day_from := (d0 AT TIME ZONE 'UTC')::date;
            day_to := day_from;
            head_from := from_at; head_to := to_at;
            tail_from := to_at; tail_to := to_at;
        ELSE
            day_from := (d0 AT TIME ZONE 'UTC')::date;
            day_to := (d1 AT TIME ZONE 'UTC')::date;
            head_from := from_at; head_to := d0;
            tail_from := d1; tail_to := to_at;
        END IF;
        RETURN NEXT;
    END LOOP;
END;
$$;

-- The range actually used, and how much of it Tracks covered: closed hours
-- and scrapes, now and before. A range reaching before Tracks started, or
-- over an outage, shows it here.
CREATE FUNCTION spy.range_info(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_now TIMESTAMPTZ DEFAULT now())
RETURNS TABLE (prec TEXT, from_at TIMESTAMPTZ, to_at TIMESTAMPTZ, before_from TIMESTAMPTZ, before_to TIMESTAMPTZ,
               hours INTEGER, hours_closed INTEGER, hours_closed_before INTEGER,
               scrapes BIGINT, scrapes_before BIGINT, sightings BIGINT, sightings_before BIGINT)
LANGUAGE plpgsql STABLE AS $$
BEGIN
    RETURN QUERY
    WITH plan AS (SELECT * FROM spy.range_plan(p_from, p_to, p_now))
    SELECT n.prec, n.from_at, n.to_at, b.from_at, b.to_at,
           (extract(epoch FROM n.to_at - n.from_at) / 3600)::int,
           (SELECT count(*)::int FROM tracks_api.closed_hour_v1 c WHERE c.hour >= n.from_at AND c.hour < n.to_at),
           (SELECT count(*)::int FROM tracks_api.closed_hour_v1 c WHERE c.hour >= b.from_at AND c.hour < b.to_at),
           (SELECT COALESCE(sum(c.scrapes), 0)::bigint FROM tracks_api.scrape_coverage_v2 c WHERE c.hour >= n.from_at AND c.hour < n.to_at),
           (SELECT COALESCE(sum(c.scrapes), 0)::bigint FROM tracks_api.scrape_coverage_v2 c WHERE c.hour >= b.from_at AND c.hour < b.to_at),
           (SELECT COALESCE(sum(c.sightings), 0)::bigint FROM tracks_api.scrape_coverage_v2 c WHERE c.hour >= n.from_at AND c.hour < n.to_at),
           (SELECT COALESCE(sum(c.sightings), 0)::bigint FROM tracks_api.scrape_coverage_v2 c WHERE c.hour >= b.from_at AND c.hour < b.to_at)
    FROM plan n JOIN plan b ON b.period = 'before'
    WHERE n.period = 'now';
END;
$$;

-- Scrapes per period, publisher and device (only those scraped at all).
CREATE FUNCTION spy.range_cover(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_now TIMESTAMPTZ)
RETURNS TABLE (period TEXT, publisher_id INTEGER, device_id SMALLINT, scrapes BIGINT, sightings BIGINT)
LANGUAGE plpgsql STABLE AS $$
BEGIN
    RETURN QUERY
    SELECT p.period, c.publisher_id, c.device_id, sum(c.scrapes)::bigint, sum(c.sightings)::bigint
    FROM spy.range_plan(p_from, p_to, p_now) p
    JOIN tracks_api.scrape_coverage_v2 c ON c.hour >= p.from_at AND c.hour < p.to_at
    GROUP BY 1, 2, 3
    HAVING sum(c.scrapes) > 0;
END;
$$;

-- Creatives over a range. One row per creative seen in the range or the
-- period before it.
CREATE FUNCTION spy.creative_range(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_now TIMESTAMPTZ DEFAULT now())
RETURNS TABLE (creative_id INTEGER, sightings BIGINT, sightings_before BIGINT, rate DOUBLE PRECISION,
               rate_before DOUBLE PRECISION, momentum_pct NUMERIC, momentum_z NUMERIC, momentum_sure BOOLEAN,
               share_pct NUMERIC, rank INTEGER, vertical TEXT, rank_in_vertical INTEGER, publishers_count INTEGER,
               phone_share_pct NUMERIC, is_junk BOOLEAN, first_seen_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    cfg JSONB := spy.cfg();
    s_min_expected NUMERIC := (cfg->>'min_expected')::numeric;
    s_min_z NUMERIC := (cfg->>'min_z')::numeric;
    phone SMALLINT := (SELECT id FROM tracks_api.device_v1 WHERE code = 'phone');
BEGIN
    RETURN QUERY
    WITH plan AS (
        SELECT * FROM spy.range_plan(p_from, p_to, p_now)
    ),
    cells AS (
        SELECT x.period, x.creative_id, x.publisher_id, x.device_id, sum(x.s) AS s, min(x.f) AS f, max(x.l) AS l
        FROM (
            SELECT p.period, d.creative_id, d.publisher_id, d.device_id, d.sightings AS s,
                   d.first_seen_at AS f, d.last_seen_at AS l
            FROM plan p JOIN tracks_api.ad_daily_v1 d ON d.day >= p.day_from AND d.day < p.day_to
            UNION ALL
            SELECT p.period, a.creative_id, h.publisher_id, h.device_id, h.sightings, h.first_seen_at, h.last_seen_at
            FROM plan p
            JOIN tracks_api.ad_hourly_v1 h ON h.hour >= p.head_from AND h.hour < p.head_to
            JOIN tracks_api.ad_v1 a ON a.id = h.ad_id
            UNION ALL
            SELECT p.period, a.creative_id, h.publisher_id, h.device_id, h.sightings, h.first_seen_at, h.last_seen_at
            FROM plan p
            JOIN tracks_api.ad_hourly_v1 h ON h.hour >= p.tail_from AND h.hour < p.tail_to
            JOIN tracks_api.ad_v1 a ON a.id = h.ad_id
        ) x
        GROUP BY 1, 2, 3, 4
    ),
    cover AS (
        SELECT * FROM spy.range_cover(p_from, p_to, p_now)
    ),
    shared AS (
        SELECT n.publisher_id, n.device_id, n.scrapes AS sc_now, b.scrapes AS sc_before
        FROM cover n JOIN cover b ON b.period = 'before' AND b.publisher_id = n.publisher_id AND b.device_id = n.device_id
        WHERE n.period = 'now'
    ),
    net AS (
        SELECT count(*) FILTER (WHERE c.period = 'now') AS pubs_now,
               count(*) FILTER (WHERE c.period = 'before') AS pubs_before,
               COALESCE(sum(c.sightings) FILTER (WHERE c.period = 'now'), 0) AS sightings_now
        FROM cover c
    ),
    per AS (
        SELECT c.creative_id,
               COALESCE(sum(c.s) FILTER (WHERE c.period = 'now'), 0) AS s_now,
               COALESCE(sum(c.s) FILTER (WHERE c.period = 'before'), 0) AS s_before,
               COALESCE(sum(1000.0 * c.s / cv.scrapes) FILTER (WHERE c.period = 'now'), 0) AS r_now,
               COALESCE(sum(1000.0 * c.s / cv.scrapes) FILTER (WHERE c.period = 'before'), 0) AS r_before,
               COALESCE(sum(c.s) FILTER (WHERE c.period = 'now' AND sh.publisher_id IS NOT NULL), 0) AS s_shared,
               COALESCE(sum(c.s::numeric * sh.sc_now / sh.sc_before)
                        FILTER (WHERE c.period = 'before' AND sh.publisher_id IS NOT NULL), 0) AS expected,
               count(DISTINCT c.publisher_id) FILTER (WHERE c.period = 'now') AS pubs,
               COALESCE(sum(c.s) FILTER (WHERE c.period = 'now' AND c.device_id = phone), 0) AS s_phone,
               min(c.f) FILTER (WHERE c.period = 'now') AS f,
               max(c.l) FILTER (WHERE c.period = 'now') AS l
        FROM cells c
        LEFT JOIN cover cv ON cv.period = c.period AND cv.publisher_id = c.publisher_id AND cv.device_id = c.device_id
        LEFT JOIN shared sh ON sh.publisher_id = c.publisher_id AND sh.device_id = c.device_id
        GROUP BY 1
    ),
    calc AS (
        SELECT pr.*, cv.shown_vertical AS vert, COALESCE(cs.is_junk, FALSE) AS junk,
               CASE WHEN pr.expected > 0 THEN (pr.s_shared - pr.expected) / sqrt(GREATEST(pr.expected, 1)) END AS z
        FROM per pr
        LEFT JOIN spy.creative_vertical cv ON cv.creative_id = pr.creative_id
        LEFT JOIN spy.creative_stats cs ON cs.creative_id = pr.creative_id
    )
    SELECT c.creative_id, c.s_now::bigint, c.s_before::bigint,
           CASE WHEN n.pubs_now > 0 THEN (c.r_now / n.pubs_now)::float8 END,
           CASE WHEN n.pubs_before > 0 THEN (c.r_before / n.pubs_before)::float8 END,
           CASE WHEN c.expected > 0 THEN round(100.0 * (c.s_shared - c.expected) / c.expected, 2) END,
           round(c.z, 2),
           COALESCE(abs(c.z) >= s_min_z AND (c.expected >= s_min_expected OR c.s_shared >= s_min_expected), FALSE),
           round(100.0 * c.s_now / GREATEST(n.sightings_now, 1), 4),
           CASE WHEN c.s_now > 0 THEN (rank() OVER (ORDER BY c.s_now DESC))::int END,
           c.vert,
           CASE WHEN c.s_now > 0 AND c.vert IS NOT NULL
                THEN (rank() OVER (PARTITION BY c.vert ORDER BY c.s_now DESC))::int END,
           c.pubs::int,
           round(100.0 * c.s_phone / GREATEST(c.s_now, 1), 2),
           c.junk, c.f, c.l
    FROM calc c CROSS JOIN net n;
END;
$$;

-- Operators over a range, each sighting counted for the account that paid for it.
CREATE FUNCTION spy.operator_range(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_now TIMESTAMPTZ DEFAULT now())
RETURNS TABLE (operator_id INTEGER, sightings BIGINT, sightings_before BIGINT, rate DOUBLE PRECISION,
               rate_before DOUBLE PRECISION, momentum_pct NUMERIC, momentum_z NUMERIC, momentum_sure BOOLEAN,
               share_pct NUMERIC, rank INTEGER, creatives_count INTEGER, publishers_count INTEGER,
               phone_share_pct NUMERIC)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    cfg JSONB := spy.cfg();
    s_min_expected NUMERIC := (cfg->>'min_expected')::numeric;
    s_min_z NUMERIC := (cfg->>'min_z')::numeric;
    phone SMALLINT := (SELECT id FROM tracks_api.device_v1 WHERE code = 'phone');
BEGIN
    RETURN QUERY
    WITH plan AS (
        SELECT * FROM spy.range_plan(p_from, p_to, p_now)
    ),
    raw AS (
        SELECT p.period, d.account_id, d.creative_id, d.publisher_id, d.device_id, d.sightings AS s
        FROM plan p JOIN tracks_api.ad_account_daily_v1 d ON d.day >= p.day_from AND d.day < p.day_to
        WHERE d.account_id IS NOT NULL
        UNION ALL
        SELECT p.period, h.account_id, a.creative_id, h.publisher_id, h.device_id, h.sightings
        FROM plan p
        JOIN tracks_api.ad_account_brand_hourly_v1 h ON h.hour >= p.head_from AND h.hour < p.head_to
        JOIN tracks_api.ad_v1 a ON a.id = h.ad_id
        WHERE h.account_id IS NOT NULL
        UNION ALL
        SELECT p.period, h.account_id, a.creative_id, h.publisher_id, h.device_id, h.sightings
        FROM plan p
        JOIN tracks_api.ad_account_brand_hourly_v1 h ON h.hour >= p.tail_from AND h.hour < p.tail_to
        JOIN tracks_api.ad_v1 a ON a.id = h.ad_id
        WHERE h.account_id IS NOT NULL
    ),
    creatives AS (
        SELECT ao.operator_id, count(DISTINCT r.creative_id) AS n
        FROM raw r JOIN spy.account_operator ao ON ao.account_id = r.account_id
        WHERE r.period = 'now'
        GROUP BY 1
    ),
    cells AS (
        SELECT r.period, ao.operator_id, r.publisher_id, r.device_id, sum(r.s) AS s
        FROM raw r JOIN spy.account_operator ao ON ao.account_id = r.account_id
        GROUP BY 1, 2, 3, 4
    ),
    cover AS (
        SELECT * FROM spy.range_cover(p_from, p_to, p_now)
    ),
    shared AS (
        SELECT n.publisher_id, n.device_id, n.scrapes AS sc_now, b.scrapes AS sc_before
        FROM cover n JOIN cover b ON b.period = 'before' AND b.publisher_id = n.publisher_id AND b.device_id = n.device_id
        WHERE n.period = 'now'
    ),
    net AS (
        SELECT count(*) FILTER (WHERE c.period = 'now') AS pubs_now,
               count(*) FILTER (WHERE c.period = 'before') AS pubs_before,
               COALESCE(sum(c.sightings) FILTER (WHERE c.period = 'now'), 0) AS sightings_now
        FROM cover c
    ),
    per AS (
        SELECT c.operator_id,
               COALESCE(sum(c.s) FILTER (WHERE c.period = 'now'), 0) AS s_now,
               COALESCE(sum(c.s) FILTER (WHERE c.period = 'before'), 0) AS s_before,
               COALESCE(sum(1000.0 * c.s / cv.scrapes) FILTER (WHERE c.period = 'now'), 0) AS r_now,
               COALESCE(sum(1000.0 * c.s / cv.scrapes) FILTER (WHERE c.period = 'before'), 0) AS r_before,
               COALESCE(sum(c.s) FILTER (WHERE c.period = 'now' AND sh.publisher_id IS NOT NULL), 0) AS s_shared,
               COALESCE(sum(c.s::numeric * sh.sc_now / sh.sc_before)
                        FILTER (WHERE c.period = 'before' AND sh.publisher_id IS NOT NULL), 0) AS expected,
               count(DISTINCT c.publisher_id) FILTER (WHERE c.period = 'now') AS pubs,
               COALESCE(sum(c.s) FILTER (WHERE c.period = 'now' AND c.device_id = phone), 0) AS s_phone
        FROM cells c
        LEFT JOIN cover cv ON cv.period = c.period AND cv.publisher_id = c.publisher_id AND cv.device_id = c.device_id
        LEFT JOIN shared sh ON sh.publisher_id = c.publisher_id AND sh.device_id = c.device_id
        GROUP BY 1
    ),
    calc AS (
        SELECT pr.*, CASE WHEN pr.expected > 0 THEN (pr.s_shared - pr.expected) / sqrt(GREATEST(pr.expected, 1)) END AS z
        FROM per pr
    )
    SELECT c.operator_id, c.s_now::bigint, c.s_before::bigint,
           CASE WHEN n.pubs_now > 0 THEN (c.r_now / n.pubs_now)::float8 END,
           CASE WHEN n.pubs_before > 0 THEN (c.r_before / n.pubs_before)::float8 END,
           CASE WHEN c.expected > 0 THEN round(100.0 * (c.s_shared - c.expected) / c.expected, 2) END,
           round(c.z, 2),
           COALESCE(abs(c.z) >= s_min_z AND (c.expected >= s_min_expected OR c.s_shared >= s_min_expected), FALSE),
           round(100.0 * c.s_now / GREATEST(n.sightings_now, 1), 4),
           CASE WHEN c.s_now > 0 THEN (rank() OVER (ORDER BY c.s_now DESC))::int END,
           COALESCE(cr.n, 0)::int, c.pubs::int,
           round(100.0 * c.s_phone / GREATEST(c.s_now, 1), 2)
    FROM calc c
    CROSS JOIN net n
    LEFT JOIN creatives cr ON cr.operator_id = c.operator_id;
END;
$$;

-- Publishers over a range: their sightings per scrape, now and before.
CREATE FUNCTION spy.publisher_range(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_now TIMESTAMPTZ DEFAULT now())
RETURNS TABLE (publisher_id INTEGER, sightings BIGINT, sightings_before BIGINT, scrapes BIGINT, scrapes_before BIGINT,
               per_scrape NUMERIC, per_scrape_before NUMERIC, change_pct NUMERIC, change_z NUMERIC,
               change_sure BOOLEAN, share_pct NUMERIC, phone_share_pct NUMERIC)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    cfg JSONB := spy.cfg();
    s_min_expected NUMERIC := (cfg->>'min_expected')::numeric;
    s_min_z NUMERIC := (cfg->>'min_z')::numeric;
    phone SMALLINT := (SELECT id FROM tracks_api.device_v1 WHERE code = 'phone');
BEGIN
    RETURN QUERY
    WITH cover AS (
        SELECT * FROM spy.range_cover(p_from, p_to, p_now)
    ),
    per AS (
        SELECT c.publisher_id,
               COALESCE(sum(c.sightings) FILTER (WHERE c.period = 'now'), 0) AS s_now,
               COALESCE(sum(c.sightings) FILTER (WHERE c.period = 'before'), 0) AS s_before,
               COALESCE(sum(c.scrapes) FILTER (WHERE c.period = 'now'), 0) AS sc_now,
               COALESCE(sum(c.scrapes) FILTER (WHERE c.period = 'before'), 0) AS sc_before,
               COALESCE(sum(c.sightings) FILTER (WHERE c.period = 'now' AND c.device_id = phone), 0) AS s_phone
        FROM cover c GROUP BY 1
    ),
    calc AS (
        SELECT pr.*,
               CASE WHEN pr.sc_before > 0 AND pr.sc_now > 0 THEN pr.s_before::numeric * pr.sc_now / pr.sc_before END AS expected
        FROM per pr
    ),
    net AS (SELECT GREATEST(sum(s_now), 1) AS total FROM per)
    SELECT c.publisher_id, c.s_now::bigint, c.s_before::bigint, c.sc_now::bigint, c.sc_before::bigint,
           CASE WHEN c.sc_now > 0 THEN round(c.s_now::numeric / c.sc_now, 4) END,
           CASE WHEN c.sc_before > 0 THEN round(c.s_before::numeric / c.sc_before, 4) END,
           CASE WHEN c.expected > 0 THEN round(100.0 * (c.s_now - c.expected) / c.expected, 2) END,
           CASE WHEN c.expected > 0 THEN round((c.s_now - c.expected) / sqrt(GREATEST(c.expected, 1)), 2) END,
           COALESCE(c.expected > 0 AND abs(c.s_now - c.expected) / sqrt(GREATEST(c.expected, 1)) >= s_min_z
                    AND (c.expected >= s_min_expected OR c.s_now >= s_min_expected), FALSE),
           round(100.0 * c.s_now / n.total, 4),
           round(100.0 * c.s_phone / GREATEST(c.s_now, 1), 2)
    FROM calc c CROSS JOIN net n;
END;
$$;
