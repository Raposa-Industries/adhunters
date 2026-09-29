-- The vertical of each Tracks creative. No row: not classified yet.
CREATE VIEW spy_api.creative_vertical_v1 AS
SELECT creative_id, vertical, subvertical, shown_vertical, confidence, unsure, source, health_from_funnel
FROM spy.creative_vertical;
