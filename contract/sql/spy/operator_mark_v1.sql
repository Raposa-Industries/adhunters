-- Published: what people marked on each operator.
CREATE VIEW spy_api.operator_mark_v1 AS
SELECT operator_id, hidden, watched, nickname, watched_since, made_by, updated_at FROM spy.operator_mark;
