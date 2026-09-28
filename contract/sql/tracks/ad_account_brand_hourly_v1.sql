CREATE VIEW tracks_api.ad_account_brand_hourly_v1 AS
SELECT hour, ad_id, account_id, brand_id, publisher_id, device_id, sightings
FROM tracks.ad_account_brand_hourly;
