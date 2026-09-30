-- History: each thing Launch did on a network, who did it and who asked.
CREATE VIEW launch_api.change_v1 AS
SELECT id, at, who, asked_by, network, account, group_id, campaign_id, kind, summary, before, after, result, problems
FROM launch.change;
