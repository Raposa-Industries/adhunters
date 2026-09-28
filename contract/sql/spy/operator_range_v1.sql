-- Operators over any range, against the period of the same length before it.
CREATE FUNCTION spy_api.operator_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ)
RETURNS TABLE (operator_id INTEGER, sightings BIGINT, sightings_before BIGINT, rate DOUBLE PRECISION,
               rate_before DOUBLE PRECISION, momentum_pct NUMERIC, momentum_z NUMERIC, momentum_sure BOOLEAN,
               share_pct NUMERIC, rank INTEGER, creatives_count INTEGER, publishers_count INTEGER,
               phone_share_pct NUMERIC)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.operator_range(p_from, p_to, now()) $$;
