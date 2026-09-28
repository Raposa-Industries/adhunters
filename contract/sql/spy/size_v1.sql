CREATE VIEW spy_api.size_v1 AS
SELECT kind, key, subject_id, market_kind, market_key, sightings_24h, share_24h_pct, rank_24h, sightings_7d,
       share_7d_pct, rank_7d, share_7d_before_pct, scaled, refreshed_at
FROM spy.size_stats;
