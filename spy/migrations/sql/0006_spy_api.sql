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

-- One row per creative, rebuilt every 5 minutes. Windows are UTC days
-- including today; for any other range use creative_range_v1.
CREATE VIEW spy_api.creative_stats_v1 AS
SELECT creative_id, sightings_total, sightings_today, sightings_yesterday, sightings_3d, sightings_prev_3d,
       sightings_7d, sightings_prev_7d, sightings_30d, scrapes_total, active_days, ads_count, publishers_count,
       publisher_ids, phone_share_pct, network_share_7d_pct, momentum_pct, avg_feed_position, peak_day,
       peak_day_sightings, drop_from_peak_pct, stage, top_ad_id, headline, brand_id, operator_id, account_ids,
       trackers, affiliate_networks, vertical, classified_vertical, subvertical, vertical_confidence, unsure,
       vertical_source, health_from_funnel, is_junk, first_seen_at, last_seen_at, refreshed_at
FROM spy.creative_stats;

CREATE VIEW spy_api.operator_stats_v1 AS
SELECT operator_id, sightings_total, sightings_today, sightings_7d, sightings_prev_7d, sightings_30d,
       share_today_pct, share_7d_pct, share_30d_pct, momentum_pct, creatives_count, live_creatives_count,
       publisher_ids, phone_share_pct, vertical, brands, first_seen_at, last_seen_at, refreshed_at
FROM spy.operator_stats;

CREATE VIEW spy_api.publisher_stats_v1 AS
SELECT publisher_id, sightings_total, sightings_today, sightings_7d, sightings_30d, share_30d_pct, scrapes_30d,
       phone_sightings_30d, creatives_30d, operators_30d, operator_hhi, top_operators, top_creatives, verticals,
       first_seen_at, last_seen_at, refreshed_at
FROM spy.publisher_stats;

-- Where the last 24 hours end (the last closed hour), for creative_recent_v1
-- and operator_recent_v1.
CREATE VIEW spy_api.recent_window_v1 AS
SELECT window_end, network_sightings_24h, refreshed_at FROM spy.recent_window;

CREATE VIEW spy_api.creative_recent_v1 AS
SELECT creative_id, sightings_24h, sightings_prev_24h, rate_24h, rate_prev_24h, peak_end, peak_sightings, peak_rate
FROM spy.creative_recent;

CREATE VIEW spy_api.operator_recent_v1 AS
SELECT operator_id, sightings_24h, sightings_prev_24h, rate_24h, rate_prev_24h FROM spy.operator_recent;

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

-- The range the range functions actually read for [p_from, p_to): its
-- precision ('hour' or 'day'), its ends, the period before it, and how much
-- of both Tracks covered.
CREATE FUNCTION spy_api.range_info_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ)
RETURNS TABLE (prec TEXT, from_at TIMESTAMPTZ, to_at TIMESTAMPTZ, before_from TIMESTAMPTZ, before_to TIMESTAMPTZ,
               hours INTEGER, hours_closed INTEGER, hours_closed_before INTEGER,
               scrapes BIGINT, scrapes_before BIGINT, sightings BIGINT, sightings_before BIGINT)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.range_info(p_from, p_to, now()) $$;

-- Creatives over any range, against the period of the same length before it.
CREATE FUNCTION spy_api.creative_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ)
RETURNS TABLE (creative_id INTEGER, sightings BIGINT, sightings_before BIGINT, rate DOUBLE PRECISION,
               rate_before DOUBLE PRECISION, momentum_pct NUMERIC, momentum_z NUMERIC, momentum_sure BOOLEAN,
               share_pct NUMERIC, rank INTEGER, vertical TEXT, rank_in_vertical INTEGER, publishers_count INTEGER,
               phone_share_pct NUMERIC, is_junk BOOLEAN, first_seen_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.creative_range(p_from, p_to, now()) $$;

-- Operators over any range, against the period of the same length before it.
CREATE FUNCTION spy_api.operator_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ)
RETURNS TABLE (operator_id INTEGER, sightings BIGINT, sightings_before BIGINT, rate DOUBLE PRECISION,
               rate_before DOUBLE PRECISION, momentum_pct NUMERIC, momentum_z NUMERIC, momentum_sure BOOLEAN,
               share_pct NUMERIC, rank INTEGER, creatives_count INTEGER, publishers_count INTEGER,
               phone_share_pct NUMERIC)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.operator_range(p_from, p_to, now()) $$;

-- Publishers over any range, against the period of the same length before it.
CREATE FUNCTION spy_api.publisher_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ)
RETURNS TABLE (publisher_id INTEGER, sightings BIGINT, sightings_before BIGINT, scrapes BIGINT, scrapes_before BIGINT,
               per_scrape NUMERIC, per_scrape_before NUMERIC, change_pct NUMERIC, change_z NUMERIC,
               change_sure BOOLEAN, share_pct NUMERIC, phone_share_pct NUMERIC)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.publisher_range(p_from, p_to, now()) $$;

-- Readers get the published views through the spy_api_read role, which the
-- box setup creates and grants to each reading login. The range functions
-- run as their owner, so a reader needs no grant on spy or tracks_api.
ALTER FUNCTION spy_api.range_info_v1(TIMESTAMPTZ, TIMESTAMPTZ) SECURITY DEFINER SET search_path = pg_catalog, pg_temp;
ALTER FUNCTION spy_api.creative_range_v1(TIMESTAMPTZ, TIMESTAMPTZ) SECURITY DEFINER SET search_path = pg_catalog, pg_temp;
ALTER FUNCTION spy_api.operator_range_v1(TIMESTAMPTZ, TIMESTAMPTZ) SECURITY DEFINER SET search_path = pg_catalog, pg_temp;
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
