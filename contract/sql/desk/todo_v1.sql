-- The to-do list: one piece of work someone holds (a person's email, or
-- 'desk'), open while done_at is NULL.
CREATE VIEW desk_api.todo_v1 AS
SELECT id, title, holder, made_by, made_at, due, link, done_at, done_by
FROM desk.todo;
