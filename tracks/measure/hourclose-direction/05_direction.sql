-- Direction and Size, copied as they are from adhunters-collector e20148c so
-- the timing measures today's code on the new layout. Nothing here is edited
-- except what is listed below. This file only ever runs on a throwaway
-- measuring box; Direction's real home will be spy/, which reads tracks_api.
--
--   migrations/025_spy_size_direction.sql  sections 1 to 8, without its
--                                           refresh_direction (027 replaced it)
--   migrations/026_spy_vertical_classifier.sql  spy.direction_ad_vertical
--   migrations/027_spy_search_universal_sao_paulo.sql  B1 and B2 (time zone
--                                           setting, refresh_direction)
--
-- Left out: GRANTs and adhunters_refresh_grants(), which need prodbox's roles.

-- ------------------------------------------------------------------------------
-- 1. Settings
-- ------------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS spy.direction_setting (
    name TEXT PRIMARY KEY,
    value NUMERIC NOT NULL,
    note TEXT NOT NULL
);

-- A re-run keeps values someone already tuned. Only the notes are refreshed.
INSERT INTO spy.direction_setting (name, value, note) VALUES
    ('window_hours', 2, 'Hours judged: the current hour so far plus the hours before it.'),
    ('usual_weeks', 3, 'Weeks back for the usual value (same UTC hour and weekday).'),
    ('usual_publisher_days', 21, 'Days back that decide where an ad usually runs.'),
    ('member_days', 28, 'Ads seen in these days are followed.'),
    ('min_expected', 5, 'Direction is judged only when the usual value expects this many sightings in the window, or this many were seen.'),
    ('min_z', 2, 'Rising and fading need the gap to the usual value to be this many square roots of the expected count.'),
    ('rise_enter', 1.5, 'Per-scrape ratio to usual that makes rising.'),
    ('rise_exit', 1.25, 'Rising stays rising until the ratio falls below this.'),
    ('fade_enter', 0.6, 'Per-scrape ratio to usual that makes fading.'),
    ('fade_exit', 0.75, 'Fading stays fading until the ratio climbs above this.'),
    ('flat_low', 0.8, 'Ratios between flat_low and flat_high count as flat.'),
    ('flat_high', 1.25, 'Ratios between flat_low and flat_high count as flat.'),
    ('free_rise_rest_max', 0.8, 'Free rise: the rest of the vertical per scrape is at or below this ratio to usual.'),
    ('stopped_hours', 6, 'Not seen for this many hours can mean stopped.'),
    ('stopped_min_expected', 3, 'Stopped only when the usual value expected at least this many sightings in those hours.'),
    ('scaled_cum_share_pct', 50, 'Scaled: among the biggest that together make this share of the vertical (7 days).'),
    ('scaled_max_rank', 20, 'Scaled: rank in the vertical (7 days) at most this.'),
    ('scaled_min_sightings_7d', 100, 'Scaled: at least this many sightings in 7 days.'),
    ('push_hours', 24, 'Push signs: new headlines and creatives in the same campaign within these hours.'),
    ('bid_rise', 1.2, 'Push sign: NewsBreak bid in the window at least this ratio to the 24 hours before.')
ON CONFLICT (name) DO UPDATE SET note = EXCLUDED.note;

CREATE OR REPLACE FUNCTION spy.direction_cfg()
RETURNS JSONB
LANGUAGE sql STABLE
AS $$ SELECT COALESCE(jsonb_object_agg(name, value), '{}'::jsonb) FROM spy.direction_setting $$;

-- ------------------------------------------------------------------------------
-- 2. Vertical source. Swap this body for another classification; nothing else
-- needs to change. One row per ad with a known vertical.
-- ------------------------------------------------------------------------------

CREATE OR REPLACE VIEW spy.direction_ad_vertical AS
SELECT a.id AS ad_id, cs.vertical
FROM spy.ad a
JOIN spy.creative_stats cs ON cs.creative_id = a.creative_id
WHERE cs.vertical IS NOT NULL;

