CREATE VIEW tracks_api.campaign_v1 AS
SELECT id, network_id, external_id, name, account_id, parent_external_id, parent_name, objective,
       first_seen_at, last_seen_at
FROM tracks.campaign;
