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
