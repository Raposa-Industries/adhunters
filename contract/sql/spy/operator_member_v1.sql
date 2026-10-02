-- Published: why each site and account is in its operator, as the last
-- grouping found it: the first rule that tied it in, or the hand fix.
CREATE VIEW spy_api.operator_member_v1 AS
SELECT m.member, m.member_id, m.operator_id, m.reason, m.agency, s.domain
FROM spy.grouping_member m
LEFT JOIN spy.site s ON m.member = 'site' AND s.id = m.member_id;
