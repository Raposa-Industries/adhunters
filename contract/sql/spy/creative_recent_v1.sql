-- creative_range_v1 for the last 24 closed hours, kept ready.
CREATE VIEW spy_api.creative_recent_v1 AS
SELECT creative_id, network_id, sightings, sightings_usual, checks, checks_usual, presence, presence_usual,
       phone_presence, desktop_presence, share_pct, share_usual_pct, share_gain_pts, rank, momentum, momentum_low,
       momentum_high, momentum_word, momentum_sure, momentum_rank, noise, usual_periods, publishers, first_seen_at, vertical,
       vertical_share_pct, vertical_rank, scaled, last_seen_at, running, lifespan_days, lifespan_pct,
       lifespan_curve, operator_id, is_junk
FROM spy.creative_recent;
