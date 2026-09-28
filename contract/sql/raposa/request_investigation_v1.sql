-- Asks for an investigation of a creative (and of one of its ads, when
-- given): 'deep' or 'quick'. When the creative already has one waiting or
-- running, that one's id comes back and nothing new is queued.
CREATE FUNCTION raposa_api.request_investigation_v1(p_creative_id INTEGER, p_mode TEXT, p_ad_id INTEGER DEFAULT NULL, p_requested_by TEXT DEFAULT '')
RETURNS BIGINT
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_id BIGINT;
BEGIN
    IF p_mode NOT IN ('deep', 'quick') THEN
        RAISE EXCEPTION 'mode must be deep or quick, not %', p_mode;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('raposa request ' || p_creative_id));
    SELECT id INTO v_id FROM raposa.investigation
    WHERE creative_id = p_creative_id AND status IN ('waiting', 'running')
    ORDER BY id LIMIT 1;
    IF FOUND THEN
        RETURN v_id;
    END IF;
    INSERT INTO raposa.investigation (creative_id, ad_id, mode, origin, requested_by, visits_target)
    VALUES (p_creative_id, p_ad_id, p_mode, 'user', COALESCE(p_requested_by, ''),
            CASE WHEN p_mode = 'deep' THEN raposa.setting_int('visits_target', 100) ELSE 0 END)
    RETURNING id INTO v_id;
    RETURN v_id;
END;
$$;
