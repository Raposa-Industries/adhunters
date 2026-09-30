-- The presets people saved, for a new group or a new pair's campaigns.
-- account '' is for every account of the network.
CREATE VIEW launch_api.preset_v1 AS
SELECT id, level, network, account, name, fields, made_by, made_at, changed_by, changed_at
FROM launch.preset;
