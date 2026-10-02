-- Published: watched operators' moves, and whether Pushcut delivered them.
CREATE VIEW spy_api.watch_notice_v1 AS
SELECT id, operator_id, reason, at, title, body, status, error, sent_at FROM spy.watch_notice;
