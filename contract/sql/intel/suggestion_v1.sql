-- Intel's suggestions. launch_url opens Launch with the change filled in;
-- nothing changes until a person confirms there. Launch and Desk read the
-- open ones; state says what became of the others.
CREATE VIEW intel_api.suggestion_v1 AS
SELECT id, kind, account, group_id, campaign_id, item_ids, values, title, why, numbers, launch_url, state,
       created_at, seen_at, answered_at
FROM intel.suggestion;
