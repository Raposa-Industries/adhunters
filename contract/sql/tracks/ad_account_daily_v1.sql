CREATE VIEW tracks_api.ad_account_daily_v1 AS
SELECT day, ad_id, account_id, brand_id, publisher_id, device_id, creative_id, sightings,
       first_seen_at, last_seen_at
FROM tracks.ad_account_daily;
