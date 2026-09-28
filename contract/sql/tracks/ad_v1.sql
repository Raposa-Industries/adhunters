CREATE VIEW tracks_api.ad_v1 AS
SELECT id, creative_id, headline, description, cta, brand_id, account_id, first_seen_at, last_seen_at
FROM tracks.ad;
