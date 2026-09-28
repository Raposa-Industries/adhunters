CREATE VIEW spy_api.operator_recent_v1 AS
SELECT operator_id, sightings_24h, sightings_prev_24h, rate_24h, rate_prev_24h FROM spy.operator_recent;
