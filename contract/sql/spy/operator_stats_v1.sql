CREATE VIEW spy_api.operator_stats_v1 AS
SELECT operator_id, sightings_total, sightings_today, sightings_7d, sightings_prev_7d, sightings_30d,
       share_today_pct, share_7d_pct, share_30d_pct, momentum_pct, creatives_count, live_creatives_count,
       publisher_ids, phone_share_pct, vertical, brands, first_seen_at, last_seen_at, refreshed_at
FROM spy.operator_stats;
