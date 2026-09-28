CREATE VIEW tracks_api.placement_daily_v1 AS
SELECT day, ad_id, publisher_id, placement_id, sightings FROM tracks.placement_daily;
