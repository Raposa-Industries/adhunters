-- Published: each creative's category and vertical (ids of verticals.yaml),
-- how sure, and from what.
CREATE VIEW spy_api.creative_class_v1 AS
SELECT creative_id, category_id, vertical_id, confidence, unsure, source, classified_at
FROM spy.creative_class;
