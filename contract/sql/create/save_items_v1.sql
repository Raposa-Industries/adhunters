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
