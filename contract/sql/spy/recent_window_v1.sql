-- Where the last 24 hours end (the first hour Tracks has not closed), for
-- creative_recent_v1 and operator_recent_v1.
CREATE VIEW spy_api.recent_window_v1 AS
SELECT window_end, network_sightings_24h, refreshed_at FROM spy.recent_window;
