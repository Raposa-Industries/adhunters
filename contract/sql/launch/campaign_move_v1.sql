-- A campaign moved to another group: Taboola cannot change a campaign's
-- group, so a move is a paused copy there with a new id. Only finished
-- moves (the original paused, or left as it was on purpose); moved_at is
-- when it finished. The ids are numbers (NULL for a network whose ids are
-- not).
CREATE VIEW launch_api.campaign_move_v1 AS
SELECT m.id, m.network, m.account,
       CASE WHEN m.from_campaign ~ '^[0-9]{1,18}$' THEN m.from_campaign::bigint END AS old_campaign_id,
       CASE WHEN m.to_campaign ~ '^[0-9]{1,18}$' THEN m.to_campaign::bigint END AS new_campaign_id,
       m.from_campaign, m.to_campaign, m.to_group, m.originals, m.done_at AS moved_at
FROM launch.move m
WHERE m.state = 'done';
