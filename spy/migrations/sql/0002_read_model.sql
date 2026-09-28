-- lint: new-table
-- The read model: one row per creative, operator and publisher with the
-- facts the lists filter and sort on (counts, who and what, first and last
-- seen), so the app never adds up counts itself. Ported from the
-- collector's spy.refresh_read_model() (018, 019, 026, 030, 034), reading
-- tracks_api instead of the collector's tables. Windows are UTC days
-- including today: 7d is today and the 6 days before. Rebuilt by
-- spy.refresh_read_model() every 5 minutes.
--
-- Judgements are not here: momentum, direction and anything per check come
-- from the range functions (0003) and Direction (0004), which account for
-- how often each publisher was checked. The collector's stage, momentum_pct,
-- phone share, average feed position and drop from peak were raw counts
-- that move with check volume (spy/METRICS.md), so they are gone.

-- Placeholder ads Taboola shows when it has nothing to sell: fallback images,
-- banner slots and filler headlines. They stay in the data but the apps hide
-- them, and Direction leaves them out.
CREATE FUNCTION spy.is_junk_ad(p_headline TEXT, p_image_url TEXT, p_creative_key TEXT)
RETURNS BOOLEAN LANGUAGE sql IMMUTABLE
AS $$
    SELECT COALESCE(p_creative_key, '') ILIKE '%fallback%'
        OR COALESCE(p_image_url, '') ILIKE '%fallback%'
        OR COALESCE(p_image_url, '') ILIKE '%/banner/%'
        OR COALESCE(p_headline, '') ~* '^(title|headline)_[-0-9]+'
        OR lower(btrim(COALESCE(p_headline, ''))) IN (
            'for further reading:', 'click here for more information', 'want to know more? click here',
            'would you like to know more?', 'this might be relevant for you', 'this may be of interest to you!',
            'recommended reading for you', 'additional resources on this topic', 'further insights on this subject',
            'brought to you by', 'you might be interested', 'you might be interested in the content above',
            'explore related insights', 'discover more context below', 'expand your knowledge here',
            'default banner title', 'default title', '')
$$;

