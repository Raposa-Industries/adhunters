-- Closed hours and the hours still open, each hour from exactly one side.
-- closed is false for the open hours, which change every 5 minutes.
CREATE VIEW tracks_api.ad_hourly_v1 AS
SELECT hour, ad_id, publisher_id, device_id, sightings, scrapes, feed_position_sum, feed_position_min,
       feed_position_max, first_seen_at, last_seen_at, TRUE AS closed
FROM tracks.ad_hourly
UNION ALL
SELECT hour, ad_id, publisher_id, device_id, sightings, scrapes, feed_position_sum, feed_position_min,
       feed_position_max, first_seen_at, last_seen_at, FALSE AS closed
FROM tracks.ad_hourly_open;
