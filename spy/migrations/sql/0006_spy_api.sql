-- The published face of Spy, version 1. Each statement is the matching file
-- in contract/sql/spy/, word for word (a test checks). Other services and
-- the Spy app read these and nothing else.

CREATE SCHEMA spy_api;

CREATE VIEW spy_api.operator_v1 AS
SELECT id, code, name, display_name, kind, vertical FROM spy.operator;

-- Which operator each Tracks account belongs to. No row: not grouped yet.
CREATE VIEW spy_api.account_operator_v1 AS
SELECT account_id, operator_id FROM spy.account_operator;

-- The vertical of each Tracks creative. No row: not classified yet.
CREATE VIEW spy_api.creative_vertical_v1 AS
SELECT creative_id, vertical, subvertical, shown_vertical, confidence, unsure, source, health_from_funnel
FROM spy.creative_vertical;

-- One row per creative, rebuilt every 5 minutes: facts only (counts, who
-- and what, first and last seen). Windows are UTC days including today.
-- Anything per check or against usual comes from creative_range_v1.
CREATE VIEW spy_api.creative_stats_v1 AS
SELECT creative_id, sightings_total, sightings_today, sightings_yesterday, sightings_3d, sightings_prev_3d,
       sightings_7d, sightings_prev_7d, sightings_30d, scrapes_total, active_days, ads_count, publishers_count,
       publisher_ids, network_share_7d_pct, is_new, running, top_ad_id, headline, brand_id, operator_id,
       account_ids, trackers, affiliate_networks, vertical, classified_vertical, subvertical, vertical_confidence,
       unsure, vertical_source, health_from_funnel, is_junk, first_seen_at, last_seen_at, refreshed_at
FROM spy.creative_stats;

CREATE VIEW spy_api.operator_stats_v1 AS
SELECT operator_id, sightings_total, sightings_today, sightings_7d, sightings_prev_7d, sightings_30d,
       share_today_pct, share_7d_pct, share_30d_pct, creatives_count, live_creatives_count, publisher_ids,
       vertical, brands, first_seen_at, last_seen_at, refreshed_at
FROM spy.operator_stats;

CREATE VIEW spy_api.publisher_stats_v1 AS
SELECT publisher_id, sightings_total, sightings_today, sightings_7d, sightings_30d, share_30d_pct, scrapes_30d,
       phone_sightings_30d, creatives_30d, operators_30d, operator_hhi, even_operators, top_operators,
       top_creatives, verticals, first_seen_at, last_seen_at, refreshed_at
FROM spy.publisher_stats;

-- Where the last 24 hours end (the first hour Tracks has not closed), for
-- creative_recent_v1 and operator_recent_v1.
CREATE VIEW spy_api.recent_window_v1 AS
SELECT window_end, network_sightings_24h, refreshed_at FROM spy.recent_window;

-- creative_range_v1 for the last 24 closed hours, kept ready.
CREATE VIEW spy_api.creative_recent_v1 AS
SELECT creative_id, network_id, sightings, sightings_usual, checks, checks_usual, presence, presence_usual,
       phone_presence, desktop_presence, share_pct, share_usual_pct, share_gain_pts, rank, momentum, momentum_low,
       momentum_high, momentum_word, momentum_sure, momentum_rank,
       fall_on_one_publisher, noise, usual_periods, publishers, first_seen_at, vertical,
       vertical_share_pct, vertical_rank, scaled, last_seen_at, running, lifespan_days, lifespan_pct,
       lifespan_curve, operator_id, is_junk
FROM spy.creative_recent;

-- operator_range_v1 for the last 24 closed hours, kept ready.
CREATE VIEW spy_api.operator_recent_v1 AS
SELECT operator_id, network_id, sightings, sightings_usual, checks, checks_usual, presence, presence_usual,
       phone_presence, desktop_presence, share_pct, share_usual_pct, share_gain_pts, rank, momentum, momentum_low,
       momentum_high, momentum_word, momentum_sure, momentum_rank,
       fall_on_one_publisher, noise, usual_periods, publishers, first_seen_at, launches, hits,
       misses, testing, hit_rate_pct, hit_rate_low_pct, hit_rate_high_pct
FROM spy.operator_recent;

