-- One brief: what to make, for one vertical. state is draft (being filled
-- in on the page), reading (the performing ads are being read), making
-- (options are being made), ready (options to choose from, nothing
-- running) or failed (nothing could be made; error says why, in pt-BR).
CREATE VIEW create_api.brief_v1 AS
SELECT id, name, vertical_id, vertical_name, ages, images, headlines, angles, extra, state, error,
       rounds, cost_usd, requested_by, origin, origin_key, created_at, updated_at
FROM create_app.brief;
