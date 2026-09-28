-- lint: new-table
-- Numbers for any date range, as spy/METRICS.md defines them (from the Spy
-- metrics handbook). See range, check, presence, share of voice, usual,
-- momentum, clear, likely, unclear, too little data and noise in
-- GLOSSARY.md.
--
-- Everything is a sum of cells: sightings per hour (or UTC day), ad,
-- publisher and device, and the checks (scrapes) of that publisher and
-- device in the same hour. A range is read to the hour when its usual
-- period starts inside the last hourly_days (35) days: both ends round to
-- the nearest hour. An older one is read in whole UTC days. The whole days
-- inside a range come from the daily counts and only the edge hours from
-- the hourly ones, so a long range costs about what its days cost. Only
-- closed hours count: a range ends at the first hour Tracks has not closed.
--
-- Each range is compared with its usual period: for a range shorter than 7
-- days, the same hours 1, 2 and 3 weeks before (only the weeks the subject
-- already existed); for 7 days or more, the whole weeks just before it; or
-- a period the caller chooses.
--
-- Momentum is the Mantel-Haenszel rate ratio over publisher and device:
-- each publisher and device is compared only with itself, so checking one
-- publisher more (or an outage) cannot fake a change. Its likely range is
-- Greenland and Robins' variance widened by the subject's noise (how much
-- more its counts vary than chance). The words and the list order come
-- from those: see spy.subject_range().

INSERT INTO spy.setting (name, value, text_value, note) VALUES
    ('hourly_days', 35, NULL,
     'Ranges: hourly counts are read for the last this many days; older ranges are read in whole UTC days. Tracks keeps hourly counts longer for now, so this can grow until they move to Parquet.'),
    ('word_rise', 1.25, NULL, 'Momentum: rising at this ratio to usual or more, when the change is real.'),
    ('word_fall', 0.8, NULL, 'Momentum: fading at this ratio or less, when real. Steady: the likely range sits between word_fall and word_rise.'),
    ('range_z', 1.645, NULL, 'Likely ranges hold the true value 90% of the time (1.645 standard errors).'),
    ('fdr', 0.10, NULL, 'Clear: false discoveries stay below this share of a list (Benjamini-Hochberg).'),
    ('min_sightings', 10, NULL, 'Momentum: fewer sightings than this in the range and its usual period together is too little data.'),
    ('max_width', 3, NULL, 'Momentum: a likely range wider than this (high / low) is too little data.'),
    ('noise_min_sightings', 200, NULL, 'Noise: measured on the range itself from this many sightings; below it, the daily measure.'),
    ('noise_min_strata', 4, NULL, 'Noise: measured only over more than this many strata (publisher, device and hour or day of the week).'),
    ('hour_cells_days', 3, NULL, 'Ranges shorter than this many days are read hour by hour, each hour against the same hour of the usual weeks; longer ones read their whole days from the daily counts.'),
    ('noise_days', 2, NULL, 'Noise: the daily measure (spy.dispersion) reads this many days of closed hours.'),
    ('new_days', 3, NULL, 'New: first seen in the last this many days.'),
    ('lifespan_days', 90, NULL, 'Lifespan: creatives first seen in these days make the curve a creative is compared with.'),
    ('ended_hours', 48, NULL, 'Lifespan: a creative not seen for this many hours has ended.'),
    ('lifespan_min_group', 20, NULL, 'Lifespan: a vertical needs this many creatives for its own curve; smaller ones use all creatives.'),
    ('hit_days', 7, NULL, 'Hit rate: a launch that ran this many days is a hit.'),
    ('hit_min_known', 5, NULL, 'Hit rate: shown once this many launches have an outcome.');

-- How much each subject's counts vary beyond chance, measured once a day
-- over the last noise_days of closed hours (spy.refresh_dispersion, run
-- with Direction's rebuild). Direction and short ranges use it.
CREATE TABLE spy.dispersion (
    kind TEXT NOT NULL,
    key TEXT NOT NULL,
    phi NUMERIC NOT NULL,            -- 1: as random as chance; 3: three times the variance
    measured BOOLEAN NOT NULL,       -- false: too few sightings, its vertical's median (or everyone's)
    sightings INTEGER NOT NULL,
    hours INTEGER NOT NULL,
    refreshed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (kind, key)
);

-- Where closed hours stop as of p_now: the first hour not closed yet,
-- counting from the oldest closed hour of the last 15 days. NULL before any
-- closed hour.
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

-- Splits [p_from, p_to) into whole UTC days before p_days_upto, read from
-- the daily counts, and the hours at either edge.
CREATE FUNCTION spy.range_split(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_days_upto TIMESTAMPTZ,
                                OUT day_from DATE, OUT day_to DATE, OUT head_from TIMESTAMPTZ,
                                OUT head_to TIMESTAMPTZ, OUT tail_from TIMESTAMPTZ, OUT tail_to TIMESTAMPTZ)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    d0 TIMESTAMPTZ := date_trunc('day', p_from, 'UTC');
    d1 TIMESTAMPTZ := LEAST(date_trunc('day', p_to, 'UTC'), p_days_upto);
BEGIN
    IF d0 < p_from THEN
        d0 := d0 + interval '1 day';
    END IF;
    day_from := (d0 AT TIME ZONE 'UTC')::date;
    IF d1 <= d0 THEN
        day_to := day_from;
        head_from := p_from; head_to := p_to;
        tail_from := p_to; tail_to := p_to;
    ELSE
        day_to := (d1 AT TIME ZONE 'UTC')::date;
        head_from := p_from; head_to := d0;
        tail_from := d1; tail_to := p_to;
    END IF;
END;
$$;

-- How a range is read: one row for the range (period 'now', wk 0) and one
-- per usual period (period 'usual', wk 1 up). usual_kind says which usual
-- was used: 'weeks' (the same hours 1-3 weeks back), 'before' (the whole
-- weeks just before) or 'chosen'.
CREATE FUNCTION spy.range_plan(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_usual_from TIMESTAMPTZ DEFAULT NULL,
                               p_usual_to TIMESTAMPTZ DEFAULT NULL, p_now TIMESTAMPTZ DEFAULT now())
RETURNS TABLE (period TEXT, wk INTEGER, prec TEXT, usual_kind TEXT, from_at TIMESTAMPTZ, to_at TIMESTAMPTZ,
               day_from DATE, day_to DATE, head_from TIMESTAMPTZ, head_to TIMESTAMPTZ,
               tail_from TIMESTAMPTZ, tail_to TIMESTAMPTZ)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    cfg JSONB := spy.cfg();
    cur_hour TIMESTAMPTZ := date_trunc('hour', p_now, 'UTC');
    closed_to TIMESTAMPTZ := COALESCE(spy.recent_end(p_now), cur_hour);
    hourly_from TIMESTAMPTZ := cur_hour - make_interval(days => COALESCE((cfg->>'hourly_days')::int, 35));
    days_upto TIMESTAMPTZ := date_trunc('day', COALESCE(spy.recent_end(p_now), cur_hour), 'UTC');
    weeks INTEGER := COALESCE((cfg->>'usual_weeks')::int, 3);
    f TIMESTAMPTZ;
    t TIMESTAMPTZ;
    uf TIMESTAMPTZ;
    ut TIMESTAMPTZ;
    pr TEXT := 'hour';
    uk TEXT;
    before_weeks INTEGER;
