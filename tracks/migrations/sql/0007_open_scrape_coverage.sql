-- lint: new-table
-- Scrapes of the hours not closed yet, beside ad_hourly_open, so a reader of
-- the open hours' sightings (Spy's Direction) has their denominator too.
-- refresh_open_hours() now rewrites both every 5 minutes.

CREATE TABLE tracks.publisher_hourly_open (
    hour TIMESTAMPTZ NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    scrapes INTEGER NOT NULL,
    answered INTEGER NOT NULL,
    failed INTEGER NOT NULL,
    sightings INTEGER NOT NULL,
    PRIMARY KEY (hour, publisher_id, device_id)
);

-- As in 0004, plus publisher_hourly_open from the scrapes of the same hours.
CREATE OR REPLACE FUNCTION tracks.refresh_open_hours(p_from TIMESTAMPTZ) RETURNS BIGINT
LANGUAGE plpgsql AS $$
DECLARE
    n BIGINT;
BEGIN
    DELETE FROM tracks.ad_hourly_open;
    INSERT INTO tracks.ad_hourly_open (hour, ad_id, publisher_id, device_id, sightings, scrapes, feed_position_sum,
                                       feed_position_min, feed_position_max, first_seen_at, last_seen_at)
    SELECT date_trunc('hour', s.seen_at) AS hour, s.ad_id, s.publisher_id, s.device_id, count(*),
           count(DISTINCT s.scrape_id), COALESCE(sum(s.feed_position), 0), min(s.feed_position),
           max(s.feed_position), min(s.seen_at), max(s.seen_at)
    FROM tracks.sighting s
    WHERE s.seen_at >= date_trunc('hour', p_from)
      AND NOT EXISTS (SELECT 1 FROM tracks.hour_state h
                      WHERE h.hour = date_trunc('hour', s.seen_at) AND h.closed_at IS NOT NULL)
    GROUP BY 1, 2, 3, 4;
    GET DIAGNOSTICS n = ROW_COUNT;

    DELETE FROM tracks.publisher_hourly_open;
    INSERT INTO tracks.publisher_hourly_open (hour, publisher_id, device_id, scrapes, answered, failed, sightings)
    SELECT date_trunc('hour', c.at), c.publisher_id, c.device_id, count(*),
           count(*) FILTER (WHERE c.outcome IN ('ok', 'empty')),
           count(*) FILTER (WHERE c.outcome NOT IN ('ok', 'empty')),
           sum(c.ad_count)
    FROM tracks.scrape c
    WHERE c.at >= date_trunc('hour', p_from)
      AND NOT EXISTS (SELECT 1 FROM tracks.hour_state h
                      WHERE h.hour = date_trunc('hour', c.at) AND h.closed_at IS NOT NULL)
    GROUP BY 1, 2, 3;
    RETURN n;
END;
$$;


-- Scrapes per publisher, device and hour: the denominator of every rate.
-- Closed hours and the hours still open, each hour from exactly one side, as
-- in ad_hourly_v1. closed is false for the open hours, which change every 5
-- minutes; an open hour that just closed is shown from the closed side only.
CREATE VIEW tracks_api.scrape_coverage_v2 AS
SELECT hour, publisher_id, device_id, scrapes, answered, failed, sightings, TRUE AS closed
FROM tracks.publisher_hourly
UNION ALL
SELECT o.hour, o.publisher_id, o.device_id, o.scrapes, o.answered, o.failed, o.sightings, FALSE AS closed
FROM tracks.publisher_hourly_open o
WHERE NOT EXISTS (SELECT 1 FROM tracks.hour_state h WHERE h.hour = o.hour AND h.closed_at IS NOT NULL);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tracks_api_read') THEN
        GRANT SELECT ON tracks_api.scrape_coverage_v2 TO tracks_api_read;
    END IF;
END;
$$;
