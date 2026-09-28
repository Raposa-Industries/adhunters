CREATE VIEW tracks_api.account_v1 AS
SELECT id, network_id, external_id, org_external_id, first_seen_at, last_seen_at FROM tracks.account;