BEGIN
    IF p_from IS NULL OR p_to IS NULL OR p_to <= p_from THEN
        RAISE EXCEPTION 'a range needs a start before its end (got % to %)', p_from, p_to;
    END IF;
    IF (p_usual_from IS NULL) <> (p_usual_to IS NULL) OR p_usual_to <= p_usual_from THEN
        RAISE EXCEPTION 'a chosen usual period needs a start before its end (got % to %)', p_usual_from, p_usual_to;
    END IF;

    f := date_trunc('hour', p_from + interval '30 minutes', 'UTC');
    t := date_trunc('hour', LEAST(p_to, closed_to) + interval '30 minutes', 'UTC');
    IF p_usual_from IS NOT NULL THEN
        uk := 'chosen';
        uf := date_trunc('hour', p_usual_from + interval '30 minutes', 'UTC');
        ut := date_trunc('hour', LEAST(p_usual_to, closed_to) + interval '30 minutes', 'UTC');
    ELSIF t - f < interval '7 days' THEN
        uk := 'weeks';
        uf := f - make_interval(days => 7 * weeks);
    ELSE
        uk := 'before';
        before_weeks := GREATEST(1, round(extract(epoch FROM t - f) / 604800)::int);
        uf := f - make_interval(days => 7 * before_weeks);
    END IF;

    IF uf < hourly_from THEN
        pr := 'day';
        f := date_trunc('day', p_from + interval '12 hours', 'UTC');
        t := date_trunc('day', LEAST(p_to, closed_to) + interval '12 hours', 'UTC');
        IF t > closed_to THEN
            t := date_trunc('day', closed_to, 'UTC');
        END IF;
        IF uk = 'chosen' THEN
            uf := date_trunc('day', p_usual_from + interval '12 hours', 'UTC');
            ut := LEAST(date_trunc('day', LEAST(p_usual_to, closed_to) + interval '12 hours', 'UTC'),
                        date_trunc('day', closed_to, 'UTC'));
        ELSIF uk = 'before' THEN
            before_weeks := GREATEST(1, round(extract(epoch FROM t - f) / 604800)::int);
        END IF;
    END IF;
    IF t <= f THEN
        RAISE EXCEPTION 'the range % to % has no closed % yet', p_from, p_to, pr;
    END IF;
    IF pr = 'hour' AND t - f < make_interval(days => COALESCE((cfg->>'hour_cells_days')::int, 3)) THEN
        days_upto := '-infinity';
    END IF;
    IF uk = 'chosen' AND ut <= uf THEN
        RAISE EXCEPTION 'the usual period % to % has no closed % yet', p_usual_from, p_usual_to, pr;
    END IF;

    RETURN QUERY
    SELECT x.period, x.wk, pr, uk, x.f, x.t, s.day_from, s.day_to, s.head_from, s.head_to, s.tail_from, s.tail_to
    FROM (
        SELECT 'now'::text AS period, 0 AS wk, f AS f, t AS t
        UNION ALL
        SELECT 'usual', 1, uf, ut WHERE uk = 'chosen'
        UNION ALL
        SELECT 'usual', g, f - make_interval(days => 7 * g), t - make_interval(days => 7 * g)
        FROM generate_series(1, weeks) g WHERE uk = 'weeks'
        UNION ALL
        SELECT 'usual', 1, f - make_interval(days => 7 * before_weeks), f WHERE uk = 'before'
    ) x
    CROSS JOIN LATERAL spy.range_split(x.f, x.t, days_upto) s;
END;
$$;

-- The range actually read, its usual period, and how much of it Tracks
-- covered: hours without a single check mean collection was down, which
-- leaves rates right but ranges wider. Say so next to the numbers.
CREATE FUNCTION spy.range_info(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_usual_from TIMESTAMPTZ DEFAULT NULL,
                               p_usual_to TIMESTAMPTZ DEFAULT NULL, p_now TIMESTAMPTZ DEFAULT now())
RETURNS TABLE (prec TEXT, from_at TIMESTAMPTZ, to_at TIMESTAMPTZ, usual_kind TEXT, usual_periods INTEGER,
               usual_from TIMESTAMPTZ, usual_to TIMESTAMPTZ, hours INTEGER, hours_unchecked INTEGER,
               checks BIGINT, sightings BIGINT, checks_usual BIGINT, sightings_usual BIGINT)
LANGUAGE plpgsql STABLE AS $$
BEGIN
    RETURN QUERY
    WITH plan AS (
        SELECT * FROM spy.range_plan(p_from, p_to, p_usual_from, p_usual_to, p_now)
    ),
    cov AS (
        SELECT p.period, c.hour, sum(c.scrapes) AS e, sum(c.sightings) AS n
        FROM plan p
        JOIN tracks_api.scrape_coverage_v2 c ON c.closed AND c.hour >= p.from_at AND c.hour < p.to_at
        GROUP BY 1, 2
    )
    SELECT n.prec, n.from_at, n.to_at, n.usual_kind,
           (SELECT count(*)::int FROM plan u WHERE u.period = 'usual'),
           (SELECT min(u.from_at) FROM plan u WHERE u.period = 'usual'),
           (SELECT max(u.to_at) FROM plan u WHERE u.period = 'usual'),
           (extract(epoch FROM n.to_at - n.from_at) / 3600)::int,
           (SELECT count(*)::int FROM generate_series(n.from_at, n.to_at - interval '1 hour', interval '1 hour') g
            WHERE NOT EXISTS (SELECT 1 FROM cov c WHERE c.period = 'now' AND c.hour = g AND c.e > 0)),
           (SELECT COALESCE(sum(c.e), 0)::bigint FROM cov c WHERE c.period = 'now'),
           (SELECT COALESCE(sum(c.n), 0)::bigint FROM cov c WHERE c.period = 'now'),
           (SELECT COALESCE(sum(c.e), 0)::bigint FROM cov c WHERE c.period = 'usual'),
           (SELECT COALESCE(sum(c.n), 0)::bigint FROM cov c WHERE c.period = 'usual')
    FROM plan n
    WHERE n.period = 'now';
END;
$$;

-- One subject over a range, on one network. Every list of creatives,
-- operators and verticals is made of these rows; each kind adds its own
-- columns (spy.creative_range() and so on). Add a number here and every
-- kind gets it.
CREATE TYPE spy.range_row AS (
    key TEXT,
    network_id INTEGER,
    sightings BIGINT,            -- in the range, every publisher: the evidence line
    sightings_usual BIGINT,      -- in the usual periods that count
    checks BIGINT,               -- of the network in the range
    checks_usual BIGINT,
    presence NUMERIC,            -- sightings per 100 checks of the network
    presence_usual NUMERIC,
    phone_presence NUMERIC,      -- the same, phone sightings per 100 phone checks
    desktop_presence NUMERIC,
    share_pct NUMERIC,           -- share of voice: its share of all the network's sightings
    share_usual_pct NUMERIC,
    share_gain_pts NUMERIC,      -- share_pct - share_usual_pct
    rank INTEGER,                -- by sightings in the network, 1 = most
    momentum NUMERIC,            -- Mantel-Haenszel rate ratio against usual; 1 = no change
    momentum_low NUMERIC,        -- likely range (90%)
    momentum_high NUMERIC,
    momentum_word TEXT,          -- rising, fading, steady, unclear, new, too_little
    momentum_sure TEXT,          -- clear (rising, fading); likely (unclear, but the range leaves out 1)
    momentum_rank INTEGER,       -- "who had more momentum": shrunk ratio, 1 = most
    noise NUMERIC,               -- the dispersion used (phi)
    usual_periods INTEGER,       -- usual periods that count (the subject existed); 0 = new
    publishers INTEGER,          -- publishers it was seen on in the range
    first_seen_at TIMESTAMPTZ
);

