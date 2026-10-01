-- One picture (kind image) or headline (kind headline, text) of a session.
-- origin is made (by turn_id, from the items in from_ids), upload, library
-- or typed. A made picture is waiting, making, done or failed; image_url is
-- its path on Create's address once done. library_id is the library's
-- creative or headline once it was saved.
CREATE VIEW create_api.item_v1 AS
SELECT id, session_id, turn_id, kind, origin, from_ids, text, angle,
       CASE WHEN kind = 'image' AND state = 'done' THEN '/create/files/items/' || id END AS image_url,
       width, height, state, error, library_id, created_at
FROM create_app.item;
