-- Saves chosen options of a brief into the library as one set named
-- p_name. Every option must be the brief's and done. p_ai_label is the
-- person's answer for the pictures: 'ai', 'not_ai' or 'unset'. Returns the
-- save's id; its library_set_id appears on save_v1 once Create's worker has
-- written it. When p_origin was used before, that save's id comes back.
CREATE FUNCTION create_api.save_set_v1(p_brief_id BIGINT, p_option_ids BIGINT[], p_name TEXT, p_requested_by TEXT,
                                       p_origin TEXT, p_ai_label TEXT DEFAULT 'ai')
RETURNS BIGINT
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_id BIGINT;
BEGIN
    IF COALESCE(p_origin, '') <> '' THEN
        PERFORM pg_advisory_xact_lock(hashtext('create save ' || p_origin));
        SELECT id INTO v_id FROM create_app.save WHERE origin_key = p_origin;
        IF FOUND THEN
            RETURN v_id;
        END IF;
    END IF;
    IF COALESCE(btrim(p_name), '') = '' THEN
        RAISE EXCEPTION 'a save needs a name';
    END IF;
    IF p_ai_label NOT IN ('unset', 'ai', 'not_ai') THEN
        RAISE EXCEPTION 'ai_label must be unset, ai or not_ai, not %', p_ai_label;
    END IF;
    IF COALESCE(cardinality(p_option_ids), 0) = 0 THEN
        RAISE EXCEPTION 'choose at least one option';
    END IF;
    IF (SELECT count(*) FROM create_app.option WHERE id = ANY (p_option_ids) AND brief_id = p_brief_id AND state = 'done')
       <> (SELECT count(DISTINCT x) FROM unnest(p_option_ids) x) THEN
        RAISE EXCEPTION 'every option must be done and belong to brief %', p_brief_id;
    END IF;
    INSERT INTO create_app.save (brief_id, name, option_ids, ai_label, requested_by, origin_key)
    VALUES (p_brief_id, btrim(p_name), p_option_ids, p_ai_label, COALESCE(p_requested_by, ''), NULLIF(p_origin, ''))
    RETURNING id INTO v_id;
    INSERT INTO create_app.job (brief_id, kind, save_id) VALUES (p_brief_id, 'save', v_id);
    RETURN v_id;
END;
$$;
