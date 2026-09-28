-- Direction now, per ad, creative, operator and vertical (kind, key;
-- subject_id is the id for the first three). Every 5 minutes.
CREATE VIEW spy_api.direction_v1 AS
SELECT kind, key, subject_id, direction, direction_since, judged, window_from, now_sightings, now_scrapes,
       usual_per_scrape, now_per_scrape, ratio, expected, z, share_now_pct, share_usual_pct, share_ratio,
       market_ratio, rest_ratio, last_seen_hour, stop_expected, stop_scrapes, reason, reason_text, refreshed_at
FROM spy.direction_stats;
