-- Live links not handed out yet, seen in the last 15 minutes.
CREATE VIEW tracks_api.live_link_v1 AS
SELECT id, host, url, network, campaign_external_id, device, publisher, page_url, ad_id, creative_id, seen_at
FROM tracks.live_link
WHERE taken_at IS NULL AND seen_at > now() - interval '15 minutes';
