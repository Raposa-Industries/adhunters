CREATE VIEW spy_api.publisher_stats_v1 AS
SELECT publisher_id, sightings_total, sightings_today, sightings_7d, sightings_30d, share_30d_pct, scrapes_30d,
       phone_sightings_30d, creatives_30d, operators_30d, operator_hhi, top_operators, top_creatives, verticals,
       first_seen_at, last_seen_at, refreshed_at
FROM spy.publisher_stats;
