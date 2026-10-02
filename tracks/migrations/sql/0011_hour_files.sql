-- lint: new-table
-- Hourly counts move to the archive after 35 days (decision 0023). Each UTC
-- day of ad_hourly and ad_account_brand_hourly is written to one Parquet file
-- in the archive (an hour file) and read back, hour by hour, before it is
-- recorded here. A month's hourly partitions are dropped only by
-- `tracks-loader hourly drop`, once every day of it has a matching hour file;
-- its daily counts stay in the database. A page can ask for an archived
-- day's hours back (tracks_api.hourly_days_v1), and the loader keeps them in
-- the database for a few days.

-- One row per hour file written. A day closed again after it was written (a
-- replay) is written again under a new version; older files stay in the
-- archive.
CREATE TABLE tracks.hour_file (
    tbl TEXT NOT NULL CHECK (tbl IN ('ad_hourly', 'ad_account_brand_hourly')),
    day DATE NOT NULL,
    version INTEGER NOT NULL,
    key TEXT NOT NULL UNIQUE,           -- hourly/<tbl>/<yyyy>/<mm>/<dd>-v<n>.parquet
    sha256 TEXT NOT NULL,
    bytes BIGINT NOT NULL,
    rows BIGINT NOT NULL,
    sightings BIGINT NOT NULL,
    hours INTEGER NOT NULL,             -- hours with at least one row
    counts_as_of TIMESTAMPTZ NOT NULL,  -- the day's last close or import when it was written
    written_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tbl, day, version)
);

-- Months whose hourly partitions were dropped after every day matched its
-- hour files. dropped_at moves when the loader drops them again (after days
-- were brought back, or a replay closed some again and they were rewritten).
CREATE TABLE tracks.archived_month (
    month DATE PRIMARY KEY CHECK (month = date_trunc('month', month)::date),
    dropped_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    days INTEGER NOT NULL,
    hourly_rows BIGINT NOT NULL,        -- ad_hourly rows dropped the first time
    brand_rows BIGINT NOT NULL,         -- ad_account_brand_hourly rows
    sightings BIGINT NOT NULL           -- ad_hourly sightings
);

-- Archived days a page asked for. The loader loads their hour files back
-- into the hourly tables; once keep_until has passed it drops them again.
CREATE TABLE tracks.hour_bring_back (
    day DATE PRIMARY KEY,
    asked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    keep_until TIMESTAMPTZ NOT NULL,
    loaded_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT
);

-- Where each day's hourly counts are: 'database', 'archive' (an archived
-- month's day with an hour file) or 'coming' (asked for, not loaded yet; a
-- day that failed to load 3 times reads 'archive' until asked again). A day
-- of an archived month is in the database again when it was brought back,
-- or closed again after the drop (a replay) and not dropped since.
CREATE FUNCTION tracks.hourly_day_state(p_from DATE, p_to DATE)
RETURNS TABLE (day DATE, state TEXT, kept_until TIMESTAMPTZ)
LANGUAGE sql STABLE AS $$
    SELECT g.day,
           CASE WHEN m.month IS NULL OR b.loaded_at IS NOT NULL
                     OR NOT EXISTS (SELECT 1 FROM tracks.hour_file f WHERE f.day = g.day)
                     OR EXISTS (SELECT 1 FROM tracks.hour_state h
                                WHERE h.hour >= g.day::timestamp AT TIME ZONE 'UTC'
                                  AND h.hour < (g.day + 1)::timestamp AT TIME ZONE 'UTC'
                                  AND GREATEST(h.closed_at, h.imported_at) > m.dropped_at)
                THEN 'database'
                WHEN b.day IS NOT NULL AND b.attempts < 3 THEN 'coming'
                ELSE 'archive' END,
           b.keep_until
    FROM (SELECT d::date AS day FROM generate_series(p_from::timestamp, p_to::timestamp, interval '1 day') d) g
    LEFT JOIN tracks.archived_month m ON m.month = date_trunc('month', g.day::timestamp)::date
    LEFT JOIN tracks.hour_bring_back b ON b.day = g.day
    ORDER BY g.day
$$;

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

REVOKE EXECUTE ON FUNCTION tracks_api.hourly_days_v1(TIMESTAMPTZ, TIMESTAMPTZ, BOOLEAN) FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tracks_api_read') THEN
        GRANT EXECUTE ON FUNCTION tracks_api.hourly_days_v1(TIMESTAMPTZ, TIMESTAMPTZ, BOOLEAN) TO tracks_api_read;
    END IF;
END;
$$;
