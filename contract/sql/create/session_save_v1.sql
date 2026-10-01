-- One save of a session's items into the library, in the session's set.
-- state is waiting, saving, done or failed.
CREATE VIEW create_api.session_save_v1 AS
SELECT s.id, s.session_id, s.item_ids, s.ai_label, s.state, s.error, x.library_set_id, s.made_by, s.origin_key,
       s.created_at, s.finished_at
FROM create_app.session_save s JOIN create_app.session x ON x.id = s.session_id;