-- Which slot of the week a bucket is: its hour of the week ('h0'..'h167',
-- Monday 00h UTC first) or, for a whole day, its day of the week ('d1'..'d7').
-- Momentum compares each publisher and device slot by slot, so a range is
-- compared hour for hour with the same hours of its usual weeks, and an
-- outage or a busier publisher at some hours cannot fake a change through
-- the daily cycle. A chosen usual period need not line up, so it has one
-- slot ('').
CREATE FUNCTION spy.range_slot(p_at TIMESTAMPTZ, p_day BOOLEAN, p_usual_kind TEXT) RETURNS TEXT
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN p_usual_kind = 'chosen' THEN ''
                WHEN p_day THEN 'd' || extract(isodow FROM p_at AT TIME ZONE 'UTC')::int
                ELSE 'h' || ((extract(isodow FROM p_at AT TIME ZONE 'UTC')::int - 1) * 24
                             + extract(hour FROM p_at AT TIME ZONE 'UTC')::int) END
$$;

-- The core: every number of spy.range_row for one kind (ad, creative,
-- operator, vertical). Keys: the id as text, or the vertical's name.
--
--   Presence   100 * sightings / checks, the network's checks in the range.
--   Share      sightings / all the network's sightings in the range.
--   Momentum   M = sum(a*T0/T) / sum(b*T1/T) over strata s: publisher,
--              device and slot of the week (spy.range_slot),
--              a = sightings in the range, b = in the usual periods that
--              count, T1 and T0 their checks, T = T1 + T0.
--              Var(ln M) = noise * sum(T1*T0*(a+b)/T^2) / (sum(a*T0/T) * sum(b*T1/T))
--              likely range = M * exp(+-range_z * sqrt(Var)).
--              Not seen in the range (M = 0) or not in its usual periods (M
--              has no value): the open end of the range comes from the chance
--              of seeing nothing, P = exp(-M * sum((a+b)*T1/T0) / noise).
--   Words      new: no usual period counts (first seen inside it or the range).
--              too_little: fewer than min_sightings sightings on the
--              publishers checked in both, counted in clumps of its noise
--              (a+b / noise); or a likely range wider than max_width; or,
--              with one side 0, a range that does not clear word_fall or
--              word_rise. rising: M >= word_rise and the change passes
--              Benjamini-Hochberg at fdr over the network's list. fading: M <=
--              word_fall and it passes. steady: the likely range inside
--              word_fall..word_rise. unclear: anything else.
--   Sure       clear: rising and fading (they passed the list's check).
--              likely: unclear, but the likely range leaves out 1; the
--              direction is the side M is on. A 90% range leaves out 1 for
--              about 1 unchanged subject in 10, so a list never calls it more.
--   Rank       ln M shrunk toward 0 by tau^2 / (tau^2 + Var), tau^2 the spread
--              of real changes in the list (empirical Bayes), so a 5-sighting
--              jump does not beat a 2,000 to 4,000 one.
--   Noise      phi = max(1, Pearson chi-square / (strata - 1)) of a against
--              what M predicts in each stratum, (a+b) * M*T1 / (M*T1 + T0),
--              binomial variance: how much more the subject varies than the
--              MH variance assumes (quasi-Poisson). A real change that differs
--              between publishers or hours raises it too, so it errs toward
--              saying less. Below noise_min_sightings: spy.dispersion, else
--              the list's median.
CREATE FUNCTION spy.subject_range(p_kind TEXT, p_from TIMESTAMPTZ, p_to TIMESTAMPTZ,
                                  p_usual_from TIMESTAMPTZ DEFAULT NULL, p_usual_to TIMESTAMPTZ DEFAULT NULL,
                                  p_now TIMESTAMPTZ DEFAULT now())
RETURNS SETOF spy.range_row
LANGUAGE plpgsql AS $$
DECLARE
    cfg JSONB := spy.cfg();
    s_rise FLOAT8 := (cfg->>'word_rise')::float8;
    s_fall FLOAT8 := (cfg->>'word_fall')::float8;
    s_z FLOAT8 := (cfg->>'range_z')::float8;
    s_fdr FLOAT8 := (cfg->>'fdr')::float8;
    s_min FLOAT8 := (cfg->>'min_sightings')::float8;
    s_width FLOAT8 := (cfg->>'max_width')::float8;
    s_noise_min FLOAT8 := (cfg->>'noise_min_sightings')::float8;
    s_noise_strata INTEGER := (cfg->>'noise_min_strata')::int;
    phone SMALLINT := (SELECT id FROM tracks_api.device_v1 WHERE code = 'phone');
    desktop SMALLINT := (SELECT id FROM tracks_api.device_v1 WHERE code = 'desktop');
BEGIN
    IF p_kind IS NULL OR p_kind NOT IN ('ad', 'creative', 'operator', 'vertical') THEN
        RAISE EXCEPTION 'ranges are for ads, creatives, operators and verticals, not %', p_kind;
    END IF;

    CREATE TEMP TABLE IF NOT EXISTS spy_r_plan (period TEXT, wk INTEGER, prec TEXT, usual_kind TEXT,
        from_at TIMESTAMPTZ, to_at TIMESTAMPTZ, day_from DATE, day_to DATE, head_from TIMESTAMPTZ,
        head_to TIMESTAMPTZ, tail_from TIMESTAMPTZ, tail_to TIMESTAMPTZ) ON COMMIT DROP;
    TRUNCATE spy_r_plan;
    INSERT INTO spy_r_plan SELECT * FROM spy.range_plan(p_from, p_to, p_usual_from, p_usual_to, p_now);

    -- Checks and all sightings per publisher, device and slot.
    CREATE TEMP TABLE IF NOT EXISTS spy_r_cov (network_id INTEGER, publisher_id INTEGER, device_id SMALLINT,
        period TEXT, wk INTEGER, slot TEXT, e BIGINT, n_all BIGINT) ON COMMIT DROP;
    TRUNCATE spy_r_cov;
    INSERT INTO spy_r_cov
    SELECT pb.network_id, c.publisher_id, c.device_id, p.period, p.wk,
           spy.range_slot(c.hour, c.hour >= p.head_to AND c.hour < p.tail_from, p.usual_kind),
           sum(c.scrapes), sum(c.sightings)
    FROM spy_r_plan p
    JOIN tracks_api.scrape_coverage_v2 c ON c.closed AND c.hour >= p.from_at AND c.hour < p.to_at
    JOIN tracks_api.publisher_v1 pb ON pb.id = c.publisher_id
    GROUP BY 1, 2, 3, 4, 5, 6
    HAVING sum(c.scrapes) > 0;
    ANALYZE spy_r_cov;

    -- The subject's sightings per publisher, device and slot.
    CREATE TEMP TABLE IF NOT EXISTS spy_r_cell (key TEXT, network_id INTEGER, publisher_id INTEGER,
        device_id SMALLINT, period TEXT, wk INTEGER, slot TEXT, n BIGINT) ON COMMIT DROP;
    TRUNCATE spy_r_cell;
    IF p_kind = 'operator' THEN
        -- Each sighting counts for the account that paid for it.
        INSERT INTO spy_r_cell
        SELECT ao.operator_id::text, pb.network_id, x.publisher_id, x.device_id, x.period, x.wk, x.slot, sum(x.n)
        FROM (
            SELECT p.period, p.wk, spy.range_slot(d.day::timestamp AT TIME ZONE 'UTC', TRUE, p.usual_kind) AS slot,
                   d.account_id, d.publisher_id, d.device_id, d.sightings AS n
            FROM spy_r_plan p JOIN tracks_api.ad_account_daily_v1 d ON d.day >= p.day_from AND d.day < p.day_to
            UNION ALL
            SELECT p.period, p.wk, spy.range_slot(h.hour, FALSE, p.usual_kind), h.account_id, h.publisher_id,
                   h.device_id, h.sightings
            FROM spy_r_plan p
            JOIN tracks_api.ad_account_brand_hourly_v1 h ON h.hour >= p.head_from AND h.hour < p.head_to
            UNION ALL
            SELECT p.period, p.wk, spy.range_slot(h.hour, FALSE, p.usual_kind), h.account_id, h.publisher_id,
                   h.device_id, h.sightings
            FROM spy_r_plan p
            JOIN tracks_api.ad_account_brand_hourly_v1 h ON h.hour >= p.tail_from AND h.hour < p.tail_to
        ) x
        JOIN spy.account_operator ao ON ao.account_id = x.account_id
        JOIN tracks_api.publisher_v1 pb ON pb.id = x.publisher_id
        GROUP BY 1, 2, 3, 4, 5, 6, 7;
    ELSE
        INSERT INTO spy_r_cell
        SELECT CASE p_kind WHEN 'ad' THEN x.ad_id::text WHEN 'creative' THEN x.creative_id::text ELSE cv.vertical END,
               pb.network_id, x.publisher_id, x.device_id, x.period, x.wk, x.slot, sum(x.n)
        FROM (
            SELECT p.period, p.wk, spy.range_slot(d.day::timestamp AT TIME ZONE 'UTC', TRUE, p.usual_kind) AS slot,
                   d.ad_id, d.creative_id, d.publisher_id, d.device_id, d.sightings AS n
            FROM spy_r_plan p JOIN tracks_api.ad_daily_v1 d ON d.day >= p.day_from AND d.day < p.day_to
            UNION ALL
            SELECT p.period, p.wk, spy.range_slot(h.hour, FALSE, p.usual_kind), h.ad_id, a.creative_id,
                   h.publisher_id, h.device_id, h.sightings
            FROM spy_r_plan p
            JOIN tracks_api.ad_hourly_v1 h ON h.closed AND h.hour >= p.head_from AND h.hour < p.head_to
            JOIN tracks_api.ad_v1 a ON a.id = h.ad_id
            UNION ALL
            SELECT p.period, p.wk, spy.range_slot(h.hour, FALSE, p.usual_kind), h.ad_id, a.creative_id,
                   h.publisher_id, h.device_id, h.sightings
            FROM spy_r_plan p
            JOIN tracks_api.ad_hourly_v1 h ON h.closed AND h.hour >= p.tail_from AND h.hour < p.tail_to
            JOIN tracks_api.ad_v1 a ON a.id = h.ad_id
        ) x
        JOIN tracks_api.publisher_v1 pb ON pb.id = x.publisher_id
        LEFT JOIN spy.creative_vertical cv ON p_kind = 'vertical' AND cv.creative_id = x.creative_id
        WHERE p_kind <> 'vertical' OR cv.vertical IS NOT NULL
        GROUP BY 1, 2, 3, 4, 5, 6, 7;
    END IF;
    ANALYZE spy_r_cell;

    -- When each subject was first seen, and which usual periods count for
    -- it: those that started after it existed.
    CREATE TEMP TABLE IF NOT EXISTS spy_r_subject (key TEXT PRIMARY KEY, first_seen TIMESTAMPTZ) ON COMMIT DROP;
    TRUNCATE spy_r_subject;
    IF p_kind = 'ad' THEN
        INSERT INTO spy_r_subject
        SELECT k.key, a.first_seen_at FROM (SELECT DISTINCT key FROM spy_r_cell) k
        LEFT JOIN tracks_api.ad_v1 a ON a.id = k.key::int;
    ELSIF p_kind = 'creative' THEN
        INSERT INTO spy_r_subject
        SELECT k.key, c.first_seen_at FROM (SELECT DISTINCT key FROM spy_r_cell) k
        LEFT JOIN tracks_api.creative_v1 c ON c.id = k.key::int;
    ELSIF p_kind = 'operator' THEN
        INSERT INTO spy_r_subject
        SELECT k.key, f.first_seen FROM (SELECT DISTINCT key FROM spy_r_cell) k
        LEFT JOIN (SELECT ao.operator_id::text AS key, min(a.first_seen_at) AS first_seen
                   FROM spy.account_operator ao JOIN tracks_api.ad_v1 a ON a.account_id = ao.account_id
                   GROUP BY 1) f USING (key);
    ELSE
        INSERT INTO spy_r_subject
        SELECT k.key, f.first_seen FROM (SELECT DISTINCT key FROM spy_r_cell) k
        LEFT JOIN (SELECT cv.vertical AS key, min(c.first_seen_at) AS first_seen
                   FROM spy.creative_vertical cv JOIN tracks_api.creative_v1 c ON c.id = cv.creative_id
                   WHERE cv.vertical IS NOT NULL GROUP BY 1) f USING (key);
    END IF;

    CREATE TEMP TABLE IF NOT EXISTS spy_r_elig (key TEXT, wk INTEGER, PRIMARY KEY (key, wk)) ON COMMIT DROP;
    TRUNCATE spy_r_elig;
    INSERT INTO spy_r_elig
    SELECT s.key, p.wk FROM spy_r_subject s
    JOIN spy_r_plan p ON p.period = 'usual' AND COALESCE(s.first_seen, '-infinity') < p.from_at;
    ANALYZE spy_r_subject, spy_r_elig;

    -- Per subject and stratum (publisher, device and slot): a and b, T1 and T0.
    CREATE TEMP TABLE IF NOT EXISTS spy_r_stratum (key TEXT, network_id INTEGER, publisher_id INTEGER,
        device_id SMALLINT, slot TEXT, a BIGINT, b BIGINT, t1 BIGINT, t0 BIGINT) ON COMMIT DROP;
    TRUNCATE spy_r_stratum;
    INSERT INTO spy_r_stratum
    WITH cell AS (
        SELECT c.key, c.network_id, c.publisher_id, c.device_id, c.slot,
               COALESCE(sum(c.n) FILTER (WHERE c.period = 'now'), 0) AS a,
               COALESCE(sum(c.n) FILTER (WHERE c.period = 'usual' AND el.key IS NOT NULL), 0) AS b
        FROM spy_r_cell c
        LEFT JOIN spy_r_elig el ON el.key = c.key AND el.wk = c.wk
        GROUP BY 1, 2, 3, 4, 5
    ),
    cov AS (
        SELECT publisher_id, device_id, slot, period, wk, sum(e) AS e FROM spy_r_cov GROUP BY 1, 2, 3, 4, 5
    ),
    t1 AS (
        SELECT cl.key, cl.publisher_id, cl.device_id, cl.slot, sum(cv.e) AS e
        FROM cell cl
        JOIN cov cv ON cv.period = 'now' AND cv.publisher_id = cl.publisher_id AND cv.device_id = cl.device_id
                   AND cv.slot = cl.slot
        GROUP BY 1, 2, 3, 4
    ),
    t0 AS (
        SELECT cl.key, cl.publisher_id, cl.device_id, cl.slot, sum(cv.e) AS e
        FROM cell cl
        JOIN spy_r_elig el ON el.key = cl.key
        JOIN cov cv ON cv.period = 'usual' AND cv.wk = el.wk AND cv.publisher_id = cl.publisher_id
                   AND cv.device_id = cl.device_id AND cv.slot = cl.slot
        GROUP BY 1, 2, 3, 4
    )
    SELECT cl.key, cl.network_id, cl.publisher_id, cl.device_id, cl.slot, cl.a, cl.b,
           COALESCE(t1.e, 0), COALESCE(t0.e, 0)
    FROM cell cl
    LEFT JOIN t1 USING (key, publisher_id, device_id, slot)
    LEFT JOIN t0 USING (key, publisher_id, device_id, slot)
    WHERE cl.a > 0 OR cl.b > 0;
    ANALYZE spy_r_stratum;

    -- Per subject and network: the sums every number is made of.
    CREATE TEMP TABLE IF NOT EXISTS spy_r_key (key TEXT, network_id INTEGER, a BIGINT, b BIGINT,
        ab_shared BIGINT, num FLOAT8, den FLOAT8, v FLOAT8, reach1 FLOAT8, reach0 FLOAT8, pubs INTEGER, a_phone BIGINT, a_desktop BIGINT,
        periods INTEGER, first_seen TIMESTAMPTZ, noise FLOAT8, PRIMARY KEY (key, network_id)) ON COMMIT DROP;
    TRUNCATE spy_r_key;
    INSERT INTO spy_r_key
    SELECT s.key, s.network_id, sum(s.a), sum(s.b),
           COALESCE(sum(s.a + s.b) FILTER (WHERE s.t1 > 0 AND s.t0 > 0), 0),
           COALESCE(sum(s.a::float8 * s.t0 / (s.t1 + s.t0)) FILTER (WHERE s.t1 + s.t0 > 0), 0),
           COALESCE(sum(s.b::float8 * s.t1 / (s.t1 + s.t0)) FILTER (WHERE s.t1 + s.t0 > 0), 0),
           COALESCE(sum(s.t1::float8 * s.t0 * (s.a + s.b) / ((s.t1 + s.t0)::float8 ^ 2)) FILTER (WHERE s.t1 + s.t0 > 0), 0),
           COALESCE(sum((s.a + s.b)::float8 * s.t1 / s.t0) FILTER (WHERE s.t1 > 0 AND s.t0 > 0), 0),
           COALESCE(sum((s.a + s.b)::float8 * s.t0 / s.t1) FILTER (WHERE s.t1 > 0 AND s.t0 > 0), 0),
           count(DISTINCT s.publisher_id) FILTER (WHERE s.a > 0),
           COALESCE(sum(s.a) FILTER (WHERE s.device_id = phone), 0),
           COALESCE(sum(s.a) FILTER (WHERE s.device_id = desktop), 0),
           (SELECT count(*) FROM spy_r_elig el WHERE el.key = s.key),
           sb.first_seen, NULL
    FROM spy_r_stratum s
    JOIN spy_r_subject sb ON sb.key = s.key
    GROUP BY s.key, s.network_id, sb.first_seen;
    ANALYZE spy_r_key;

    -- Noise, measured on the strata for subjects with enough sightings.
    WITH fit AS (
        SELECT s.key, s.network_id, (s.a + s.b)::float8 AS ab, s.a::float8 AS a,
               (k.num / k.den) * s.t1 / ((k.num / k.den) * s.t1 + s.t0) AS p
        FROM spy_r_stratum s JOIN spy_r_key k USING (key, network_id)
        WHERE k.num > 0 AND k.den > 0 AND k.ab_shared >= s_noise_min AND s.t1 > 0 AND s.t0 > 0 AND s.a + s.b > 0
    ),
    phi AS (
        SELECT key, network_id,
               GREATEST(1, sum((a - ab * p) ^ 2 / (ab * p * (1 - p))) / (count(*) - 1)) AS phi
        FROM fit GROUP BY 1, 2 HAVING count(*) > s_noise_strata
    )
    UPDATE spy_r_key k SET noise = phi.phi FROM phi WHERE phi.key = k.key AND phi.network_id = k.network_id;
    UPDATE spy_r_key k SET noise = d.phi
    FROM spy.dispersion d WHERE k.noise IS NULL AND d.kind = p_kind AND d.key = k.key;
    UPDATE spy_r_key SET noise = COALESCE(
        (SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY noise) FROM spy_r_key WHERE noise IS NOT NULL), 1)
    WHERE noise IS NULL;

    RETURN QUERY
    WITH net AS (
        SELECT network_id,
               sum(e) FILTER (WHERE period = 'now') AS e_now,
               sum(n_all) FILTER (WHERE period = 'now') AS n_now,
               sum(e) FILTER (WHERE period = 'now' AND device_id = phone) AS e_phone,
               sum(e) FILTER (WHERE period = 'now' AND device_id = desktop) AS e_desktop
        FROM spy_r_cov GROUP BY 1
    ),
    netwk AS (
        SELECT network_id, wk, sum(e) AS e, sum(n_all) AS n FROM spy_r_cov WHERE period = 'usual' GROUP BY 1, 2
    ),
    kusual AS (
        SELECT k.key, k.network_id, sum(w.e) AS e_u, sum(w.n) AS n_u
        FROM spy_r_key k
        JOIN spy_r_elig el ON el.key = k.key
        JOIN netwk w ON w.network_id = k.network_id AND w.wk = el.wk
        GROUP BY 1, 2
    ),
    m AS (
        SELECT k.*, n.e_now, n.n_now, n.e_phone, n.e_desktop, u.e_u, u.n_u,
               CASE WHEN k.den > 0 THEN k.num / k.den END AS mh,
               CASE WHEN k.num > 0 AND k.den > 0 THEN k.noise * k.v / (k.num * k.den) END AS var_ln,
               CASE WHEN k.v > 0 THEN (k.num - k.den) / sqrt(k.noise * k.v) END AS zs
        FROM spy_r_key k
        LEFT JOIN net n USING (network_id)
        LEFT JOIN kusual u USING (key, network_id)
    ),
    lh AS (
        SELECT m.*,
               -- One side 0 (not seen in the range, or not in its usual
               -- periods, on the publishers checked in both): the other end
               -- of the range from the chance of seeing nothing, in clumps
               -- of its noise (Poisson, one-sided 5%).
               CASE WHEN m.var_ln > 0 THEN m.mh * exp(-s_z * LEAST(sqrt(m.var_ln), 20))
                    WHEN m.num = 0 AND m.den > 0 THEN 0
                    WHEN m.den = 0 AND m.num > 0 THEN m.reach0 / (m.noise * ln(2 / erfc(s_z / sqrt(2)))) END AS lo,
               CASE WHEN m.var_ln > 0 THEN m.mh * exp(s_z * LEAST(sqrt(m.var_ln), 20))
                    WHEN m.num = 0 AND m.den > 0 THEN m.noise * ln(2 / erfc(s_z / sqrt(2))) / NULLIF(m.reach1, 0) END AS hi
        FROM m
    ),
    st AS (
        SELECT lh.*,
               -- The test statistic: Wald on ln M when M is a number, the
               -- score test (the same numerator) when one side is 0.
               CASE WHEN lh.var_ln > 0 THEN ln(lh.mh) / sqrt(lh.var_ln) ELSE lh.zs END AS z,
               CASE WHEN lh.periods = 0 THEN 'new'
                    -- Sightings count in clumps of its noise.
                    WHEN lh.zs IS NULL OR lh.ab_shared / lh.noise < s_min THEN 'too_little'
                    WHEN lh.var_ln > 0 AND 2 * s_z * sqrt(lh.var_ln) > ln(s_width) THEN 'too_little'
                    WHEN lh.num = 0 AND NOT COALESCE(lh.hi <= s_fall, FALSE) THEN 'too_little'
                    WHEN lh.den = 0 AND NOT COALESCE(lh.lo >= s_rise, FALSE) THEN 'too_little'
               END AS state
        FROM lh
    ),
    wd AS (
        SELECT st.*, CASE WHEN st.state IS NULL THEN erfc(abs(st.z) / sqrt(2)) END AS p
        FROM st
    ),
    bh AS (
        SELECT wd.*,
               row_number() OVER (PARTITION BY wd.network_id ORDER BY wd.p) AS p_rank,
               count(wd.p) OVER (PARTITION BY wd.network_id) AS p_count
        FROM wd
    ),
    bh2 AS (
        SELECT bh.*,
               max(bh.p) FILTER (WHERE bh.p <= bh.p_rank * s_fdr / NULLIF(bh.p_count, 0)) OVER (PARTITION BY bh.network_id) AS p_cut,
               -- Spread of real changes: observed spread minus the noise in it.
               GREATEST(0, COALESCE(var_samp(ln(NULLIF(bh.mh, 0))) FILTER (WHERE bh.state IS NULL AND bh.var_ln > 0)
                                        OVER (PARTITION BY bh.network_id), 0)
                           - COALESCE(avg(bh.var_ln) FILTER (WHERE bh.state IS NULL AND bh.var_ln > 0)
                                          OVER (PARTITION BY bh.network_id), 0)) AS tau2
        FROM bh
    ),
    fin AS (
        SELECT bh2.*,
               CASE WHEN bh2.state IS NULL AND bh2.var_ln > 0
                    THEN ln(bh2.mh) * bh2.tau2 / (bh2.tau2 + bh2.var_ln) END AS shrunk,
               CASE
                   WHEN bh2.state IS NOT NULL THEN bh2.state
                   WHEN bh2.p <= bh2.p_cut AND bh2.z > 0 AND (bh2.mh IS NULL OR bh2.mh >= s_rise) THEN 'rising'
                   WHEN bh2.p <= bh2.p_cut AND bh2.z < 0 AND bh2.mh <= s_fall THEN 'fading'
                   WHEN bh2.lo >= s_fall AND bh2.hi <= s_rise THEN 'steady'
                   ELSE 'unclear'
               END AS word
        FROM bh2
    )
    SELECT f.key, f.network_id, f.a, f.b, f.e_now::bigint, COALESCE(f.e_u, 0)::bigint,
           round((100.0 * f.a / NULLIF(f.e_now, 0))::numeric, 4),
           CASE WHEN f.periods > 0 THEN round((100.0 * f.b / NULLIF(f.e_u, 0))::numeric, 4) END,
           round((100.0 * f.a_phone / NULLIF(f.e_phone, 0))::numeric, 4),
           round((100.0 * f.a_desktop / NULLIF(f.e_desktop, 0))::numeric, 4),
           round((100.0 * f.a / NULLIF(f.n_now, 0))::numeric, 4),
           CASE WHEN f.periods > 0 THEN round((100.0 * f.b / NULLIF(f.n_u, 0))::numeric, 4) END,
           CASE WHEN f.periods > 0 AND f.n_now > 0 AND f.n_u > 0
                THEN round((100.0 * f.a / f.n_now - 100.0 * f.b / f.n_u)::numeric, 4) END,
           CASE WHEN f.a > 0 THEN (rank() OVER (PARTITION BY f.network_id ORDER BY f.a DESC))::int END,
           CASE WHEN f.state IS NULL THEN round(f.mh::numeric, 3) END,
           CASE WHEN f.state IS NULL THEN round(f.lo::numeric, 3) END,
           CASE WHEN f.state IS NULL THEN round(f.hi::numeric, 3) END,
           f.word,
           CASE WHEN f.word IN ('rising', 'fading') THEN 'clear'
                WHEN f.word = 'unclear' AND abs(f.z) >= s_z THEN 'likely' END,
           CASE WHEN f.state IS NULL THEN (row_number() OVER (
               PARTITION BY f.network_id, f.state IS NULL
               ORDER BY f.shrunk DESC NULLS LAST, f.z DESC NULLS LAST, f.a DESC))::int END,
           round(f.noise::numeric, 2),
           f.periods::int,
           f.pubs::int,
           f.first_seen
    FROM fin f;
