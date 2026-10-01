-- One session of Create: a vertical and a name, which are also the library
-- folders what it saves goes in (<vertical>/<name>). library_set_id is that
-- set once something was saved (Launch opens it with /launch/new?set=<id>).
CREATE VIEW create_api.session_v1 AS
SELECT id, name, vertical_id, vertical_name, library_set_id, cost_usd, made_by, origin_key, created_at, updated_at
FROM create_app.session;