-- Direction now, per ad, creative, operator and vertical (kind, key;
-- subject_id is the id for the first three). Every 5 minutes.
CREATE VIEW spy_api.direction_v1 AS
SELECT kind, key, subject_id, direction, direction_since, judged, window_from, now_sightings, now_scrapes,
       usual_per_scrape, now_per_scrape, ratio, expected, z, share_now_pct, share_usual_pct, share_ratio,
       market_ratio, rest_ratio, last_seen_hour, stop_expected, stop_scrapes, reason, reason_text, refreshed_at
FROM spy.direction_stats;

-- Every change of direction, for alerts and watches.
CREATE VIEW spy_api.direction_event_v1 AS
SELECT id, at, kind, key, subject_id, from_direction, to_direction, reason, reason_text FROM spy.direction_event;

CREATE VIEW spy_api.size_v1 AS
SELECT kind, key, subject_id, market_kind, market_key, sightings_24h, share_24h_pct, rank_24h, sightings_7d,
       share_7d_pct, rank_7d, share_7d_before_pct, scaled, refreshed_at
FROM spy.size_stats;

-- The range the range functions read for [p_from, p_to): precision ('hour'
-- or 'day'), its ends, its usual period ('weeks': the same hours 1-3 weeks
-- back; 'before': the whole weeks just before; 'chosen': p_usual_from to
-- p_usual_to), and hours without a single check (collection was down).
CREATE FUNCTION spy_api.range_info_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_usual_from TIMESTAMPTZ DEFAULT NULL,
                                      p_usual_to TIMESTAMPTZ DEFAULT NULL)
RETURNS TABLE (prec TEXT, from_at TIMESTAMPTZ, to_at TIMESTAMPTZ, usual_kind TEXT, usual_periods INTEGER,
               usual_from TIMESTAMPTZ, usual_to TIMESTAMPTZ, hours INTEGER, hours_unchecked INTEGER,
               checks BIGINT, sightings BIGINT, checks_usual BIGINT, sightings_usual BIGINT)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.range_info(p_from, p_to, p_usual_from, p_usual_to, now()) $$;

-- Creatives over any range, one row per creative and network. See
-- spy/METRICS.md for every column.
CREATE FUNCTION spy_api.creative_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ,
                                          p_usual_from TIMESTAMPTZ DEFAULT NULL, p_usual_to TIMESTAMPTZ DEFAULT NULL)
RETURNS TABLE (creative_id INTEGER, network_id INTEGER, sightings BIGINT, sightings_usual BIGINT, checks BIGINT,
               checks_usual BIGINT, presence NUMERIC, presence_usual NUMERIC, phone_presence NUMERIC,
               desktop_presence NUMERIC, share_pct NUMERIC, share_usual_pct NUMERIC, share_gain_pts NUMERIC,
               rank INTEGER, momentum NUMERIC, momentum_low NUMERIC, momentum_high NUMERIC, momentum_word TEXT,
               momentum_sure TEXT, momentum_rank INTEGER,
               fall_on_one_publisher BOOLEAN, noise NUMERIC, usual_periods INTEGER, publishers INTEGER,
               first_seen_at TIMESTAMPTZ, vertical TEXT, vertical_share_pct NUMERIC,
               vertical_rank INTEGER, scaled BOOLEAN, last_seen_at TIMESTAMPTZ, running BOOLEAN,
               lifespan_days INTEGER, lifespan_pct NUMERIC, lifespan_curve TEXT, operator_id INTEGER,
               is_junk BOOLEAN)
LANGUAGE sql
AS $$ SELECT * FROM spy.creative_range(p_from, p_to, p_usual_from, p_usual_to, now()) $$;

-- Operators over any range, one row per operator and network, with their
-- launches in the range and their hit rate. See spy/METRICS.md.
CREATE FUNCTION spy_api.operator_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ,
                                          p_usual_from TIMESTAMPTZ DEFAULT NULL, p_usual_to TIMESTAMPTZ DEFAULT NULL)
