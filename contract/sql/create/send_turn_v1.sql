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
