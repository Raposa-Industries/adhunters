-- Where closed hours stop, as of p_now: the first hour not closed yet,
-- counting from the oldest closed hour of the last 6 days. NULL before any.
--
-- 0003 counted from the last 15 days. Tracks fills in the hours of the last
-- 7 days that had no scrapes and closes them empty, but older holes stay
-- holes for good: the collector's history (Tracks import-old) has hours the
-- collector never scraped. A hole 8 to 15 days back held the last 24 hours
-- there, and every range ending at it failed with "no closed day yet".
-- Within 6 days every hour is either closed or about to close.
CREATE OR REPLACE FUNCTION spy.recent_end(p_now TIMESTAMPTZ) RETURNS TIMESTAMPTZ
LANGUAGE plpgsql STABLE AS $$
DECLARE
    cur TIMESTAMPTZ := date_trunc('hour', p_now, 'UTC');
    first_closed TIMESTAMPTZ;
BEGIN
    SELECT min(hour) INTO first_closed FROM tracks_api.closed_hour_v1
    WHERE hour >= cur - interval '6 days' AND hour < cur;
    IF first_closed IS NULL THEN
        RETURN NULL;
    END IF;
    RETURN (SELECT min(g) FROM generate_series(first_closed, cur, interval '1 hour') g
            WHERE NOT EXISTS (SELECT 1 FROM tracks_api.closed_hour_v1 c WHERE c.hour = g));
END;
$$;
