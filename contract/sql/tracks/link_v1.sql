CREATE VIEW tracks_api.link_v1 AS
SELECT id, link_key, host, path, item_id, tracker, affiliate_network, params, sample_url,
       first_seen_at, last_seen_at
FROM tracks.link;
