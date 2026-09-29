-- Which operator each Tracks account belongs to. No row: not grouped yet.
CREATE VIEW spy_api.account_operator_v1 AS
SELECT account_id, operator_id FROM spy.account_operator;
