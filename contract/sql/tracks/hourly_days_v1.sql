-- Where the hour-by-hour counts of each UTC day from p_from to p_to are:
-- 'database' (read them from ad_hourly_v1 and ad_account_brand_hourly_v1),
-- 'archive' (moved to the archive after 35 days; the day's daily counts are
-- still in the database) or 'coming' (asked for: the loader brings them back
-- within a minute or two). With p_bring_back, the archived days of the range
-- (15 at most) are asked for and read 'coming', and days already back are
-- kept 3 days from now. kept_until is when a brought-back day goes again.
CREATE FUNCTION tracks_api.hourly_days_v1(p_from TIMESTAMPTZ, p_to TIMESTAMPTZ, p_bring_back BOOLEAN)
RETURNS TABLE (day DATE, state TEXT, kept_until TIMESTAMPTZ)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    d0 DATE := (p_from AT TIME ZONE 'UTC')::date;
    d1 DATE := ((p_to - interval '1 microsecond') AT TIME ZONE 'UTC')::date;
BEGIN
    IF p_from IS NULL OR p_to IS NULL OR p_to <= p_from THEN
        RAISE EXCEPTION 'a range needs a start before its end (got % to %)', p_from, p_to;
    END IF;
    IF p_bring_back THEN
        IF d1 - d0 >= 15 THEN
            RAISE EXCEPTION 'hours come back 15 days at a time at most (asked % to %)', d0, d1;
        END IF;
        UPDATE tracks.hour_bring_back b SET keep_until = GREATEST(b.keep_until, now() + interval '3 days'),
            attempts = CASE WHEN b.loaded_at IS NULL THEN 0 ELSE b.attempts END
        WHERE b.day BETWEEN d0 AND d1;
        INSERT INTO tracks.hour_bring_back (day, keep_until)
        SELECT s.day, now() + interval '3 days' FROM tracks.hourly_day_state(d0, d1) s WHERE s.state = 'archive'
        ON CONFLICT ON CONSTRAINT hour_bring_back_pkey DO NOTHING;
    END IF;
    RETURN QUERY SELECT s.day, s.state, s.kept_until FROM tracks.hourly_day_state(d0, d1) s;
END;
$$;
