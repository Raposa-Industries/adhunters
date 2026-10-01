-- The published face of Create's sessions (decision 0019). Each statement is
-- the matching file in contract/sql/create/, word for word (a test checks).
-- The brief views and functions of 0002 stay until Desk and every other
-- caller has moved off them.

-- One session of Create: a vertical and a name, which are also the library
-- folders what it saves goes in (<vertical>/<name>). library_set_id is that
-- set once something was saved (Launch opens it with /launch/new?set=<id>).
CREATE VIEW create_api.session_v1 AS
SELECT id, name, vertical_id, vertical_name, library_set_id, cost_usd, made_by, origin_key, created_at, updated_at
FROM create_app.session;

-- One turn of a session: the person's prompt, the items it started from
-- (picked) and how many pictures and headlines it asked for. state is
-- making, done (something came out) or failed (nothing did; error says why,
-- in pt-BR).
CREATE VIEW create_api.turn_v1 AS
SELECT id, session_id, prompt, picked, images, headlines, state, error, made_by, origin_key, created_at
FROM create_app.turn;

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

-- One save of a session's items into the library, in the session's set.
-- state is waiting, saving, done or failed.
CREATE VIEW create_api.session_save_v1 AS
SELECT s.id, s.session_id, s.item_ids, s.ai_label, s.state, s.error, x.library_set_id, s.made_by, s.origin_key,
       s.created_at, s.finished_at
FROM create_app.session_save s JOIN create_app.session x ON x.id = s.session_id;

-- Opens a session in a vertical (an id of shared/verticals/verticals.yaml
-- and its name). A name already used in that vertical opens that session
-- again; so does an origin used before.
CREATE FUNCTION create_api.new_session_v1(p_name TEXT, p_vertical_id TEXT, p_vertical_name TEXT,
                                          p_requested_by TEXT, p_origin TEXT)
RETURNS BIGINT
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_id BIGINT;
BEGIN
    IF COALESCE(p_origin, '') <> '' THEN
        SELECT id INTO v_id FROM create_app.session WHERE origin_key = p_origin;
        IF FOUND THEN
            RETURN v_id;
        END IF;
    END IF;
    IF btrim(COALESCE(p_name, '')) = '' OR btrim(COALESCE(p_vertical_name, '')) = '' THEN
        RAISE EXCEPTION 'a session needs a name and a vertical';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('create session ' || p_vertical_id || ' ' || lower(btrim(p_name))));
    SELECT id INTO v_id FROM create_app.session WHERE vertical_id = p_vertical_id AND lower(name) = lower(btrim(p_name));
    IF FOUND THEN
        RETURN v_id;
    END IF;
    INSERT INTO create_app.session (name, vertical_id, vertical_name, made_by, origin_key)
    VALUES (btrim(p_name), p_vertical_id, btrim(p_vertical_name), COALESCE(p_requested_by, ''), NULLIF(p_origin, ''))
    RETURNING id INTO v_id;
    RETURN v_id;
END;
$$;

-- Sends a turn in a session: Create makes p_images pictures (0 to 8) and
-- p_headlines headlines (0 to 20, always in English) from the prompt and the
-- picked items, which must be the session's and done. Picked pictures go to
-- the picture model to be changed as the prompt says; picked headlines are
-- varied. An origin used before returns that turn and asks nothing new.
CREATE FUNCTION create_api.send_turn_v1(p_session_id BIGINT, p_prompt TEXT, p_picked BIGINT[], p_images INTEGER,
                                        p_headlines INTEGER, p_requested_by TEXT, p_origin TEXT)
RETURNS BIGINT
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_id BIGINT;
    v_picked BIGINT[] := COALESCE(p_picked, '{}');
