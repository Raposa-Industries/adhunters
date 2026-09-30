-- One option made for a brief: a picture (kind image) or a headline (kind
-- headline, text). A picture is waiting, making, done or failed; image_url
-- is its path on Create's address once done. chosen and starred are the
-- person's marks on the page.
CREATE VIEW create_api.option_v1 AS
SELECT id, brief_id, round, kind, angle, text,
       CASE WHEN kind = 'image' AND state = 'done' THEN '/create/files/options/' || id END AS image_url,
       width, height, state, error, parent_id, note, chosen, starred, created_at
FROM create_app.option;