CREATE TABLE spy.creative_stats (
    creative_id INTEGER PRIMARY KEY,
    sightings_total BIGINT NOT NULL,
    sightings_today INTEGER NOT NULL,
    sightings_yesterday INTEGER NOT NULL,
    sightings_3d INTEGER NOT NULL,
    sightings_prev_3d INTEGER NOT NULL,
    sightings_7d INTEGER NOT NULL,
    sightings_prev_7d INTEGER NOT NULL,
    sightings_30d INTEGER NOT NULL,
    scrapes_total BIGINT NOT NULL,
    active_days INTEGER NOT NULL,              -- days with at least one sighting
    ads_count INTEGER NOT NULL,                -- headlines used with this creative
    publishers_count INTEGER NOT NULL,
    publisher_ids INTEGER[] NOT NULL,          -- most sightings first
    network_share_7d_pct NUMERIC(7, 4) NOT NULL,  -- share of voice: of all sightings in the last 7 days
    is_new BOOLEAN NOT NULL,                   -- first seen in the last new_days days
    running BOOLEAN NOT NULL,                  -- seen in the last ended_hours hours
    top_ad_id INTEGER,
    headline TEXT,                             -- of the ad with the most sightings
    brand_id INTEGER,                          -- of the ad seen last
    operator_id INTEGER,                       -- of the account behind most sightings
    account_ids INTEGER[] NOT NULL,            -- most sightings first
    trackers TEXT[] NOT NULL,
    affiliate_networks TEXT[] NOT NULL,
    vertical TEXT,                             -- spy.creative_vertical.shown_vertical
    classified_vertical TEXT,                  -- spy.creative_vertical.vertical: health, finance, ...
    subvertical TEXT,
    vertical_confidence NUMERIC(3, 2) NOT NULL,
    unsure BOOLEAN NOT NULL,                   -- below 0.6, or not classified yet
    vertical_source TEXT,
    health_from_funnel BOOLEAN NOT NULL,
    is_junk BOOLEAN NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    refreshed_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX creative_stats_7d_idx ON spy.creative_stats (sightings_7d DESC);
CREATE INDEX creative_stats_last_seen_idx ON spy.creative_stats (last_seen_at DESC);
CREATE INDEX creative_stats_first_seen_idx ON spy.creative_stats (first_seen_at DESC);
CREATE INDEX creative_stats_share_idx ON spy.creative_stats (network_share_7d_pct DESC);
CREATE INDEX creative_stats_operator_idx ON spy.creative_stats (operator_id);
CREATE INDEX creative_stats_vertical_idx ON spy.creative_stats (vertical);
CREATE INDEX creative_stats_publishers_idx ON spy.creative_stats USING gin (publisher_ids);
CREATE INDEX creative_stats_accounts_idx ON spy.creative_stats USING gin (account_ids);

CREATE TABLE spy.operator_stats (
    operator_id INTEGER PRIMARY KEY REFERENCES spy.operator(id) ON DELETE CASCADE,
    sightings_total BIGINT NOT NULL,
    sightings_today INTEGER NOT NULL,
    sightings_7d INTEGER NOT NULL,
    sightings_prev_7d INTEGER NOT NULL,
    sightings_30d INTEGER NOT NULL,
    share_today_pct NUMERIC(7, 4) NOT NULL,
    share_7d_pct NUMERIC(7, 4) NOT NULL,
    share_30d_pct NUMERIC(7, 4) NOT NULL,
    creatives_count INTEGER NOT NULL,
    live_creatives_count INTEGER NOT NULL,     -- running
    publisher_ids INTEGER[] NOT NULL,          -- most sightings first
    vertical TEXT,                             -- the operator's label, else its creatives' most common
    brands TEXT[] NOT NULL,                    -- most sightings first, at most 10
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    refreshed_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX operator_stats_30d_idx ON spy.operator_stats (share_30d_pct DESC);

CREATE TABLE spy.publisher_stats (
    publisher_id INTEGER PRIMARY KEY,
    sightings_total BIGINT NOT NULL,
    sightings_today INTEGER NOT NULL,
    sightings_7d INTEGER NOT NULL,
    sightings_30d INTEGER NOT NULL,
    share_30d_pct NUMERIC(7, 4) NOT NULL,
    scrapes_30d INTEGER NOT NULL,
    phone_sightings_30d INTEGER NOT NULL,
    creatives_30d INTEGER NOT NULL,
    operators_30d INTEGER NOT NULL,
    operator_hhi NUMERIC(7, 1) NOT NULL,       -- sum of squared operator shares (0-10000), last 30 days
    even_operators NUMERIC(7, 1),              -- 10000 / operator_hhi: as concentrated as this many equal operators
    top_operators JSONB NOT NULL,              -- [{operator_id, sightings, share_pct}], at most 10
    top_creatives JSONB NOT NULL,              -- [{creative_id, sightings}], at most 8
    verticals JSONB NOT NULL,                  -- [{vertical, sightings, share_pct}]
    first_seen_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ,
    refreshed_at TIMESTAMPTZ NOT NULL
);

-- Rebuilds the three as of p_now. Creatives first: operators and publishers
-- read creative_stats. Returns the creatives written.
CREATE FUNCTION spy.refresh_read_model(p_now TIMESTAMPTZ DEFAULT now()) RETURNS INTEGER
LANGUAGE plpgsql AS $$
DECLARE
    today DATE := (p_now AT TIME ZONE 'UTC')::date;
    phone SMALLINT := (SELECT id FROM tracks_api.device_v1 WHERE code = 'phone');
    network_today BIGINT;
    network_7d BIGINT;
    network_30d BIGINT;
    new_for INTERVAL := make_interval(days => (spy.cfg()->>'new_days')::int);
    ended_after INTERVAL := make_interval(hours => (spy.cfg()->>'ended_hours')::int);
    last_days TIMESTAMPTZ;
    redo DATE[];
    n INTEGER;
BEGIN
    IF NOT pg_try_advisory_xact_lock(hashtext('spy.refresh_read_model')) THEN
        RETURN 0;
    END IF;

    -- Shares divide daily counts by scrape coverage; the two close at slightly
    -- different moments, so a share is capped at 100.
    SELECT GREATEST(COALESCE(sum(sightings) FILTER (WHERE hour >= today::timestamp AT TIME ZONE 'UTC'), 0), 1),
           GREATEST(COALESCE(sum(sightings) FILTER (WHERE hour >= (today - 6)::timestamp AT TIME ZONE 'UTC'), 0), 1),
           GREATEST(COALESCE(sum(sightings), 0), 1)
    INTO network_today, network_7d, network_30d
    FROM tracks_api.scrape_coverage_v2 WHERE hour >= (today - 29)::timestamp AT TIME ZONE 'UTC';

    -- Creatives ----------------------------------------------------------------
    INSERT INTO spy.creative_stats AS cs (
        creative_id, sightings_total, sightings_today, sightings_yesterday, sightings_3d, sightings_prev_3d,
        sightings_7d, sightings_prev_7d, sightings_30d, scrapes_total, active_days, ads_count,
        publishers_count, publisher_ids, network_share_7d_pct, is_new, running, top_ad_id, headline, brand_id, operator_id,
        account_ids, trackers, affiliate_networks, vertical, classified_vertical, subvertical,
        vertical_confidence, unsure, vertical_source, health_from_funnel, is_junk,
        first_seen_at, last_seen_at, refreshed_at
    )
    WITH per_pub AS (
        SELECT creative_id, publisher_id, sum(sightings) AS s
        FROM tracks_api.ad_daily_v1 GROUP BY 1, 2
    ),
    pubs AS (
        SELECT creative_id, count(*) AS n, array_agg(publisher_id ORDER BY s DESC) AS ids
        FROM per_pub GROUP BY 1
    ),
    per_ad AS (
        SELECT creative_id, ad_id, sum(sightings) AS s
        FROM tracks_api.ad_daily_v1 GROUP BY 1, 2
    ),
    top_ad AS (
        SELECT DISTINCT ON (pa.creative_id) pa.creative_id, pa.ad_id, a.headline
        FROM per_ad pa JOIN tracks_api.ad_v1 a ON a.id = pa.ad_id
        ORDER BY pa.creative_id, pa.s DESC, pa.ad_id
    ),
    -- Each sighting counts for the account that paid for it.
    per_account AS (
        SELECT creative_id, account_id, sum(sightings) AS s
        FROM tracks_api.ad_account_daily_v1
        WHERE account_id IS NOT NULL
        GROUP BY 1, 2
    ),
    accounts AS (
        SELECT creative_id, array_agg(account_id ORDER BY s DESC) AS ids
        FROM per_account GROUP BY 1
    ),
    top_account AS (
        SELECT DISTINCT ON (x.creative_id) x.creative_id, ao.operator_id
        FROM per_account x LEFT JOIN spy.account_operator ao ON ao.account_id = x.account_id
        ORDER BY x.creative_id, x.s DESC
    ),
    latest_brand AS (
        SELECT DISTINCT ON (creative_id) creative_id, brand_id
        FROM tracks_api.ad_v1 WHERE brand_id IS NOT NULL
        ORDER BY creative_id, last_seen_at DESC
    ),
    links AS (
        SELECT cl.creative_id,
               array_agg(DISTINCT l.tracker) FILTER (WHERE NULLIF(l.tracker, '') IS NOT NULL) AS trackers,
               array_agg(DISTINCT l.affiliate_network) FILTER (WHERE NULLIF(l.affiliate_network, '') IS NOT NULL) AS nets
        FROM (SELECT DISTINCT creative_id, link_id FROM tracks_api.creative_link_daily_v1) cl
        JOIN tracks_api.link_v1 l ON l.id = cl.link_id
        GROUP BY 1
    ),
    agg AS (
        SELECT
            d.creative_id,
            sum(d.sightings) AS total,
            sum(d.sightings) FILTER (WHERE d.day = today) AS s_today,
            sum(d.sightings) FILTER (WHERE d.day = today - 1) AS s_yesterday,
            sum(d.sightings) FILTER (WHERE d.day > today - 3) AS s_3d,
            sum(d.sightings) FILTER (WHERE d.day > today - 6 AND d.day <= today - 3) AS s_prev_3d,
            sum(d.sightings) FILTER (WHERE d.day > today - 7) AS s_7d,
            sum(d.sightings) FILTER (WHERE d.day > today - 14 AND d.day <= today - 7) AS s_prev_7d,
            sum(d.sightings) FILTER (WHERE d.day > today - 30) AS s_30d,
            sum(d.scrapes) AS scrapes,
            count(DISTINCT d.day) AS days,
            count(DISTINCT d.ad_id) AS ads,
            min(d.first_seen_at) AS first_seen,
            max(d.last_seen_at) AS last_seen
        FROM tracks_api.ad_daily_v1 d
        GROUP BY d.creative_id
    ),
    base AS (
        SELECT a.* FROM agg a
    )
    SELECT
        b.creative_id, b.total, COALESCE(b.s_today, 0), COALESCE(b.s_yesterday, 0),
        COALESCE(b.s_3d, 0), COALESCE(b.s_prev_3d, 0), COALESCE(b.s_7d, 0), COALESCE(b.s_prev_7d, 0),
        COALESCE(b.s_30d, 0), b.scrapes, b.days, b.ads, p.n, p.ids,
        LEAST(100, round(100.0 * COALESCE(b.s_7d, 0) / network_7d, 4)),
        c.first_seen_at >= p_now - new_for,
        b.last_seen >= p_now - ended_after,
        tad.ad_id, tad.headline, lb.brand_id, ta.operator_id,
        COALESCE(acc.ids, '{}'), COALESCE(lk.trackers, '{}'), COALESCE(lk.nets, '{}'),
        cv.shown_vertical, cv.vertical, cv.subvertical, COALESCE(cv.confidence, 0), COALESCE(cv.unsure, TRUE),
        cv.source, COALESCE(cv.health_from_funnel, FALSE),
        spy.is_junk_ad(tad.headline, c.image_url, c.creative_key),
        b.first_seen, b.last_seen, p_now
    FROM base b
    JOIN tracks_api.creative_v1 c ON c.id = b.creative_id
    JOIN pubs p USING (creative_id)
    LEFT JOIN top_ad tad USING (creative_id)
    LEFT JOIN top_account ta USING (creative_id)
    LEFT JOIN latest_brand lb USING (creative_id)
    LEFT JOIN accounts acc USING (creative_id)
    LEFT JOIN links lk USING (creative_id)
    LEFT JOIN spy.creative_vertical cv USING (creative_id)
    ON CONFLICT (creative_id) DO UPDATE SET
        sightings_total = EXCLUDED.sightings_total,
        sightings_today = EXCLUDED.sightings_today,
        sightings_yesterday = EXCLUDED.sightings_yesterday,
        sightings_3d = EXCLUDED.sightings_3d,
        sightings_prev_3d = EXCLUDED.sightings_prev_3d,
        sightings_7d = EXCLUDED.sightings_7d,
        sightings_prev_7d = EXCLUDED.sightings_prev_7d,
        sightings_30d = EXCLUDED.sightings_30d,
        scrapes_total = EXCLUDED.scrapes_total,
        active_days = EXCLUDED.active_days,
        ads_count = EXCLUDED.ads_count,
        publishers_count = EXCLUDED.publishers_count,
        publisher_ids = EXCLUDED.publisher_ids,
        network_share_7d_pct = EXCLUDED.network_share_7d_pct,
        is_new = EXCLUDED.is_new,
        running = EXCLUDED.running,
        top_ad_id = EXCLUDED.top_ad_id,
        headline = EXCLUDED.headline,
        brand_id = EXCLUDED.brand_id,
        operator_id = EXCLUDED.operator_id,
        account_ids = EXCLUDED.account_ids,
        trackers = EXCLUDED.trackers,
        affiliate_networks = EXCLUDED.affiliate_networks,
        vertical = EXCLUDED.vertical,
        classified_vertical = EXCLUDED.classified_vertical,
        subvertical = EXCLUDED.subvertical,
        vertical_confidence = EXCLUDED.vertical_confidence,
        unsure = EXCLUDED.unsure,
        vertical_source = EXCLUDED.vertical_source,
        health_from_funnel = EXCLUDED.health_from_funnel,
        is_junk = EXCLUDED.is_junk,
        first_seen_at = EXCLUDED.first_seen_at,
        last_seen_at = EXCLUDED.last_seen_at,
        refreshed_at = EXCLUDED.refreshed_at;
    GET DIAGNOSTICS n = ROW_COUNT;

    -- Operators ----------------------------------------------------------------
    -- Each sighting counts for the account that paid for it, with the brand
    -- it was shown with (the ad's last brand when the sighting had none).
    CREATE TEMP TABLE IF NOT EXISTS spy_op_day (operator_id INTEGER, day DATE, publisher_id INTEGER,
                                                device_id SMALLINT, brand_id INTEGER, sightings BIGINT,
                                                first_seen TIMESTAMPTZ, last_seen TIMESTAMPTZ) ON COMMIT DROP;
    TRUNCATE spy_op_day;
    INSERT INTO spy_op_day
    SELECT ao.operator_id, d.day, d.publisher_id, d.device_id, COALESCE(d.brand_id, a.brand_id), sum(d.sightings),
           min(d.first_seen_at), max(d.last_seen_at)
    FROM tracks_api.ad_account_daily_v1 d
    JOIN spy.account_operator ao ON ao.account_id = d.account_id
    JOIN tracks_api.ad_v1 a ON a.id = d.ad_id
    GROUP BY 1, 2, 3, 4, 5;
    ANALYZE spy_op_day;

    DELETE FROM spy.operator_stats os
    WHERE NOT EXISTS (SELECT 1 FROM spy_op_day x WHERE x.operator_id = os.operator_id);

    INSERT INTO spy.operator_stats AS os (
        operator_id, sightings_total, sightings_today, sightings_7d, sightings_prev_7d, sightings_30d,
        share_today_pct, share_7d_pct, share_30d_pct, creatives_count, live_creatives_count,
        publisher_ids, vertical, brands, first_seen_at, last_seen_at, refreshed_at
    )
    WITH agg AS (
        SELECT operator_id,
               sum(sightings) AS total,
               sum(sightings) FILTER (WHERE day = today) AS s_today,
               sum(sightings) FILTER (WHERE day > today - 7) AS s_7d,
               sum(sightings) FILTER (WHERE day > today - 14 AND day <= today - 7) AS s_prev_7d,
               sum(sightings) FILTER (WHERE day > today - 30) AS s_30d,
               min(first_seen) AS first_seen, max(last_seen) AS last_seen
        FROM spy_op_day GROUP BY 1
    ),
    pubs AS (
        SELECT operator_id, array_agg(publisher_id ORDER BY s DESC) AS ids
        FROM (SELECT operator_id, publisher_id, sum(sightings) AS s FROM spy_op_day GROUP BY 1, 2) x
        GROUP BY 1
    ),
    brands AS (
        SELECT operator_id, (array_agg(b.name ORDER BY s DESC))[1:10] AS names
        FROM (SELECT operator_id, brand_id, sum(sightings) AS s FROM spy_op_day
              WHERE brand_id IS NOT NULL GROUP BY 1, 2) x
        JOIN tracks_api.brand_v1 b ON b.id = x.brand_id
        GROUP BY 1
    ),
    creatives AS (
        SELECT operator_id, count(*) AS n, count(*) FILTER (WHERE running) AS live,
               mode() WITHIN GROUP (ORDER BY vertical) AS vertical
        FROM spy.creative_stats WHERE operator_id IS NOT NULL AND NOT is_junk
        GROUP BY 1
    )
    SELECT a.operator_id, a.total, COALESCE(a.s_today, 0), COALESCE(a.s_7d, 0), COALESCE(a.s_prev_7d, 0),
           COALESCE(a.s_30d, 0),
           LEAST(100, round(100.0 * COALESCE(a.s_today, 0) / network_today, 4)),
           LEAST(100, round(100.0 * COALESCE(a.s_7d, 0) / network_7d, 4)),
           LEAST(100, round(100.0 * COALESCE(a.s_30d, 0) / network_30d, 4)),
           COALESCE(c.n, 0), COALESCE(c.live, 0), p.ids,
           COALESCE(o.vertical, c.vertical), COALESCE(b.names, '{}'),
           a.first_seen, a.last_seen, p_now
    FROM agg a
    JOIN spy.operator o ON o.id = a.operator_id
    JOIN pubs p USING (operator_id)
    LEFT JOIN brands b USING (operator_id)
    LEFT JOIN creatives c USING (operator_id)
    ON CONFLICT (operator_id) DO UPDATE SET
        sightings_total = EXCLUDED.sightings_total,
        sightings_today = EXCLUDED.sightings_today,
        sightings_7d = EXCLUDED.sightings_7d,
        sightings_prev_7d = EXCLUDED.sightings_prev_7d,
        sightings_30d = EXCLUDED.sightings_30d,
        share_today_pct = EXCLUDED.share_today_pct,
        share_7d_pct = EXCLUDED.share_7d_pct,
        share_30d_pct = EXCLUDED.share_30d_pct,
        creatives_count = EXCLUDED.creatives_count,
        live_creatives_count = EXCLUDED.live_creatives_count,
        publisher_ids = EXCLUDED.publisher_ids,
        vertical = EXCLUDED.vertical,
        brands = EXCLUDED.brands,
        first_seen_at = EXCLUDED.first_seen_at,
        last_seen_at = EXCLUDED.last_seen_at,
        refreshed_at = EXCLUDED.refreshed_at;

    -- Publishers ---------------------------------------------------------------
    INSERT INTO spy.publisher_stats AS ps (
        publisher_id, sightings_total, sightings_today, sightings_7d, sightings_30d, share_30d_pct,
        scrapes_30d, phone_sightings_30d, creatives_30d, operators_30d, operator_hhi, even_operators,
        top_operators, top_creatives, verticals, first_seen_at, last_seen_at, refreshed_at
    )
    WITH hourly AS (
        SELECT publisher_id,
               sum(sightings) AS total,
               sum(sightings) FILTER (WHERE hour >= today::timestamp AT TIME ZONE 'UTC') AS s_today,
               sum(sightings) FILTER (WHERE hour >= (today - 6)::timestamp AT TIME ZONE 'UTC') AS s_7d,
               sum(sightings) FILTER (WHERE hour >= (today - 29)::timestamp AT TIME ZONE 'UTC') AS s_30d,
               sum(scrapes) FILTER (WHERE hour >= (today - 29)::timestamp AT TIME ZONE 'UTC') AS scrapes_30d,
               sum(sightings) FILTER (WHERE hour >= (today - 29)::timestamp AT TIME ZONE 'UTC'
                                        AND device_id = phone) AS phone_30d,
               min(hour) FILTER (WHERE sightings > 0) AS first_hour,
               max(hour) FILTER (WHERE sightings > 0) AS last_hour
        FROM tracks_api.scrape_coverage_v2 GROUP BY 1
    ),
    per_op AS (
        SELECT publisher_id, operator_id, sum(sightings) AS s
        FROM spy_op_day WHERE day > today - 30 GROUP BY 1, 2
    ),
    op_share AS (
        SELECT po.publisher_id, po.operator_id, po.s,
               100.0 * po.s / NULLIF(sum(po.s) OVER (PARTITION BY po.publisher_id), 0) AS share,
               row_number() OVER (PARTITION BY po.publisher_id ORDER BY po.s DESC) AS rn
        FROM per_op po
    ),
    ops AS (
        SELECT publisher_id, count(*) AS n, round(sum(share * share), 1) AS hhi,
               jsonb_agg(jsonb_build_object('operator_id', operator_id, 'sightings', s,
                                            'share_pct', round(share, 2)) ORDER BY s DESC)
                   FILTER (WHERE rn <= 10) AS top
        FROM op_share GROUP BY 1
    ),
    per_creative AS (
        SELECT publisher_id, creative_id, sum(sightings) AS s,
               row_number() OVER (PARTITION BY publisher_id ORDER BY sum(sightings) DESC) AS rn
        FROM tracks_api.ad_daily_v1 WHERE day > today - 30 GROUP BY 1, 2
    ),
    creatives AS (
        SELECT pc.publisher_id, count(*) AS n,
               jsonb_agg(jsonb_build_object('creative_id', pc.creative_id, 'sightings', pc.s) ORDER BY pc.s DESC)
                   FILTER (WHERE pc.rn <= 8 AND NOT COALESCE(cs.is_junk, FALSE)) AS top
        FROM per_creative pc LEFT JOIN spy.creative_stats cs USING (creative_id)
        GROUP BY 1
    ),
    verticals AS (
        SELECT publisher_id,
               jsonb_agg(jsonb_build_object('vertical', vertical, 'sightings', s,
                                            'share_pct', round(100.0 * s / NULLIF(t, 0), 1)) ORDER BY s DESC) AS list
        FROM (SELECT pc.publisher_id, cs.vertical, sum(pc.s) AS s,
                     sum(sum(pc.s)) OVER (PARTITION BY pc.publisher_id) AS t
              FROM per_creative pc JOIN spy.creative_stats cs USING (creative_id)
              WHERE cs.vertical IS NOT NULL
              GROUP BY 1, 2) x
        GROUP BY 1
    )
    SELECT h.publisher_id, h.total, COALESCE(h.s_today, 0), COALESCE(h.s_7d, 0), COALESCE(h.s_30d, 0),
           LEAST(100, round(100.0 * COALESCE(h.s_30d, 0) / network_30d, 4)),
           COALESCE(h.scrapes_30d, 0), COALESCE(h.phone_30d, 0),
           COALESCE(c.n, 0), COALESCE(o.n, 0), COALESCE(o.hhi, 0), round(10000 / NULLIF(o.hhi, 0), 1),
           COALESCE(o.top, '[]'), COALESCE(c.top, '[]'), COALESCE(v.list, '[]'),
           h.first_hour, h.last_hour + interval '1 hour', p_now
    FROM hourly h
    LEFT JOIN ops o USING (publisher_id)
    LEFT JOIN creatives c USING (publisher_id)
    LEFT JOIN verticals v USING (publisher_id)
    ON CONFLICT (publisher_id) DO UPDATE SET
        sightings_total = EXCLUDED.sightings_total,
        sightings_today = EXCLUDED.sightings_today,
        sightings_7d = EXCLUDED.sightings_7d,
        sightings_30d = EXCLUDED.sightings_30d,
        share_30d_pct = EXCLUDED.share_30d_pct,
        scrapes_30d = EXCLUDED.scrapes_30d,
        phone_sightings_30d = EXCLUDED.phone_sightings_30d,
        creatives_30d = EXCLUDED.creatives_30d,
        operators_30d = EXCLUDED.operators_30d,
        operator_hhi = EXCLUDED.operator_hhi,
        even_operators = EXCLUDED.even_operators,
        top_operators = EXCLUDED.top_operators,
        top_creatives = EXCLUDED.top_creatives,
        verticals = EXCLUDED.verticals,
        first_seen_at = EXCLUDED.first_seen_at,
        last_seen_at = EXCLUDED.last_seen_at,
        refreshed_at = EXCLUDED.refreshed_at;

    -- Each creative's days, for lifespan. The last 3 days every time, older
    -- days when an hour in them closed again, everything the first time.
    -- The days are picked first: a filter on creative_day inside the insert
    -- would read the rows the insert itself is writing.
    SELECT done_at INTO last_days FROM spy.direction_mark WHERE job = 'creative_day';
    IF last_days IS NULL THEN
        DELETE FROM spy.creative_day;
        INSERT INTO spy.creative_day (creative_id, day, sightings, first_seen_at, last_seen_at)
        SELECT d.creative_id, d.day, sum(d.sightings), min(d.first_seen_at), max(d.last_seen_at)
        FROM tracks_api.ad_daily_v1 d
        GROUP BY 1, 2;
        ANALYZE spy.creative_day;
    ELSE
        redo := ARRAY(
            SELECT today - g FROM generate_series(0, 2) g
            UNION
            SELECT DISTINCT (c.hour AT TIME ZONE 'UTC')::date FROM tracks_api.closed_hour_v1 c WHERE c.closed_at > last_days);
        DELETE FROM spy.creative_day WHERE day = ANY (redo);
        INSERT INTO spy.creative_day (creative_id, day, sightings, first_seen_at, last_seen_at)
        SELECT d.creative_id, d.day, sum(d.sightings), min(d.first_seen_at), max(d.last_seen_at)
        FROM tracks_api.ad_daily_v1 d
        WHERE d.day = ANY (redo)
        GROUP BY 1, 2;
    END IF;
    INSERT INTO spy.direction_mark (job, done_at) VALUES ('creative_day', clock_timestamp())
    ON CONFLICT (job) DO UPDATE SET done_at = EXCLUDED.done_at;

    RETURN n;
END;
$$;