END;
$$;

-- Creatives over a range: the range numbers, plus share of voice in the
-- creative's vertical, Size's scaled, and lifespan against its vertical.
CREATE TYPE spy.creative_range_row AS (
    creative_id INTEGER,
    network_id INTEGER,
    sightings BIGINT,
    sightings_usual BIGINT,
    checks BIGINT,
    checks_usual BIGINT,
    presence NUMERIC,
    presence_usual NUMERIC,
    phone_presence NUMERIC,
    desktop_presence NUMERIC,
    share_pct NUMERIC,
    share_usual_pct NUMERIC,
    share_gain_pts NUMERIC,
    rank INTEGER,
    momentum NUMERIC,
    momentum_low NUMERIC,
    momentum_high NUMERIC,
    momentum_word TEXT,
    momentum_sure TEXT,
    momentum_rank INTEGER,
    noise NUMERIC,
    usual_periods INTEGER,
    publishers INTEGER,
    first_seen_at TIMESTAMPTZ,
    vertical TEXT,                   -- Direction's vertical (creative_vertical.vertical)
    vertical_share_pct NUMERIC,      -- share of voice in its vertical, this network
    vertical_rank INTEGER,
    scaled BOOLEAN,                  -- among the few that make half its vertical (Size)
    last_seen_at TIMESTAMPTZ,        -- as of the range's end
    running BOOLEAN,                 -- seen in the ended_hours before the range's end
    lifespan_days INTEGER,           -- first sighting to last (or to the range's end, if running)
    lifespan_pct NUMERIC,            -- share of its curve's creatives that ended younger (Kaplan-Meier)
    lifespan_curve TEXT,             -- the vertical it was compared with, '*' = all creatives
    operator_id INTEGER,
    is_junk BOOLEAN
);

-- Last sighting of each creative per UTC day, from the daily counts. Kept by
-- spy.refresh_read_model(); lifespan reads it for any past moment.
CREATE TABLE spy.creative_day (
    creative_id INTEGER NOT NULL,
    day DATE NOT NULL,
    sightings BIGINT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (creative_id, day)
);
CREATE INDEX creative_day_day_idx ON spy.creative_day (day);

-- A creative's life as of p_at: when it was last seen before p_at, whether
-- it is still running then, and how many whole days it ran (to its last
-- sighting, or to p_at while running).
CREATE FUNCTION spy.creative_life(p_creative_id INTEGER, p_first_seen TIMESTAMPTZ, p_at TIMESTAMPTZ,
                                  p_ended INTERVAL, OUT last_seen_at TIMESTAMPTZ, OUT running BOOLEAN,
                                  OUT days INTEGER)
LANGUAGE plpgsql STABLE AS $$
BEGIN
    SELECT max(LEAST(d.last_seen_at, p_at)) INTO last_seen_at FROM spy.creative_day d
    WHERE d.creative_id = p_creative_id AND d.day <= (p_at AT TIME ZONE 'UTC')::date AND d.first_seen_at < p_at;
    last_seen_at := COALESCE(last_seen_at, LEAST(p_first_seen, p_at));
    running := last_seen_at >= p_at - p_ended;
    days := GREATEST(0, floor(extract(epoch FROM
                (CASE WHEN running THEN p_at ELSE last_seen_at END) - p_first_seen) / 86400))::int;
END;
$$;

-- Lifespan curves as of p_at (Kaplan-Meier): of the creatives first seen in
-- the lifespan_days before p_at, the share still running after each whole
-- day. Creatives still running count as running, not as ended. One curve
-- per vertical with lifespan_min_group creatives or more, and '*' for all.
CREATE FUNCTION spy.lifespan_curve(p_at TIMESTAMPTZ)
RETURNS TABLE (curve TEXT, day INTEGER, surviving FLOAT8, creatives INTEGER)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    cfg JSONB := spy.cfg();
    s_days INTEGER := (cfg->>'lifespan_days')::int;
    s_ended INTERVAL := make_interval(hours => (cfg->>'ended_hours')::int);
    s_min INTEGER := (cfg->>'lifespan_min_group')::int;
BEGIN
    RETURN QUERY
    WITH pop AS (
        SELECT COALESCE(cv.vertical, '*') AS vert, l.running AS run, l.days AS d
        FROM tracks_api.creative_v1 c
        LEFT JOIN spy.creative_vertical cv ON cv.creative_id = c.id
        CROSS JOIN LATERAL spy.creative_life(c.id, c.first_seen_at, p_at, s_ended) l
        WHERE c.first_seen_at >= p_at - make_interval(days => s_days) AND c.first_seen_at < p_at
          AND NOT EXISTS (SELECT 1 FROM spy.creative_stats cs WHERE cs.creative_id = c.id AND cs.is_junk)
    ),
    member AS (
        SELECT x.vert AS c, x.d, x.run FROM (
            SELECT p.*, count(*) OVER (PARTITION BY p.vert) AS size FROM pop p WHERE p.vert <> '*') x
        WHERE x.size >= s_min
        UNION ALL
        SELECT '*', p.d, p.run FROM pop p
    ),
    steps AS (
        SELECT m.c, m.d, count(*) FILTER (WHERE NOT m.run) AS ended, count(*) AS gone_after
        FROM member m GROUP BY 1, 2
    ),
    km AS (
        -- At risk on day d: everyone who lasted d days or more.
        SELECT s.c, s.d, s.ended, sum(s.gone_after) OVER (PARTITION BY s.c ORDER BY s.d DESC) AS at_risk,
               sum(s.gone_after) OVER (PARTITION BY s.c) AS size
        FROM steps s
    )
    SELECT km.c, km.d,
           CASE WHEN bool_or(km.ended >= km.at_risk) OVER w THEN 0
                ELSE exp(sum(ln(NULLIF(1 - km.ended::float8 / km.at_risk, 0))) OVER w) END,
           km.size::int
    FROM km
    WINDOW w AS (PARTITION BY km.c ORDER BY km.d);
END;
$$;

CREATE FUNCTION spy.creative_range(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_usual_from TIMESTAMPTZ DEFAULT NULL,
                                   p_usual_to TIMESTAMPTZ DEFAULT NULL, p_now TIMESTAMPTZ DEFAULT now())
RETURNS SETOF spy.creative_range_row
LANGUAGE plpgsql AS $$
DECLARE
    cfg JSONB := spy.cfg();
    s_scaled_share NUMERIC := (cfg->>'scaled_cum_share_pct')::numeric;
    s_scaled_rank NUMERIC := (cfg->>'scaled_max_rank')::numeric;
    s_scaled_min NUMERIC := (cfg->>'scaled_min_sightings_7d')::numeric;
    s_ended INTERVAL := make_interval(hours => (cfg->>'ended_hours')::int);
    range_end TIMESTAMPTZ;
BEGIN
    CREATE TEMP TABLE IF NOT EXISTS spy_r_creative OF spy.range_row ON COMMIT DROP;
    TRUNCATE spy_r_creative;
    INSERT INTO spy_r_creative SELECT * FROM spy.subject_range('creative', p_from, p_to, p_usual_from, p_usual_to, p_now);
    SELECT to_at INTO range_end FROM spy_r_plan WHERE period = 'now';
    CREATE TEMP TABLE IF NOT EXISTS spy_r_curve (curve TEXT, day INTEGER, surviving FLOAT8, creatives INTEGER,
        PRIMARY KEY (curve, day)) ON COMMIT DROP;
    TRUNCATE spy_r_curve;
    INSERT INTO spy_r_curve SELECT * FROM spy.lifespan_curve(range_end);

    RETURN QUERY
    WITH r AS (
        SELECT r.*, cv.vertical AS vert,
               sum(r.sightings) OVER (PARTITION BY r.network_id, cv.vertical) AS vert_total
        FROM spy_r_creative r
        LEFT JOIN spy.creative_vertical cv ON cv.creative_id = r.key::int
    ),
    v AS (
        SELECT r.*,
               CASE WHEN r.vert IS NOT NULL AND r.sightings > 0
                    THEN (rank() OVER (PARTITION BY r.network_id, r.vert ORDER BY r.sightings DESC))::int END AS vrank,
               COALESCE(sum(r.sightings) OVER (PARTITION BY r.network_id, r.vert ORDER BY r.sightings DESC, r.key
                                               ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING), 0) AS above
        FROM r
    )
    SELECT v.key::int, v.network_id, v.sightings, v.sightings_usual, v.checks, v.checks_usual, v.presence,
           v.presence_usual, v.phone_presence, v.desktop_presence, v.share_pct, v.share_usual_pct,
           v.share_gain_pts, v.rank, v.momentum, v.momentum_low, v.momentum_high, v.momentum_word,
           v.momentum_sure, v.momentum_rank, v.noise, v.usual_periods, v.publishers, v.first_seen_at,
           v.vert,
           CASE WHEN v.vert IS NOT NULL THEN round(100.0 * v.sightings / NULLIF(v.vert_total, 0), 4) END,
           v.vrank,
           COALESCE(v.vert IS NOT NULL AND v.sightings >= s_scaled_min AND v.vrank <= s_scaled_rank
                    AND 100.0 * v.above / NULLIF(v.vert_total, 0) < s_scaled_share, FALSE),
           l.last_seen_at, l.running, l.days,
           round((100 * (1 - COALESCE((SELECT k.surviving FROM spy_r_curve k WHERE k.curve = g.curve AND k.day < l.days
                                       ORDER BY k.day DESC LIMIT 1), 1)))::numeric, 1),
           g.curve,
           cs.operator_id, COALESCE(cs.is_junk, FALSE)
    FROM v
    LEFT JOIN tracks_api.creative_v1 c ON c.id = v.key::int
    LEFT JOIN LATERAL spy.creative_life(c.id, c.first_seen_at, range_end, s_ended) l ON TRUE
    LEFT JOIN LATERAL (SELECT CASE WHEN EXISTS (SELECT 1 FROM spy_r_curve k WHERE k.curve = v.vert)
                                   THEN v.vert ELSE '*' END AS curve) g ON TRUE
    LEFT JOIN spy.creative_stats cs ON cs.creative_id = v.key::int;
END;
$$;

-- Operators over a range: the range numbers, plus launches (creatives first
-- seen in the range) and their hit rate: the share that ran hit_days or
-- more, of those whose outcome is known by p_now, with a 90% Wilson range.
CREATE TYPE spy.operator_range_row AS (
    operator_id INTEGER,
    network_id INTEGER,
    sightings BIGINT,
    sightings_usual BIGINT,
    checks BIGINT,
    checks_usual BIGINT,
    presence NUMERIC,
    presence_usual NUMERIC,
    phone_presence NUMERIC,
    desktop_presence NUMERIC,
    share_pct NUMERIC,
    share_usual_pct NUMERIC,
    share_gain_pts NUMERIC,
    rank INTEGER,
    momentum NUMERIC,
    momentum_low NUMERIC,
    momentum_high NUMERIC,
    momentum_word TEXT,
    momentum_sure TEXT,
    momentum_rank INTEGER,
    noise NUMERIC,
    usual_periods INTEGER,
    publishers INTEGER,
    first_seen_at TIMESTAMPTZ,
    launches INTEGER,                -- its creatives first seen in the range
    hits INTEGER,                    -- of those, ran hit_days or more
    misses INTEGER,                  -- ended sooner
    testing INTEGER,                 -- still running, younger than hit_days: no outcome yet
    hit_rate_pct NUMERIC,            -- hits / (hits + misses), once hit_min_known have an outcome
    hit_rate_low_pct NUMERIC,
    hit_rate_high_pct NUMERIC
);

CREATE FUNCTION spy.operator_range(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_usual_from TIMESTAMPTZ DEFAULT NULL,
                                   p_usual_to TIMESTAMPTZ DEFAULT NULL, p_now TIMESTAMPTZ DEFAULT now())
RETURNS SETOF spy.operator_range_row
LANGUAGE plpgsql AS $$
DECLARE
    cfg JSONB := spy.cfg();
    s_hit INTERVAL := make_interval(days => (cfg->>'hit_days')::int);
    s_ended INTERVAL := make_interval(hours => (cfg->>'ended_hours')::int);
    s_known INTEGER := (cfg->>'hit_min_known')::int;
    z FLOAT8 := (cfg->>'range_z')::float8;
    r_from TIMESTAMPTZ;
    r_to TIMESTAMPTZ;
BEGIN
    CREATE TEMP TABLE IF NOT EXISTS spy_r_operator OF spy.range_row ON COMMIT DROP;
    TRUNCATE spy_r_operator;
    INSERT INTO spy_r_operator SELECT * FROM spy.subject_range('operator', p_from, p_to, p_usual_from, p_usual_to, p_now);
    SELECT from_at, to_at INTO r_from, r_to FROM spy_r_plan WHERE period = 'now';

    RETURN QUERY
    WITH launch AS (
        SELECT cs.operator_id, c.first_seen_at AS f, c.last_seen_at AS l
        FROM tracks_api.creative_v1 c
        JOIN spy.creative_stats cs ON cs.creative_id = c.id
        WHERE c.first_seen_at >= r_from AND c.first_seen_at < r_to
          AND cs.operator_id IS NOT NULL AND NOT cs.is_junk
    ),
    outcome AS (
        SELECT operator_id, count(*) AS n,
               count(*) FILTER (WHERE l - f >= s_hit) AS hits,
               count(*) FILTER (WHERE l - f < s_hit AND l < p_now - s_ended) AS misses
        FROM launch GROUP BY 1
    ),
    w AS (
        SELECT o.*, o.hits + o.misses AS k,
               o.hits::float8 / NULLIF(o.hits + o.misses, 0) AS ph
        FROM outcome o
    )
    SELECT r.key::int, r.network_id, r.sightings, r.sightings_usual, r.checks, r.checks_usual, r.presence,
           r.presence_usual, r.phone_presence, r.desktop_presence, r.share_pct, r.share_usual_pct,
           r.share_gain_pts, r.rank, r.momentum, r.momentum_low, r.momentum_high, r.momentum_word,
           r.momentum_sure, r.momentum_rank, r.noise, r.usual_periods, r.publishers, r.first_seen_at,
           COALESCE(w.n, 0)::int, COALESCE(w.hits, 0)::int, COALESCE(w.misses, 0)::int,
           COALESCE(w.n - w.hits - w.misses, 0)::int,
           CASE WHEN w.k >= s_known THEN round((100 * w.ph)::numeric, 1) END,
           CASE WHEN w.k >= s_known THEN round((100 * GREATEST(0,
               (w.ph + z * z / (2 * w.k) - z * sqrt(w.ph * (1 - w.ph) / w.k + z * z / (4.0 * w.k * w.k)))
               / (1 + z * z / w.k)))::numeric, 1) END,
           CASE WHEN w.k >= s_known THEN round((100 * LEAST(1,
               (w.ph + z * z / (2 * w.k) + z * sqrt(w.ph * (1 - w.ph) / w.k + z * z / (4.0 * w.k * w.k)))
               / (1 + z * z / w.k)))::numeric, 1) END
    FROM spy_r_operator r
    LEFT JOIN w ON w.operator_id = r.key::int;
END;
$$;

-- Publishers over a range: checks, sightings per 100 checks (how many ads
-- a check shows), share of the network's sightings, and how concentrated
-- its operators are, as the number of operators that would split it evenly
-- (1 / HHI).
CREATE FUNCTION spy.publisher_range(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_now TIMESTAMPTZ DEFAULT now())
RETURNS TABLE (publisher_id INTEGER, network_id INTEGER, checks BIGINT, sightings BIGINT, per_100_checks NUMERIC,
               share_pct NUMERIC, hours_checked INTEGER, operators INTEGER, even_operators NUMERIC)
LANGUAGE plpgsql STABLE AS $$
BEGIN
    RETURN QUERY
    WITH plan AS (
        SELECT * FROM spy.range_plan(p_from, p_to, NULL, NULL, p_now) WHERE period = 'now'
    ),
    cov AS (
        SELECT c.publisher_id, pb.network_id, sum(c.scrapes) AS e, sum(c.sightings) AS n,
               count(DISTINCT c.hour) FILTER (WHERE c.scrapes > 0) AS hours
        FROM plan p
        JOIN tracks_api.scrape_coverage_v2 c ON c.closed AND c.hour >= p.from_at AND c.hour < p.to_at
        JOIN tracks_api.publisher_v1 pb ON pb.id = c.publisher_id
        GROUP BY 1, 2
    ),
    acc AS (
        SELECT d.publisher_id, d.account_id, d.sightings
        FROM plan p JOIN tracks_api.ad_account_daily_v1 d ON d.day >= p.day_from AND d.day < p.day_to
        UNION ALL
        SELECT h.publisher_id, h.account_id, h.sightings
        FROM plan p JOIN tracks_api.ad_account_brand_hourly_v1 h ON h.hour >= p.head_from AND h.hour < p.head_to
        UNION ALL
        SELECT h.publisher_id, h.account_id, h.sightings
        FROM plan p JOIN tracks_api.ad_account_brand_hourly_v1 h ON h.hour >= p.tail_from AND h.hour < p.tail_to
    ),
    ops AS (
        SELECT x.publisher_id, count(*) AS n, 1 / NULLIF(sum((x.s / x.t) ^ 2), 0) AS even
        FROM (
            SELECT a.publisher_id, ao.operator_id, sum(a.sightings)::float8 AS s,
                   sum(sum(a.sightings)) OVER (PARTITION BY a.publisher_id)::float8 AS t
            FROM acc a JOIN spy.account_operator ao ON ao.account_id = a.account_id
            GROUP BY 1, 2
        ) x
        GROUP BY 1
    )
    SELECT c.publisher_id, c.network_id::int, c.e::bigint, c.n::bigint,
           round(100.0 * c.n / NULLIF(c.e, 0), 4),
           round(100.0 * c.n / NULLIF(sum(c.n) OVER (PARTITION BY c.network_id), 0), 4),
           c.hours::int, COALESCE(o.n, 0)::int, round(o.even::numeric, 1)
    FROM cov c LEFT JOIN ops o USING (publisher_id);
END;
$$;
