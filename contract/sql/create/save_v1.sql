-- A save of chosen options into the library, as one set. state is waiting,
-- saving, done or failed; library_set_id is the set (Launch opens it with
-- /launch/new?set=<id>) once it exists.
CREATE VIEW create_api.save_v1 AS
SELECT id, brief_id, name, option_ids, ai_label, state, error, library_set_id, requested_by, origin_key,
       created_at, finished_at
FROM create_app.save;