-- ------------------------------------------------------------------------------
-- 3. Tables
-- ------------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS spy.direction_member (
    ad_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    key TEXT NOT NULL,
    PRIMARY KEY (ad_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_spy_dmember_subject ON spy.direction_member(kind, key);

CREATE TABLE IF NOT EXISTS spy.direction_subject (
    kind TEXT NOT NULL CHECK (kind IN ('ad', 'creative', 'operator', 'vertical', 'network')),
    key TEXT NOT NULL,
    market_kind TEXT,           -- 'vertical', or 'network' for verticals and ads without one
    market_key TEXT,
    first_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (kind, key)
);

CREATE TABLE IF NOT EXISTS spy.direction_usual_publisher (
    kind TEXT NOT NULL,
    key TEXT NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    PRIMARY KEY (kind, key, publisher_id, device_id)
);

-- One row per one-hour slot. Sums over the past weeks that count (the subject
-- existed and its publishers were scraped). No row: no sightings in any of
-- those weeks.
CREATE TABLE IF NOT EXISTS spy.direction_usual (
    hour TIMESTAMPTZ NOT NULL,      -- the current-week hour this usual value is for
    kind TEXT NOT NULL,
    key TEXT NOT NULL,
    weeks SMALLINT NOT NULL,        -- past weeks summed
    sightings INTEGER NOT NULL,     -- on its usual publishers
    scrapes INTEGER NOT NULL,       -- of its usual publishers
    sightings_all INTEGER NOT NULL, -- on every publisher, for share of voice
    PRIMARY KEY (hour, kind, key)
);

-- Which slots are filled, and when the daily and hourly parts last ran.
CREATE TABLE IF NOT EXISTS spy.direction_mark (
    job TEXT PRIMARY KEY,
    done_at TIMESTAMPTZ NOT NULL
);

-- Which hours are done, per job: 'usual' (direction_usual filled for that
-- slot) and 'seen' (direction_seen filled for that complete hour).
CREATE TABLE IF NOT EXISTS spy.direction_hour_mark (
    job TEXT NOT NULL,
    hour TIMESTAMPTZ NOT NULL,
    done_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (job, hour)
);

-- Who was seen in each complete hour of the stopped hours before the window.
-- A complete hour never changes, so it is counted once, not every 5 minutes.
CREATE TABLE IF NOT EXISTS spy.direction_seen (
    hour TIMESTAMPTZ NOT NULL,
    kind TEXT NOT NULL,
    key TEXT NOT NULL,
    sightings INTEGER NOT NULL,
    PRIMARY KEY (hour, kind, key)
);

CREATE TABLE IF NOT EXISTS spy.size_stats (
    kind TEXT NOT NULL,
    key TEXT NOT NULL,
    subject_id INTEGER GENERATED ALWAYS AS (
        CASE WHEN kind IN ('ad', 'creative', 'operator') THEN key::integer END) STORED,
    market_kind TEXT NOT NULL,
    market_key TEXT NOT NULL,
    sightings_24h INTEGER NOT NULL,
    share_24h_pct NUMERIC(7, 4) NOT NULL,
    rank_24h INTEGER,
    sightings_7d INTEGER NOT NULL,
    share_7d_pct NUMERIC(7, 4) NOT NULL,
    rank_7d INTEGER,
    share_7d_before_pct NUMERIC(7, 4) NOT NULL,  -- share of everyone ranked above it
    scaled BOOLEAN NOT NULL,
    refreshed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (kind, key)
);
CREATE INDEX IF NOT EXISTS idx_spy_size_subject ON spy.size_stats(kind, subject_id);
CREATE INDEX IF NOT EXISTS idx_spy_size_market ON spy.size_stats(kind, market_key, rank_7d);

CREATE TABLE IF NOT EXISTS spy.direction_stats (
    kind TEXT NOT NULL,
    key TEXT NOT NULL,
    subject_id INTEGER GENERATED ALWAYS AS (
        CASE WHEN kind IN ('ad', 'creative', 'operator') THEN key::integer END) STORED,
    -- rising, steady, fading, stopped. Proposals, not in the glossary yet:
    -- free_rise (share up only because others dropped), unclear (can't tell).
    direction TEXT NOT NULL,
    direction_since TIMESTAMPTZ NOT NULL,
    judged BOOLEAN NOT NULL,        -- false: too little data now, direction kept from before
    window_from TIMESTAMPTZ NOT NULL,
    now_sightings INTEGER NOT NULL,     -- window, usual publishers
    now_scrapes INTEGER NOT NULL,       -- window, usual publishers
    usual_per_scrape NUMERIC,
    now_per_scrape NUMERIC,
    ratio NUMERIC,                      -- now / usual, per scrape. NULL when usual is 0
    expected NUMERIC,                   -- usual per scrape x scrapes done
    z NUMERIC,
    share_now_pct NUMERIC,
    share_usual_pct NUMERIC,
    share_ratio NUMERIC,
    market_ratio NUMERIC,               -- the whole vertical, per scrape, now / usual
    rest_ratio NUMERIC,                 -- the vertical without this one
    last_seen_hour TIMESTAMPTZ,
    stop_expected NUMERIC,
    stop_scrapes INTEGER,
    reason JSONB NOT NULL,
    reason_text TEXT NOT NULL,
    refreshed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (kind, key)
);
CREATE INDEX IF NOT EXISTS idx_spy_dstats_subject ON spy.direction_stats(kind, subject_id);
CREATE INDEX IF NOT EXISTS idx_spy_dstats_direction ON spy.direction_stats(kind, direction);

CREATE TABLE IF NOT EXISTS spy.direction_event (
    id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    at TIMESTAMPTZ NOT NULL,
    kind TEXT NOT NULL,
    key TEXT NOT NULL,
    subject_id INTEGER GENERATED ALWAYS AS (
        CASE WHEN kind IN ('ad', 'creative', 'operator') THEN key::integer END) STORED,
    from_direction TEXT NOT NULL,
    to_direction TEXT NOT NULL,
    reason JSONB NOT NULL,
    reason_text TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_spy_devent_at ON spy.direction_event(at);
CREATE INDEX IF NOT EXISTS idx_spy_devent_subject ON spy.direction_event(kind, subject_id, at);

-- ------------------------------------------------------------------------------
-- 4. Members and subjects
-- ------------------------------------------------------------------------------

-- Adds ads seen since p_since that are not members yet (junk ads never).
-- A member keeps its vertical until the next daily rebuild.
CREATE OR REPLACE FUNCTION spy.direction_add_members(p_since TIMESTAMPTZ)
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    n INTEGER;
BEGIN
    CREATE TEMP TABLE IF NOT EXISTS spy_dir_new_member (ad_id INTEGER, kind TEXT, key TEXT) ON COMMIT DROP;
    TRUNCATE spy_dir_new_member;

    WITH ads AS (
        SELECT a.id, a.creative_id, acc.operator_id, v.vertical
        FROM spy.ad a
        LEFT JOIN spy.account acc ON acc.id = a.account_id
        LEFT JOIN spy.direction_ad_vertical v ON v.ad_id = a.id
        WHERE a.last_seen_at >= p_since
          AND NOT EXISTS (SELECT 1 FROM spy.direction_member m WHERE m.ad_id = a.id AND m.kind = 'ad')
          AND NOT EXISTS (SELECT 1 FROM spy.creative_stats cs WHERE cs.creative_id = a.creative_id AND cs.is_junk)
    ),
    ins AS (
        INSERT INTO spy.direction_member (ad_id, kind, key)
        SELECT id, 'ad', id::text FROM ads
        UNION ALL SELECT id, 'creative', creative_id::text FROM ads
        UNION ALL SELECT id, 'operator', operator_id::text FROM ads WHERE operator_id IS NOT NULL
        UNION ALL SELECT id, 'vertical', vertical FROM ads WHERE vertical IS NOT NULL
        UNION ALL SELECT id, 'network', '*' FROM ads
        ON CONFLICT (ad_id, kind) DO NOTHING
        RETURNING ad_id, kind, key
    )
    INSERT INTO spy_dir_new_member SELECT * FROM ins;
    GET DIAGNOSTICS n = ROW_COUNT;

    -- New subjects only. Existing ones keep their vertical until the rebuild.
    INSERT INTO spy.direction_subject (kind, key, market_kind, market_key, first_seen_at)
    SELECT s.kind, s.key,
           CASE WHEN s.kind = 'network' THEN NULL WHEN s.kind = 'vertical' OR s.vertical IS NULL THEN 'network' ELSE 'vertical' END,
           CASE WHEN s.kind = 'network' THEN NULL WHEN s.kind = 'vertical' OR s.vertical IS NULL THEN '*' ELSE s.vertical END,
           s.first_seen
    FROM (
        SELECT m.kind, m.key, min(a.first_seen_at) AS first_seen,
               mode() WITHIN GROUP (ORDER BY mv.key) AS vertical
        FROM spy_dir_new_member m
        JOIN spy.ad a ON a.id = m.ad_id
        LEFT JOIN spy_dir_new_member mv ON mv.ad_id = m.ad_id AND mv.kind = 'vertical'
        GROUP BY 1, 2
    ) s
    ON CONFLICT (kind, key) DO NOTHING;

    RETURN n;
END;
$$;

-- Daily: members, subjects and usual publishers from scratch. The usual values
-- are dropped so they are filled again with the new sets.
CREATE OR REPLACE FUNCTION spy.direction_rebuild()
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    cfg JSONB := spy.direction_cfg();
    today DATE := (NOW() AT TIME ZONE 'UTC')::date;
    n INTEGER;
BEGIN
    TRUNCATE spy.direction_member;
    n := spy.direction_add_members(NOW() - make_interval(days => (cfg->>'member_days')::int));

    -- Subjects again, now from every member: vertical is the most common one of
    -- its ads, first sighting is the earliest of its ads.
    CREATE TEMP TABLE IF NOT EXISTS spy_dir_subject (LIKE spy.direction_subject) ON COMMIT DROP;
    TRUNCATE spy_dir_subject;
    INSERT INTO spy_dir_subject
    SELECT s.kind, s.key,
           CASE WHEN s.kind = 'network' THEN NULL WHEN s.kind = 'vertical' OR s.vertical IS NULL THEN 'network' ELSE 'vertical' END,
           CASE WHEN s.kind = 'network' THEN NULL WHEN s.kind = 'vertical' OR s.vertical IS NULL THEN '*' ELSE s.vertical END,
           s.first_seen
    FROM (
        SELECT m.kind, m.key, min(a.first_seen_at) AS first_seen,
               mode() WITHIN GROUP (ORDER BY mv.key) AS vertical
        FROM spy.direction_member m
        JOIN spy.ad a ON a.id = m.ad_id
        LEFT JOIN spy.direction_member mv ON mv.ad_id = m.ad_id AND mv.kind = 'vertical'
        GROUP BY 1, 2
    ) s;

    DELETE FROM spy.direction_subject d
    WHERE NOT EXISTS (SELECT 1 FROM spy_dir_subject s WHERE s.kind = d.kind AND s.key = d.key);
    INSERT INTO spy.direction_subject AS d SELECT * FROM spy_dir_subject
    ON CONFLICT (kind, key) DO UPDATE SET
        market_kind = EXCLUDED.market_kind,
        market_key = EXCLUDED.market_key,
        first_seen_at = EXCLUDED.first_seen_at;

    DELETE FROM spy.direction_stats d
    WHERE NOT EXISTS (SELECT 1 FROM spy.direction_subject s WHERE s.kind = d.kind AND s.key = d.key);
    DELETE FROM spy.size_stats d
    WHERE NOT EXISTS (SELECT 1 FROM spy.direction_subject s WHERE s.kind = d.kind AND s.key = d.key);

    TRUNCATE spy.direction_usual_publisher;
    INSERT INTO spy.direction_usual_publisher (kind, key, publisher_id, device_id)
    SELECT DISTINCT m.kind, m.key, d.publisher_id, d.device_id
    FROM (SELECT DISTINCT ad_id, publisher_id, device_id FROM spy.ad_daily
          WHERE day >= today - (cfg->>'usual_publisher_days')::int AND day < today) d
    JOIN spy.direction_member m ON m.ad_id = d.ad_id;

    TRUNCATE spy.direction_usual, spy.direction_seen, spy.direction_hour_mark;

    INSERT INTO spy.direction_mark (job, done_at) VALUES ('rebuild', NOW())
    ON CONFLICT (job) DO UPDATE SET done_at = EXCLUDED.done_at;
    RETURN n;
END;
$$;

-- ------------------------------------------------------------------------------
-- 5. Usual value for one slot hour: the same hour 1, 2 and 3 weeks back.
-- ------------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION spy.direction_fill_usual(p_hour TIMESTAMPTZ)
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    cfg JSONB := spy.direction_cfg();
    hours TIMESTAMPTZ[];
    n INTEGER;
BEGIN
    DELETE FROM spy.direction_usual WHERE hour = p_hour;
    -- The same hour 1, 2 and 3 weeks back. An array, so the reads use the index.
    hours := ARRAY(SELECT p_hour - make_interval(days => 7 * k)
                   FROM generate_series(1, (cfg->>'usual_weeks')::int) k);

    INSERT INTO spy.direction_usual (hour, kind, key, weeks, sightings, scrapes, sightings_all)
    WITH past AS (
        SELECT unnest(hours) AS h
    ),
    ah AS (
        SELECT x.hour AS h, x.ad_id, x.publisher_id, x.device_id, x.sightings
        FROM spy.ad_hourly x WHERE x.hour = ANY (hours)
    ),
    per AS (
        SELECT m.kind, m.key, ah.h, ah.publisher_id, ah.device_id, sum(ah.sightings) AS s
        FROM ah JOIN spy.direction_member m ON m.ad_id = ah.ad_id
        GROUP BY 1, 2, 3, 4, 5
    ),
    counts AS (
        SELECT p.kind, p.key, p.h,
               sum(p.s) AS s_all,
               COALESCE(sum(p.s) FILTER (WHERE up.kind IS NOT NULL), 0) AS s_usual
        FROM per p
        LEFT JOIN spy.direction_usual_publisher up
               ON up.kind = p.kind AND up.key = p.key
              AND up.publisher_id = p.publisher_id AND up.device_id = p.device_id
        GROUP BY 1, 2, 3
    ),
    ph AS (
        SELECT hour AS h, publisher_id, device_id, scrapes
        FROM spy.publisher_hourly WHERE hour = ANY (hours)
    ),
    scr AS (
        SELECT up.kind, up.key, ph.h, sum(ph.scrapes) AS sc
        FROM spy.direction_usual_publisher up
        JOIN ph ON ph.publisher_id = up.publisher_id AND ph.device_id = up.device_id
        WHERE (up.kind, up.key) IN (SELECT kind, key FROM counts)
        GROUP BY 1, 2, 3
    ),
    weekly AS (
        -- Every past week, including weeks with no sightings. A week counts when
        -- the subject existed and its usual publishers were scraped.
        SELECT sb.kind, sb.key, p.h,
               COALESCE(c.s_usual, 0) AS s_usual, COALESCE(c.s_all, 0) AS s_all, sc.sc
        FROM (SELECT DISTINCT kind, key FROM counts) sb
        CROSS JOIN past p
        JOIN spy.direction_subject s ON s.kind = sb.kind AND s.key = sb.key
        JOIN scr sc ON sc.kind = sb.kind AND sc.key = sb.key AND sc.h = p.h
        LEFT JOIN counts c ON c.kind = sb.kind AND c.key = sb.key AND c.h = p.h
        WHERE s.first_seen_at < p.h + interval '1 hour' AND sc.sc > 0
    )
    SELECT p_hour, kind, key, count(*), sum(s_usual), sum(sc), sum(s_all)
    FROM weekly
    GROUP BY kind, key;
    GET DIAGNOSTICS n = ROW_COUNT;

    INSERT INTO spy.direction_hour_mark (job, hour, done_at) VALUES ('usual', p_hour, NOW())
    ON CONFLICT (job, hour) DO UPDATE SET done_at = EXCLUDED.done_at;
    RETURN n;
END;
$$;

-- ------------------------------------------------------------------------------
-- 6. Size: share and rank in the vertical. 7 days = today and the 6 days
-- before (as creative_stats). 24 hours = the current hour and the 23 before.
-- ------------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION spy.refresh_size()
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    cfg JSONB := spy.direction_cfg();
    today DATE := (NOW() AT TIME ZONE 'UTC')::date;
    cur_hour TIMESTAMPTZ := date_trunc('hour', NOW(), 'UTC');
    n INTEGER;
BEGIN
    CREATE TEMP TABLE IF NOT EXISTS spy_size_new (LIKE spy.size_stats) ON COMMIT DROP;
    TRUNCATE spy_size_new;

    INSERT INTO spy_size_new (kind, key, market_kind, market_key, sightings_24h, share_24h_pct, rank_24h,
                              sightings_7d, share_7d_pct, rank_7d, share_7d_before_pct, scaled, refreshed_at)
    WITH d7 AS (
        SELECT ad_id, sum(sightings) AS s FROM spy.ad_daily WHERE day > today - 7 GROUP BY 1
    ),
    h24 AS (
        SELECT ad_id, sum(sightings) AS s FROM spy.ad_hourly WHERE hour > cur_hour - interval '24 hours' GROUP BY 1
    ),
    per_ad AS (
        SELECT COALESCE(d7.ad_id, h24.ad_id) AS ad_id, COALESCE(d7.s, 0) AS s7, COALESCE(h24.s, 0) AS s24
        FROM d7 FULL JOIN h24 ON h24.ad_id = d7.ad_id
    ),
    per AS (
        SELECT m.kind, m.key, sum(p.s7) AS s7, sum(p.s24) AS s24
        FROM per_ad p JOIN spy.direction_member m ON m.ad_id = p.ad_id
        GROUP BY 1, 2
    ),
    withm AS (
        SELECT p.kind, p.key, s.market_kind, s.market_key, p.s7, p.s24,
               GREATEST(mk.s7, 1) AS m7, GREATEST(mk.s24, 1) AS m24
        FROM per p
        JOIN spy.direction_subject s ON s.kind = p.kind AND s.key = p.key
        JOIN per mk ON mk.kind = s.market_kind AND mk.key = s.market_key
        WHERE p.kind <> 'network'
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
           NOW()
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

    INSERT INTO spy.direction_mark (job, done_at) VALUES ('size', NOW())
    ON CONFLICT (job) DO UPDATE SET done_at = EXCLUDED.done_at;
    RETURN n;
END;
$$;

-- ------------------------------------------------------------------------------
-- 7. The sentence the UI shows. Built from the reason numbers only.
-- ------------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION spy.direction_ratio_word(p_ratio NUMERIC, p_low NUMERIC, p_high NUMERIC)
RETURNS TEXT
LANGUAGE plpgsql IMMUTABLE
AS $$
BEGIN
    IF p_ratio IS NULL THEN RETURN 'unknown'; END IF;
    IF p_ratio < p_low THEN RETURN 'down ' || round(p_ratio, 1) || '×'; END IF;
    IF p_ratio > p_high THEN RETURN 'up ' || round(p_ratio, 1) || '×'; END IF;
    RETURN 'flat (' || round(p_ratio, 1) || '×)';
END;
$$;

-- plpgsql, not sql: it runs once per changed row and the branches exit early.
CREATE OR REPLACE FUNCTION spy.direction_reason_text(p_direction TEXT, r JSONB)
RETURNS TEXT
LANGUAGE plpgsql IMMUTABLE
AS $$
DECLARE
    main TEXT;
    low NUMERIC := (r->>'flat_low')::numeric;
    high NUMERIC := (r->>'flat_high')::numeric;
    word TEXT := r->>'market_word';
BEGIN
    IF p_direction = 'stopped' THEN
        main := format('Not seen for %sh. Its publishers were scraped %s times, about %s sightings were expected.',
                       r->>'unseen_hours', r->>'stop_scrapes', round((r->>'stop_expected')::numeric, 1));
    ELSIF p_direction = 'unclear' AND (r->>'stop_scrapes')::numeric = 0 THEN
        main := format('Not seen for %sh, but its publishers were not scraped. Can''t tell.', r->>'unseen_hours');
    ELSIF p_direction = 'unclear' AND r ? 'unseen_hours' THEN
        main := format('Not seen for %sh, but only %s sightings were expected. Too rare to tell.',
                       r->>'unseen_hours', round(COALESCE((r->>'stop_expected')::numeric, 0), 1));
    ELSIF (r->>'weeks')::int = 0 THEN
        main := format('No usual value for %s yet. Seen for less than a week, or its publishers were not scraped then.', r->>'slot');
    ELSIF NOT (r->>'judged')::boolean THEN
        main := format('Too little data for %s (%s expected, %s seen). Direction kept.',
                       r->>'slot', round(COALESCE((r->>'expected')::numeric, 0), 1), r->>'now_sightings');
    ELSIF p_direction = 'free_rise' THEN
        main := format('Share %s× usual for %s, but per scrape %s and the rest of the %s %s. A rise because others dropped.',
                       round((r->>'share_ratio')::numeric, 1), r->>'slot',
                       spy.direction_ratio_word((r->>'ratio')::numeric, low, high), word,
                       spy.direction_ratio_word((r->>'rest_ratio')::numeric, low, high));
    ELSIF NOT r ? 'ratio' THEN
        main := format('Not usually seen at %s. Now %s sightings in %s scrapes.', r->>'slot', r->>'now_sightings', r->>'now_scrapes');
    ELSE
        main := format('%s× usual for %s (%s vs %s per scrape). %s %s%s.',
                       round((r->>'ratio')::numeric, 1), r->>'slot',
                       round((r->>'now_per_scrape')::numeric, 3), round((r->>'usual_per_scrape')::numeric, 3),
                       upper(left(word, 1)) || substr(word, 2),
                       spy.direction_ratio_word((r->>'market_ratio')::numeric, low, high),
                       CASE WHEN p_direction = 'rising' THEN ', so a real push' ELSE '' END);
    END IF;
    IF NOT (r ? 'new_publishers' OR r ? 'both_devices' OR r ? 'new_ads_in_campaign'
            OR r ? 'new_creatives_in_campaign' OR r ? 'bid_up') THEN
        RETURN main;
    END IF;
    RETURN concat_ws(' ', main,
        CASE WHEN (r->>'new_publishers')::int > 0 THEN format('New publishers: %s.', r->>'new_publishers') END,
        CASE WHEN (r->>'both_devices')::boolean THEN 'Now on both devices.' END,
        CASE WHEN (r->>'new_ads_in_campaign')::int > 0 THEN format('New headlines in its campaigns: %s.', r->>'new_ads_in_campaign') END,
        CASE WHEN (r->>'new_creatives_in_campaign')::int > 0 THEN format('New creatives in its campaigns: %s.', r->>'new_creatives_in_campaign') END,
        CASE WHEN (r->>'bid_up')::boolean THEN format('NewsBreak bid up from %s to %s.', r->>'bid_before', r->>'bid_now') END);
END;
$$;

-- ------------------------------------------------------------------------------
-- 8. Refresh. Every 5 minutes, from the collector.
-- ------------------------------------------------------------------------------

-- Fills direction_seen for one complete hour.
CREATE OR REPLACE FUNCTION spy.direction_fill_seen(p_hour TIMESTAMPTZ)
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    n INTEGER;
BEGIN
    DELETE FROM spy.direction_seen WHERE hour = p_hour;
    INSERT INTO spy.direction_seen (hour, kind, key, sightings)
    SELECT p_hour, m.kind, m.key, sum(x.s)
    FROM (SELECT ad_id, sum(sightings) AS s FROM spy.ad_hourly WHERE hour = p_hour GROUP BY 1) x
    JOIN spy.direction_member m ON m.ad_id = x.ad_id
    GROUP BY 1, 2, 3;
    GET DIAGNOSTICS n = ROW_COUNT;
    INSERT INTO spy.direction_hour_mark (job, hour, done_at) VALUES ('seen', p_hour, NOW())
    ON CONFLICT (job, hour) DO UPDATE SET done_at = EXCLUDED.done_at;
    RETURN n;
END;
$$;


-- From 026: Direction reads the vertical classification.
CREATE OR REPLACE VIEW spy.direction_ad_vertical AS
SELECT a.id AS ad_id, cv.vertical
FROM spy.ad a
JOIN spy.creative_vertical cv ON cv.creative_id = a.creative_id
WHERE cv.vertical IS NOT NULL;

-- ------------------------------------------------------------------------------
-- B1. The time zone setting
-- ------------------------------------------------------------------------------

ALTER TABLE spy.direction_setting ADD COLUMN IF NOT EXISTS text_value TEXT;
ALTER TABLE spy.direction_setting ALTER COLUMN value DROP NOT NULL;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname = 'direction_setting_has_value'
                     AND conrelid = 'spy.direction_setting'::regclass) THEN
        ALTER TABLE spy.direction_setting
            ADD CONSTRAINT direction_setting_has_value CHECK (value IS NOT NULL OR text_value IS NOT NULL);
    END IF;
END
$$;

-- A re-run keeps a time zone someone already set. Only the notes are refreshed.
INSERT INTO spy.direction_setting (name, value, text_value, note) VALUES
    ('time_zone', NULL, 'America/Sao_Paulo',
     'Time zone of the hours and weekdays in the sentences people read, like "Thu 14h". The client is in São Paulo. A name from the IANA list.'),
    ('usual_weeks', 3, NULL, 'Weeks back for the usual value (the same hour and weekday).')
ON CONFLICT (name) DO UPDATE SET note = EXCLUDED.note;

CREATE OR REPLACE FUNCTION spy.direction_cfg()
RETURNS JSONB
LANGUAGE sql STABLE
AS $$
    SELECT COALESCE(jsonb_object_agg(name, COALESCE(to_jsonb(value), to_jsonb(text_value))), '{}'::jsonb)
    FROM spy.direction_setting
$$;

-- The time zone setting, or 'UTC' when it is empty or not a known name.
CREATE OR REPLACE FUNCTION spy.direction_time_zone()
RETURNS TEXT
LANGUAGE plpgsql STABLE
AS $$
DECLARE
    tz TEXT;
BEGIN
    SELECT NULLIF(btrim(text_value), '') INTO tz FROM spy.direction_setting WHERE name = 'time_zone';
    IF tz IS NULL THEN
        RETURN 'UTC';
    END IF;
    PERFORM NOW() AT TIME ZONE tz;
    RETURN tz;
EXCEPTION WHEN invalid_parameter_value THEN
    RETURN 'UTC';
END;
$$;

-- The hours judged, as people read them: "Thu 14h" for one hour, "Thu 13–14h"
-- for more. In the time zone setting. "UTC" is added only when that is UTC.
CREATE OR REPLACE FUNCTION spy.direction_slot(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ)
RETURNS TEXT
LANGUAGE plpgsql STABLE
AS $$
DECLARE
    tz TEXT := spy.direction_time_zone();
BEGIN
    RETURN CASE WHEN p_from = p_to
                THEN to_char(p_to AT TIME ZONE tz, 'Dy HH24"h"')
                ELSE to_char(p_from AT TIME ZONE tz, 'Dy HH24') || '–' || to_char(p_to AT TIME ZONE tz, 'HH24"h"')
           END
           || CASE WHEN tz = 'UTC' THEN ' UTC' ELSE '' END;
END;
$$;

-- ------------------------------------------------------------------------------
-- B2. spy.refresh_direction(): 025 with the slot in the time zone setting.
-- ------------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION spy.refresh_direction(p_rebuild BOOLEAN DEFAULT FALSE)
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    cfg JSONB := spy.direction_cfg();
    now_ts TIMESTAMPTZ := NOW();
    -- Settings, read once.
    s_bid_rise NUMERIC := (cfg->>'bid_rise')::numeric;
    s_fade_enter NUMERIC := (cfg->>'fade_enter')::numeric;
    s_fade_exit NUMERIC := (cfg->>'fade_exit')::numeric;
    s_flat_high NUMERIC := (cfg->>'flat_high')::numeric;
    s_flat_low NUMERIC := (cfg->>'flat_low')::numeric;
    s_free_rise_rest_max NUMERIC := (cfg->>'free_rise_rest_max')::numeric;
    s_min_expected NUMERIC := (cfg->>'min_expected')::numeric;
    s_min_z NUMERIC := (cfg->>'min_z')::numeric;
    s_push_hours NUMERIC := (cfg->>'push_hours')::numeric;
    s_rise_enter NUMERIC := (cfg->>'rise_enter')::numeric;
    s_rise_exit NUMERIC := (cfg->>'rise_exit')::numeric;
    s_stopped_hours NUMERIC := (cfg->>'stopped_hours')::numeric;
    s_stopped_min_expected NUMERIC := (cfg->>'stopped_min_expected')::numeric;
    s_usual_weeks NUMERIC := (cfg->>'usual_weeks')::numeric;
    s_window_hours NUMERIC := (cfg->>'window_hours')::numeric;
    cur_hour TIMESTAMPTZ := date_trunc('hour', NOW(), 'UTC');
    win_from TIMESTAMPTZ;
    stop_from TIMESTAMPTZ;
    slot TEXT;
    h TIMESTAMPTZ;
    last_rebuild TIMESTAMPTZ;
    last_size TIMESTAMPTZ;
    n INTEGER;
BEGIN
    -- One run at a time. A run that finds another one busy does nothing.
    IF NOT pg_try_advisory_xact_lock(hashtext('spy.refresh_direction')) THEN
        RETURN 0;
    END IF;

    win_from := cur_hour - make_interval(hours => s_window_hours::int - 1);
    stop_from := LEAST(win_from, cur_hour - make_interval(hours => s_stopped_hours::int));
    -- 027: the hours people read, in the time zone setting (São Paulo).
    slot := spy.direction_slot(win_from, cur_hour);

    -- Daily part.
    SELECT done_at INTO last_rebuild FROM spy.direction_mark WHERE job = 'rebuild';
    IF p_rebuild OR last_rebuild IS NULL OR (last_rebuild AT TIME ZONE 'UTC')::date < (now_ts AT TIME ZONE 'UTC')::date THEN
        PERFORM spy.direction_rebuild();
    END IF;
    PERFORM spy.direction_add_members(stop_from);

    -- Hourly part: usual values for the slots in use, who was seen in the
    -- complete hours before the window, then Size.
    DELETE FROM spy.direction_usual WHERE hour < stop_from;
    DELETE FROM spy.direction_seen WHERE hour < stop_from;
    DELETE FROM spy.direction_hour_mark WHERE hour < stop_from;
    FOR h IN SELECT generate_series(stop_from, cur_hour, interval '1 hour') LOOP
        IF NOT EXISTS (SELECT 1 FROM spy.direction_hour_mark WHERE job = 'usual' AND hour = h) THEN
            PERFORM spy.direction_fill_usual(h);
        END IF;
        IF h < win_from AND NOT EXISTS (SELECT 1 FROM spy.direction_hour_mark WHERE job = 'seen' AND hour = h) THEN
            PERFORM spy.direction_fill_seen(h);
        END IF;
    END LOOP;
    SELECT done_at INTO last_size FROM spy.direction_mark WHERE job = 'size';
    IF p_rebuild OR last_size IS NULL OR last_size < cur_hour THEN
        PERFORM spy.refresh_size();
    END IF;

    -- Now: sightings in the window (the only raw hours read), split by
    -- whether the publisher and device are usual for the subject ---------------
    CREATE TEMP TABLE IF NOT EXISTS spy_dir_now (
        kind TEXT, key TEXT, win_all BIGINT, win_usual BIGINT, last_hour TIMESTAMPTZ, PRIMARY KEY (kind, key)
    ) ON COMMIT DROP;
    TRUNCATE spy_dir_now;
    INSERT INTO spy_dir_now
    WITH cur AS (
        SELECT ad_id, publisher_id, device_id, sum(sightings) AS s, max(hour) AS last_hour
        FROM spy.ad_hourly WHERE hour >= win_from
        GROUP BY 1, 2, 3
    ),
    per AS (
        SELECT m.kind, m.key, c.publisher_id, c.device_id, sum(c.s) AS s, max(c.last_hour) AS last_hour
        FROM cur c JOIN spy.direction_member m ON m.ad_id = c.ad_id
        GROUP BY 1, 2, 3, 4
    )
    SELECT p.kind, p.key, sum(p.s), COALESCE(sum(p.s) FILTER (WHERE up.kind IS NOT NULL), 0), max(p.last_hour)
    FROM per p
    LEFT JOIN spy.direction_usual_publisher up
           ON up.kind = p.kind AND up.key = p.key AND up.publisher_id = p.publisher_id AND up.device_id = p.device_id
    GROUP BY 1, 2;
    ANALYZE spy_dir_now;

    -- Last hour seen in the stopped hours, window included.
    CREATE TEMP TABLE IF NOT EXISTS spy_dir_seen (kind TEXT, key TEXT, last_hour TIMESTAMPTZ, PRIMARY KEY (kind, key)) ON COMMIT DROP;
    TRUNCATE spy_dir_seen;
    INSERT INTO spy_dir_seen
    SELECT kind, key, max(last_hour) FROM (
        SELECT kind, key, last_hour FROM spy_dir_now
        UNION ALL
        SELECT kind, key, max(hour) FROM spy.direction_seen WHERE hour >= stop_from GROUP BY 1, 2
    ) x GROUP BY 1, 2;
    ANALYZE spy_dir_seen;

    -- Scrapes of each subject's usual publishers, in the window and in the stopped hours.
    CREATE TEMP TABLE IF NOT EXISTS spy_dir_scrapes (
        kind TEXT, key TEXT, sc_win BIGINT, sc_stop BIGINT, PRIMARY KEY (kind, key)
    ) ON COMMIT DROP;
    TRUNCATE spy_dir_scrapes;
    INSERT INTO spy_dir_scrapes
    WITH ph AS (
        SELECT publisher_id, device_id,
               COALESCE(sum(scrapes) FILTER (WHERE hour >= win_from), 0) AS sc_win,
               sum(scrapes) AS sc_stop
        FROM spy.publisher_hourly WHERE hour >= stop_from
        GROUP BY 1, 2
    )
    SELECT up.kind, up.key, sum(ph.sc_win), sum(ph.sc_stop)
    FROM spy.direction_usual_publisher up
    JOIN ph ON ph.publisher_id = up.publisher_id AND ph.device_id = up.device_id
    GROUP BY 1, 2;
    ANALYZE spy_dir_scrapes;

    -- Usual values summed over the slots. Shares use per-week averages, so a
    -- subject that existed fewer weeks than its vertical compares fairly.
    CREATE TEMP TABLE IF NOT EXISTS spy_dir_usual (
        kind TEXT, key TEXT, u_win BIGINT, u_sc_win BIGINT, u_all_win NUMERIC, u_sc_avg_win NUMERIC,
        weeks INTEGER, u_stop BIGINT, u_sc_stop BIGINT, PRIMARY KEY (kind, key)
    ) ON COMMIT DROP;
    TRUNCATE spy_dir_usual;
    INSERT INTO spy_dir_usual
    SELECT kind, key,
           COALESCE(sum(sightings) FILTER (WHERE hour >= win_from), 0),
           COALESCE(sum(scrapes) FILTER (WHERE hour >= win_from), 0),
           COALESCE(sum(sightings_all::numeric / weeks) FILTER (WHERE hour >= win_from), 0),
           COALESCE(sum(scrapes::numeric / weeks) FILTER (WHERE hour >= win_from), 0),
           COALESCE(max(weeks) FILTER (WHERE hour >= win_from), 0),
           sum(sightings), sum(scrapes)
    FROM spy.direction_usual WHERE hour >= stop_from
    GROUP BY 1, 2;
    ANALYZE spy_dir_usual;

    -- Who to judge: everything with data now or usually, plus live rows that
    -- may need to change.
    CREATE TEMP TABLE IF NOT EXISTS spy_dir_keys (kind TEXT, key TEXT, PRIMARY KEY (kind, key)) ON COMMIT DROP;
    TRUNCATE spy_dir_keys;
    INSERT INTO spy_dir_keys
    SELECT kind, key FROM spy_dir_seen
    UNION SELECT kind, key FROM spy_dir_usual
    UNION SELECT kind, key FROM spy.direction_stats WHERE direction NOT IN ('stopped', 'unclear');
    ANALYZE spy_dir_keys;

    -- Judge -------------------------------------------------------------------
    CREATE TEMP TABLE IF NOT EXISTS spy_dir_new (LIKE spy.direction_stats INCLUDING DEFAULTS) ON COMMIT DROP;
    TRUNCATE spy_dir_new;
    INSERT INTO spy_dir_new (kind, key, direction, direction_since, judged, window_from, now_sightings, now_scrapes,
                             usual_per_scrape, now_per_scrape, ratio, expected, z, share_now_pct, share_usual_pct,
                             share_ratio, market_ratio, rest_ratio, last_seen_hour, stop_expected, stop_scrapes,
                             reason, reason_text, refreshed_at)
    WITH net AS (
        SELECT COALESCE((SELECT sc_win FROM spy_dir_scrapes WHERE kind = 'network' AND key = '*'), 0) AS sc_now,
               COALESCE((SELECT u_sc_avg_win FROM spy_dir_usual WHERE kind = 'network' AND key = '*'), 0) AS sc_usual
    ),
    base AS (
        SELECT s.kind, s.key, s.market_kind, s.first_seen_at,
               COALESCE(nw.win_all, 0) AS win_all, COALESCE(nw.win_usual, 0) AS win_usual,
               se.last_hour,
               COALESCE(sc.sc_win, 0) AS sc_win, COALESCE(sc.sc_stop, 0) AS sc_stop,
               COALESCE(u.u_win, 0) AS u_win, COALESCE(u.u_sc_win, 0) AS u_sc_win,
               COALESCE(u.u_all_win, 0) AS u_all_win,
               COALESCE(u.u_stop, 0) AS u_stop, COALESCE(u.u_sc_stop, 0) AS u_sc_stop,
               -- Without a usual row: the weeks it existed, if it has usual publishers at all.
               CASE WHEN u.weeks > 0 THEN u.weeks
                    WHEN sc.kind IS NULL THEN 0
                    ELSE LEAST(s_usual_weeks::int,
                               GREATEST(0, floor(extract(epoch FROM (win_from - s.first_seen_at)) / 604800)::int))
               END AS weeks,
               COALESCE(mn.win_all, 0) AS m_win_all, COALESCE(mn.win_usual, 0) AS m_win_usual,
               COALESCE(mu.u_all_win, 0) AS m_all_usual,
               ms.sc_win AS m_sc_win, mu.u_win AS m_u_win, mu.u_sc_win AS m_u_sc_win,
               prev.direction AS prev_direction, prev.direction_since AS prev_since
        FROM spy_dir_keys k
        JOIN spy.direction_subject s ON s.kind = k.kind AND s.key = k.key
        LEFT JOIN spy_dir_now nw ON nw.kind = s.kind AND nw.key = s.key
        LEFT JOIN spy_dir_seen se ON se.kind = s.kind AND se.key = s.key
        LEFT JOIN spy_dir_scrapes sc ON sc.kind = s.kind AND sc.key = s.key
        LEFT JOIN spy_dir_usual u ON u.kind = s.kind AND u.key = s.key
        LEFT JOIN spy_dir_now mn ON mn.kind = s.market_kind AND mn.key = s.market_key
        LEFT JOIN spy_dir_usual mu ON mu.kind = s.market_kind AND mu.key = s.market_key
        LEFT JOIN spy_dir_scrapes ms ON ms.kind = s.market_kind AND ms.key = s.market_key
        LEFT JOIN spy.direction_stats prev ON prev.kind = s.kind AND prev.key = s.key
        WHERE s.kind <> 'network'
    ),
    calc AS (
        SELECT b.*,
               CASE WHEN b.u_sc_win > 0 THEN b.u_win::numeric / b.u_sc_win
                    WHEN b.weeks > 0 THEN 0 END AS usual_sps,
               CASE WHEN b.sc_win > 0 THEN b.win_usual::numeric / b.sc_win END AS now_sps,
               CASE WHEN b.u_sc_stop > 0 THEN b.u_stop::numeric / b.u_sc_stop * b.sc_stop
                    WHEN b.weeks > 0 THEN 0 END AS stop_expected,
               -- Share of voice, now and usual (usual from per-week averages).
               CASE WHEN b.m_win_all > 0 THEN b.win_all::numeric / b.m_win_all END AS share_now,
               CASE WHEN b.m_all_usual > 0 AND b.weeks > 0 THEN b.u_all_win / b.m_all_usual END AS share_usual,
               -- The whole vertical, per scrape of its usual publishers.
               CASE WHEN b.m_u_sc_win > 0 AND b.m_u_win > 0 AND b.m_sc_win > 0
                    THEN (b.m_win_usual::numeric / b.m_sc_win) / (b.m_u_win::numeric / b.m_u_sc_win) END AS market_ratio,
               -- The vertical without this one, per scrape of all publishers.
               CASE WHEN n.sc_now > 0 AND n.sc_usual > 0 AND b.m_all_usual - b.u_all_win > 0
                    THEN ((b.m_win_all - b.win_all)::numeric / n.sc_now)
                         / ((b.m_all_usual - b.u_all_win) / n.sc_usual) END AS rest_ratio,
               CASE WHEN b.last_hour IS NULL THEN floor(extract(epoch FROM (now_ts - stop_from)) / 3600)::int END AS unseen_hours
        FROM base b CROSS JOIN net n
    ),
    calc2 AS (
        SELECT c.*,
               c.usual_sps * c.sc_win AS expected,
               CASE WHEN c.usual_sps > 0 AND c.now_sps IS NOT NULL THEN c.now_sps / c.usual_sps END AS ratio,
               CASE WHEN c.share_usual > 0 AND c.share_now IS NOT NULL THEN c.share_now / c.share_usual END AS share_ratio
        FROM calc c
    ),
    calc3 AS (
        SELECT c.*,
               (c.win_usual - COALESCE(c.expected, 0)) / sqrt(GREATEST(COALESCE(c.expected, 0), 1)) AS z,
               c.weeks > 0 AND c.sc_win > 0 AND c.usual_sps IS NOT NULL
                 AND (c.expected >= s_min_expected OR c.win_usual >= s_min_expected) AS judgeable
        FROM calc2 c
    ),
    judged AS (
        SELECT c.*,
            CASE
                -- Not seen in the stopped hours.
                WHEN c.last_hour IS NULL AND COALESCE(c.stop_expected, 0) >= s_stopped_min_expected THEN 'stopped'
                WHEN c.last_hour IS NULL AND c.prev_direction = 'stopped' THEN 'stopped'
                WHEN c.last_hour IS NULL THEN 'unclear'
                -- Too little data: keep what it was.
                WHEN NOT c.judgeable THEN
                    CASE WHEN c.prev_direction IN ('rising', 'steady', 'fading', 'free_rise') THEN c.prev_direction ELSE 'unclear' END
                -- Ratio NULL: not usually seen at this hour, now seen enough.
                WHEN (c.ratio IS NULL OR c.ratio >= CASE WHEN c.prev_direction = 'rising' THEN s_rise_exit
                                                          ELSE s_rise_enter END)
                     AND c.z >= s_min_z THEN 'rising'
                WHEN c.ratio <= CASE WHEN c.prev_direction = 'fading' THEN s_fade_exit
                                     ELSE s_fade_enter END
                     AND c.z <= -s_min_z THEN 'fading'
                -- Free rise: share up, per scrape flat, the rest of the vertical down.
                WHEN c.share_ratio >= CASE WHEN c.prev_direction = 'free_rise' THEN s_rise_exit
                                           ELSE s_rise_enter END
                     AND c.ratio < s_rise_exit
                     AND c.ratio >= s_flat_low
                     AND c.rest_ratio <= s_free_rise_rest_max THEN 'free_rise'
                ELSE 'steady'
            END AS direction
        FROM calc3 c
    )
    SELECT j.kind, j.key, j.direction,
           CASE WHEN j.direction = j.prev_direction THEN j.prev_since ELSE now_ts END,
           CASE WHEN j.last_hour IS NULL THEN j.direction = 'stopped' ELSE j.judgeable END,
           win_from, j.win_usual, j.sc_win,
           round(j.usual_sps, 6), round(j.now_sps, 6), round(j.ratio, 3), round(j.expected, 2), round(j.z, 2),
           round(100 * j.share_now, 4), round(100 * j.share_usual, 4), round(j.share_ratio, 3),
           round(j.market_ratio, 3), round(j.rest_ratio, 3),
           j.last_hour, round(j.stop_expected, 2), j.sc_stop,
           jsonb_strip_nulls(jsonb_build_object(
               'slot', slot,
               'judged', CASE WHEN j.last_hour IS NULL THEN j.direction = 'stopped' ELSE j.judgeable END,
               'weeks', j.weeks,
               'now_sightings', j.win_usual,
               'now_scrapes', j.sc_win,
               'usual_per_scrape', round(j.usual_sps, 4),
               'now_per_scrape', round(j.now_sps, 4),
               'ratio', round(j.ratio, 2),
               'expected', round(j.expected, 1),
               'z', round(j.z, 1),
               'share_now_pct', round(100 * j.share_now, 3),
               'share_usual_pct', round(100 * j.share_usual, 3),
               'share_ratio', round(j.share_ratio, 2),
               'market_word', CASE WHEN j.market_kind = 'vertical' THEN 'vertical' ELSE 'all ads' END,
               'market_ratio', round(j.market_ratio, 2),
               'rest_ratio', round(j.rest_ratio, 2),
               'unseen_hours', j.unseen_hours,
               'stop_scrapes', CASE WHEN j.last_hour IS NULL THEN j.sc_stop END,
               'stop_expected', CASE WHEN j.last_hour IS NULL THEN round(j.stop_expected, 1) END,
               'flat_low', s_flat_low,
               'flat_high', s_flat_high)),
           '', now_ts
    FROM judged j;

    -- Push signs, only for what is rising (few rows): new publishers, now on
    -- both devices, new headlines and creatives in the same campaigns, and a
    -- higher NewsBreak bid.
    WITH rising AS (
        SELECT kind, key FROM spy_dir_new
        WHERE direction IN ('rising', 'free_rise') AND kind IN ('ad', 'creative', 'operator')
    ),
    rising_ads AS (
        SELECT r.kind, r.key, m.ad_id
        FROM rising r JOIN spy.direction_member m ON m.kind = r.kind AND m.key = r.key
    ),
    where_now AS (
        SELECT DISTINCT ra.kind, ra.key, x.publisher_id, x.device_id
        FROM rising_ads ra JOIN spy.ad_hourly x ON x.ad_id = ra.ad_id AND x.hour >= win_from
    ),
    spread AS (
        SELECT w.kind, w.key,
               count(DISTINCT w.publisher_id) FILTER (WHERE NOT EXISTS (
                   SELECT 1 FROM spy.direction_usual_publisher up
                   WHERE up.kind = w.kind AND up.key = w.key AND up.publisher_id = w.publisher_id)) AS new_pubs,
               count(DISTINCT w.device_id) AS devices_now,
               (SELECT count(DISTINCT up.device_id) FROM spy.direction_usual_publisher up
                WHERE up.kind = w.kind AND up.key = w.key) AS devices_usual
        FROM where_now w GROUP BY 1, 2
    ),
    camps AS (
        SELECT DISTINCT ra.kind, ra.key, cc.campaign_id
        FROM rising_ads ra
        JOIN spy.ad a ON a.id = ra.ad_id
        JOIN spy.creative_campaign cc ON cc.creative_id = a.creative_id
        WHERE cc.last_seen_at >= now_ts - interval '7 days'
    ),
    new_in_camp AS (
        SELECT c.kind, c.key,
               count(DISTINCT a.id) FILTER (WHERE a.first_seen_at >= now_ts - make_interval(hours => s_push_hours::int)) AS new_ads,
               count(DISTINCT cc.creative_id) FILTER (WHERE cc.first_seen_at >= now_ts - make_interval(hours => s_push_hours::int)) AS new_creatives
        FROM camps c
        JOIN spy.creative_campaign cc ON cc.campaign_id = c.campaign_id
        LEFT JOIN spy.ad a ON a.creative_id = cc.creative_id
        GROUP BY 1, 2
    ),
    bids AS (
        SELECT ra.kind, ra.key,
               avg(s.bid_price) FILTER (WHERE s.seen_at >= win_from) AS bid_now,
               avg(s.bid_price) FILTER (WHERE s.seen_at < win_from) AS bid_before
        FROM rising_ads ra
        JOIN spy.sighting s ON s.ad_id = ra.ad_id
        WHERE s.seen_at >= win_from - interval '24 hours' AND s.bid_price IS NOT NULL
        GROUP BY 1, 2
    ),
    signs AS (
        SELECT r.kind, r.key,
               NULLIF(COALESCE(sp.new_pubs, 0), 0) AS new_pubs,
               sp.devices_now > 1 AND sp.devices_usual = 1 AS both_devices,
               NULLIF(COALESCE(n.new_ads, 0), 0) AS new_ads,
               NULLIF(COALESCE(n.new_creatives, 0), 0) AS new_creatives,
               b.bid_now, b.bid_before
        FROM rising r
        LEFT JOIN spread sp ON sp.kind = r.kind AND sp.key = r.key
        LEFT JOIN new_in_camp n ON n.kind = r.kind AND n.key = r.key
        LEFT JOIN bids b ON b.kind = r.kind AND b.key = r.key
    )
    UPDATE spy_dir_new d SET reason = d.reason || jsonb_strip_nulls(jsonb_build_object(
        'new_publishers', s.new_pubs,
        'both_devices', CASE WHEN s.both_devices THEN TRUE END,
        'new_ads_in_campaign', s.new_ads,
        'new_creatives_in_campaign', s.new_creatives,
        'bid_now', round(s.bid_now::numeric, 2),
        'bid_before', round(s.bid_before::numeric, 2),
        'bid_up', CASE WHEN s.bid_before > 0 AND s.bid_now >= s.bid_before * s_bid_rise THEN TRUE END))
    FROM signs s
    WHERE s.kind = d.kind AND s.key = d.key;

    -- Every change of direction becomes an event. The first time a subject is
    -- judged is not a change.
    INSERT INTO spy.direction_event (at, kind, key, from_direction, to_direction, reason, reason_text)
    SELECT now_ts, d.kind, d.key, p.direction, d.direction, d.reason, spy.direction_reason_text(d.direction, d.reason)
    FROM spy_dir_new d
    JOIN spy.direction_stats p ON p.kind = d.kind AND p.key = d.key
    WHERE p.direction <> d.direction;

    -- Rows whose numbers did not move are left alone. refreshed_at is the
    -- last time they moved.
    INSERT INTO spy.direction_stats AS ds (kind, key, direction, direction_since, judged, window_from, now_sightings,
                                           now_scrapes, usual_per_scrape, now_per_scrape, ratio, expected, z,
                                           share_now_pct, share_usual_pct, share_ratio, market_ratio, rest_ratio,
                                           last_seen_hour, stop_expected, stop_scrapes, reason, reason_text, refreshed_at)
    SELECT d.kind, d.key, d.direction, d.direction_since, d.judged, d.window_from, d.now_sightings,
           d.now_scrapes, d.usual_per_scrape, d.now_per_scrape, d.ratio, d.expected, d.z,
           d.share_now_pct, d.share_usual_pct, d.share_ratio, d.market_ratio, d.rest_ratio,
           d.last_seen_hour, d.stop_expected, d.stop_scrapes, d.reason,
           spy.direction_reason_text(d.direction, d.reason), d.refreshed_at
    FROM spy_dir_new d
    LEFT JOIN spy.direction_stats p ON p.kind = d.kind AND p.key = d.key
    WHERE p.kind IS NULL OR p.direction <> d.direction OR p.reason IS DISTINCT FROM d.reason
    ON CONFLICT (kind, key) DO UPDATE SET
        direction = EXCLUDED.direction,
        direction_since = EXCLUDED.direction_since,
        judged = EXCLUDED.judged,
        window_from = EXCLUDED.window_from,
        now_sightings = EXCLUDED.now_sightings,
        now_scrapes = EXCLUDED.now_scrapes,
        usual_per_scrape = EXCLUDED.usual_per_scrape,
        now_per_scrape = EXCLUDED.now_per_scrape,
        ratio = EXCLUDED.ratio,
        expected = EXCLUDED.expected,
        z = EXCLUDED.z,
        share_now_pct = EXCLUDED.share_now_pct,
        share_usual_pct = EXCLUDED.share_usual_pct,
        share_ratio = EXCLUDED.share_ratio,
        market_ratio = EXCLUDED.market_ratio,
        rest_ratio = EXCLUDED.rest_ratio,
        last_seen_hour = COALESCE(EXCLUDED.last_seen_hour, ds.last_seen_hour),
        stop_expected = EXCLUDED.stop_expected,
        stop_scrapes = EXCLUDED.stop_scrapes,
        reason = EXCLUDED.reason,
        reason_text = EXCLUDED.reason_text,
        refreshed_at = EXCLUDED.refreshed_at;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END;
$$;
