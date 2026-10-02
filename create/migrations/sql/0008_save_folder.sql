-- Create's redesign (Draw Designer d587e1b829, 2 Oct 2026): Salvar puts
-- what a person picked into the library set (folder) they choose, with
-- tags, instead of always the session's own. Only adds; a save without a
-- set goes to the session's folder as before.
--
-- library_set_id is the set the person chose ('' and NULL: the session's),
-- set_name its name as they saw it, tags what the library puts on each
-- item saved.
ALTER TABLE create_app.session_save
    ADD COLUMN library_set_id BIGINT,
    ADD COLUMN set_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN tags TEXT[] NOT NULL DEFAULT '{}';
