-- Creatives over any range, against the period of the same length before it.
CREATE FUNCTION spy_api.creative_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ)
RETURNS TABLE (creative_id INTEGER, sightings BIGINT, sightings_before BIGINT, rate DOUBLE PRECISION,
               rate_before DOUBLE PRECISION, momentum_pct NUMERIC, momentum_z NUMERIC, momentum_sure BOOLEAN,
               share_pct NUMERIC, rank INTEGER, vertical TEXT, rank_in_vertical INTEGER, publishers_count INTEGER,
               phone_share_pct NUMERIC, is_junk BOOLEAN, first_seen_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.creative_range(p_from, p_to, now()) $$;