BEGIN
    IF COALESCE(p_origin, '') <> '' THEN
        PERFORM pg_advisory_xact_lock(hashtext('create turn ' || p_origin));
        SELECT id INTO v_id FROM create_app.turn WHERE origin_key = p_origin;
        IF FOUND THEN
            RETURN v_id;
        END IF;
    END IF;
    PERFORM 1 FROM create_app.session WHERE id = p_session_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'no session %', p_session_id;
    END IF;
    IF COALESCE(p_images, 0) NOT BETWEEN 0 AND 8 OR COALESCE(p_headlines, 0) NOT BETWEEN 0 AND 20
       OR COALESCE(p_images, 0) + COALESCE(p_headlines, 0) = 0 THEN
        RAISE EXCEPTION 'images must be 0 to 8 and headlines 0 to 20, not both 0';
    END IF;
    IF btrim(COALESCE(p_prompt, '')) = '' AND cardinality(v_picked) = 0 THEN
        RAISE EXCEPTION 'a turn needs a prompt or picked items';
    END IF;
    IF (SELECT count(*) FROM create_app.item WHERE id = ANY (v_picked) AND session_id = p_session_id AND state = 'done')
       <> (SELECT count(DISTINCT x) FROM unnest(v_picked) x) THEN
        RAISE EXCEPTION 'every picked item must be done and belong to session %', p_session_id;
    END IF;
    INSERT INTO create_app.turn (session_id, prompt, picked, images, headlines, made_by, origin_key)
    VALUES (p_session_id, btrim(COALESCE(p_prompt, '')), v_picked, COALESCE(p_images, 0), COALESCE(p_headlines, 0),
            COALESCE(p_requested_by, ''), NULLIF(p_origin, ''))
    RETURNING id INTO v_id;
    INSERT INTO create_app.work (session_id, kind, turn_id) VALUES (p_session_id, 'turn', v_id);
    UPDATE create_app.session SET updated_at = now() WHERE id = p_session_id;
    RETURN v_id;
END;
$$;

-- Saves items of a session into the library, in the session's own set
-- (made by the first save, in <vertical>/<session name>). Every item must be
-- the session's and done. p_ai_label is the person's answer for the
-- pictures: ai, not_ai or unset. An origin used before returns that save.
CREATE FUNCTION create_api.save_items_v1(p_session_id BIGINT, p_item_ids BIGINT[], p_ai_label TEXT,
                                         p_requested_by TEXT, p_origin TEXT)
RETURNS BIGINT
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_id BIGINT;
BEGIN
    IF COALESCE(p_origin, '') <> '' THEN
        PERFORM pg_advisory_xact_lock(hashtext('create session save ' || p_origin));
        SELECT id INTO v_id FROM create_app.session_save WHERE origin_key = p_origin;
        IF FOUND THEN
            RETURN v_id;
        END IF;
    END IF;
    IF cardinality(COALESCE(p_item_ids, '{}')) = 0 THEN
        RAISE EXCEPTION 'pick at least one item to save';
    END IF;
    IF (SELECT count(*) FROM create_app.item WHERE id = ANY (p_item_ids) AND session_id = p_session_id AND state = 'done')
       <> (SELECT count(DISTINCT x) FROM unnest(p_item_ids) x) THEN
        RAISE EXCEPTION 'every item must be done and belong to session %', p_session_id;
    END IF;
    INSERT INTO create_app.session_save (session_id, item_ids, ai_label, made_by, origin_key)
    VALUES (p_session_id, p_item_ids, COALESCE(NULLIF(p_ai_label, ''), 'unset'), COALESCE(p_requested_by, ''), NULLIF(p_origin, ''))
    RETURNING id INTO v_id;
    INSERT INTO create_app.work (session_id, kind, save_id) VALUES (p_session_id, 'save', v_id);
    RETURN v_id;
END;
$$;

REVOKE EXECUTE ON FUNCTION create_api.new_session_v1(TEXT, TEXT, TEXT, TEXT, TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION create_api.send_turn_v1(BIGINT, TEXT, BIGINT[], INTEGER, INTEGER, TEXT, TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION create_api.save_items_v1(BIGINT, BIGINT[], TEXT, TEXT, TEXT) FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'create_api_read') THEN
        GRANT SELECT ON ALL TABLES IN SCHEMA create_api TO create_api_read;
        GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA create_api TO create_api_read;
    END IF;
END
$$;
