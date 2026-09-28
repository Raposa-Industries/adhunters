-- Publishers over any range, against the period of the same length before it.
CREATE FUNCTION spy_api.publisher_range_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ)
RETURNS TABLE (publisher_id INTEGER, sightings BIGINT, sightings_before BIGINT, scrapes BIGINT, scrapes_before BIGINT,
               per_scrape NUMERIC, per_scrape_before NUMERIC, change_pct NUMERIC, change_z NUMERIC,
               change_sure BOOLEAN, share_pct NUMERIC, phone_share_pct NUMERIC)
LANGUAGE sql STABLE
AS $$ SELECT * FROM spy.publisher_range(p_from, p_to, now()) $$;
