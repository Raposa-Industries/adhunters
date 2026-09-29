-- Every change of direction, for alerts and watches.
CREATE VIEW spy_api.direction_event_v1 AS
SELECT id, at, kind, key, subject_id, from_direction, to_direction, reason, reason_text FROM spy.direction_event;
