-- The range the range functions actually read for [p_from, p_to): its
-- precision ('hour' or 'day'), its ends, the period before it, and how much
-- of both Tracks covered.
CREATE FUNCTION spy_api.range_info_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ)
RETURNS TABLE (prec TEXT, from_at TIMESTAMPTZ, to_at TIMESTAMPTZ, before_from TIMESTAMPTZ, before_to TIMESTAMPTZ,
               hours INTEGER, hours_closed INTEGER, hours_closed_before INTEGER,
               scrapes BIGINT, scrapes_before BIGINT, sightings BIGINT, sightings_before BIGINT)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.range_info(p_from, p_to, now()) $$;
