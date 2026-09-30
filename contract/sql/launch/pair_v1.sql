-- The desktop and mobile campaigns Launch made together.
CREATE VIEW launch_api.pair_v1 AS
SELECT id, network, account, group_id, name, desktop_id, mobile_id, preset_id, made_by, made_at
FROM launch.pair;
