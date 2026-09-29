-- operator_range_v1 for the last 24 closed hours, kept ready.
CREATE VIEW spy_api.operator_recent_v1 AS
SELECT operator_id, network_id, sightings, sightings_usual, checks, checks_usual, presence, presence_usual,
       phone_presence, desktop_presence, share_pct, share_usual_pct, share_gain_pts, rank, momentum, momentum_low,
       momentum_high, momentum_word, momentum_sure, momentum_rank,
       fall_on_one_publisher, noise, usual_periods, publishers, first_seen_at, launches, hits,
       misses, testing, hit_rate_pct, hit_rate_low_pct, hit_rate_high_pct
FROM spy.operator_recent;