RETURNS TABLE (operator_id INTEGER, network_id INTEGER, sightings BIGINT, sightings_usual BIGINT, checks BIGINT,
               checks_usual BIGINT, presence NUMERIC, presence_usual NUMERIC, phone_presence NUMERIC,
               desktop_presence NUMERIC, share_pct NUMERIC, share_usual_pct NUMERIC, share_gain_pts NUMERIC,
               rank INTEGER, momentum NUMERIC, momentum_low NUMERIC, momentum_high NUMERIC, momentum_word TEXT,
               momentum_sure TEXT, momentum_rank INTEGER,
               fall_on_one_publisher BOOLEAN, noise NUMERIC, usual_periods INTEGER, publishers INTEGER,
               first_seen_at TIMESTAMPTZ, launches INTEGER, hits INTEGER, misses INTEGER,
               testing INTEGER, hit_rate_pct NUMERIC, hit_rate_low_pct NUMERIC, hit_rate_high_pct NUMERIC)
LANGUAGE sql
AS $$ SELECT * FROM spy.operator_range(p_from, p_to, p_usual_from, p_usual_to, now()) $$;

-- Verticals over any range, one row per vertical and network. See
-- spy/METRICS.md.
CREATE FUNCTION spy_api.vertical_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ,
                                          p_usual_from TIMESTAMPTZ DEFAULT NULL, p_usual_to TIMESTAMPTZ DEFAULT NULL)
RETURNS TABLE (vertical TEXT, network_id INTEGER, sightings BIGINT, sightings_usual BIGINT, checks BIGINT,
               checks_usual BIGINT, presence NUMERIC, presence_usual NUMERIC, phone_presence NUMERIC,
               desktop_presence NUMERIC, share_pct NUMERIC, share_usual_pct NUMERIC, share_gain_pts NUMERIC,
               rank INTEGER, momentum NUMERIC, momentum_low NUMERIC, momentum_high NUMERIC, momentum_word TEXT,
               momentum_sure TEXT, momentum_rank INTEGER,
               fall_on_one_publisher BOOLEAN, noise NUMERIC, usual_periods INTEGER, publishers INTEGER,
               first_seen_at TIMESTAMPTZ)
LANGUAGE sql
AS $$ SELECT * FROM spy.subject_range('vertical', p_from, p_to, p_usual_from, p_usual_to, now()) $$;

-- Publishers over any range: checks, sightings per 100 checks, share of the
-- network's sightings, and operator concentration as a number of equal
-- operators.
CREATE FUNCTION spy_api.publisher_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ)
RETURNS TABLE (publisher_id INTEGER, network_id INTEGER, checks BIGINT, sightings BIGINT, per_100_checks NUMERIC,
               share_pct NUMERIC, hours_checked INTEGER, operators INTEGER, even_operators NUMERIC)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.publisher_range(p_from, p_to, now()) $$;

-- Readers get the published views through the spy_api_read role, which the
-- box setup creates and grants to each reading login. The range functions
-- run as their owner, so a reader needs no grant on spy or tracks_api.
ALTER FUNCTION spy_api.range_info_v1(TIMESTAMPTZ, TIMESTAMPTZ, TIMESTAMPTZ, TIMESTAMPTZ) SECURITY DEFINER SET search_path = pg_catalog, pg_temp;
ALTER FUNCTION spy_api.creative_range_v1(TIMESTAMPTZ, TIMESTAMPTZ, TIMESTAMPTZ, TIMESTAMPTZ) SECURITY DEFINER SET search_path = pg_catalog, pg_temp;
ALTER FUNCTION spy_api.operator_range_v1(TIMESTAMPTZ, TIMESTAMPTZ, TIMESTAMPTZ, TIMESTAMPTZ) SECURITY DEFINER SET search_path = pg_catalog, pg_temp;
ALTER FUNCTION spy_api.vertical_range_v1(TIMESTAMPTZ, TIMESTAMPTZ, TIMESTAMPTZ, TIMESTAMPTZ) SECURITY DEFINER SET search_path = pg_catalog, pg_temp;
ALTER FUNCTION spy_api.publisher_range_v1(TIMESTAMPTZ, TIMESTAMPTZ) SECURITY DEFINER SET search_path = pg_catalog, pg_temp;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'spy_api_read') THEN
        GRANT USAGE ON SCHEMA spy_api TO spy_api_read;
        GRANT SELECT ON ALL TABLES IN SCHEMA spy_api TO spy_api_read;
        GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA spy_api TO spy_api_read;
    END IF;
END;
$$;
