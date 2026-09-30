-- Hours whose counts are current. An hour closes an hour after it ends and
-- closes again when a late event reaches one of its journeys.
CREATE VIEW funnels_api.closed_hour_v1 AS
SELECT hour, closed_at, journeys FROM funnels.hour_state WHERE closed_at IS NOT NULL AND dirty_since IS NULL;
