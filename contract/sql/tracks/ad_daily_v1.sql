CREATE VIEW tracks_api.ad_daily_v1 AS
SELECT day, ad_id, publisher_id, device_id, creative_id, sightings, scrapes, feed_position_sum,
       first_seen_at, last_seen_at
FROM tracks.ad_daily;
