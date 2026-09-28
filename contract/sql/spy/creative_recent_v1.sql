CREATE VIEW spy_api.creative_recent_v1 AS
SELECT creative_id, sightings_24h, sightings_prev_24h, rate_24h, rate_prev_24h, peak_end, peak_sightings, peak_rate
FROM spy.creative_recent;
