-- Publishers over any range: checks, sightings per 100 checks, share of the
-- network's sightings, and operator concentration as a number of equal
-- operators.
CREATE FUNCTION spy_api.publisher_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ)
RETURNS TABLE (publisher_id INTEGER, network_id INTEGER, checks BIGINT, sightings BIGINT, per_100_checks NUMERIC,
               share_pct NUMERIC, hours_checked INTEGER, operators INTEGER, even_operators NUMERIC)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.publisher_range(p_from, p_to, now()) $$;
