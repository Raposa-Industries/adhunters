-- One turn of a session: the person's prompt, the items it started from
-- (picked) and how many pictures and headlines it asked for. state is
-- making, done (something came out) or failed (nothing did; error says why,
-- in pt-BR).
CREATE VIEW create_api.turn_v1 AS
SELECT id, session_id, prompt, picked, images, headlines, state, error, made_by, origin_key, created_at
FROM create_app.turn;
