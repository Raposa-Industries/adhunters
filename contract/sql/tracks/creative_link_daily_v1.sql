CREATE VIEW tracks_api.creative_link_daily_v1 AS
SELECT day, creative_id, link_id, sightings, first_seen_at, last_seen_at FROM tracks.creative_link_daily;
