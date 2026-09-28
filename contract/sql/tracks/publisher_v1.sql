CREATE VIEW tracks_api.publisher_v1 AS
SELECT id, network_id, name, domain, first_seen_at, last_seen_at FROM tracks.publisher;
