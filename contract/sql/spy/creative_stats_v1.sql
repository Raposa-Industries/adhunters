-- One row per creative, rebuilt every 5 minutes: facts only (counts, who
-- and what, first and last seen). Windows are UTC days including today.
-- Anything per check or against usual comes from creative_range_v1.
CREATE VIEW spy_api.creative_stats_v1 AS
SELECT creative_id, sightings_total, sightings_today, sightings_yesterday, sightings_3d, sightings_prev_3d,
       sightings_7d, sightings_prev_7d, sightings_30d, scrapes_total, active_days, ads_count, publishers_count,
       publisher_ids, network_share_7d_pct, is_new, running, top_ad_id, headline, brand_id, operator_id,
       account_ids, trackers, affiliate_networks, vertical, classified_vertical, subvertical, vertical_confidence,
       unsure, vertical_source, health_from_funnel, is_junk, first_seen_at, last_seen_at, refreshed_at
FROM spy.creative_stats;
