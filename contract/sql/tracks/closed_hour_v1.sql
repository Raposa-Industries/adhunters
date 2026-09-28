-- Which hours are closed. A dirty hour got a late file and closes again soon.
CREATE VIEW tracks_api.closed_hour_v1 AS
SELECT hour, closed_at, dirty
FROM tracks.hour_state
WHERE closed_at IS NOT NULL;
