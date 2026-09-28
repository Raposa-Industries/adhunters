CREATE VIEW tracks_api.campaign_daily_v1 AS
SELECT day, campaign_id, publisher_id, sightings, scrapes, first_seen_at, last_seen_at
FROM tracks.campaign_daily;
