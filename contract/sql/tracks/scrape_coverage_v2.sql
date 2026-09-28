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
