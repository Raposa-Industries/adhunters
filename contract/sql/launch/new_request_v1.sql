-- Asks Launch for a change; a person confirms it in Launch before anything
-- is sent. p_kind is pause, pause_ads, change, duplicate or move; p_input
-- holds network, account, campaigns (ids) and, by kind, ads (ids), change
-- ({cpc, daily_cap, spending_limit, name}), to_group and originals
-- (when_started, now, leave). p_origin names the ask ("desk:42"): asking
-- again with the same origin returns the same request.
CREATE FUNCTION launch_api.new_request_v1(p_kind TEXT, p_input JSONB, p_requested_by TEXT, p_origin TEXT)
RETURNS BIGINT
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_id BIGINT;
BEGIN
    IF p_kind IS NULL OR p_kind NOT IN ('pause', 'pause_ads', 'change', 'duplicate', 'move') THEN
        RAISE EXCEPTION 'kind must be pause, pause_ads, change, duplicate or move, not %', p_kind;
    END IF;
    IF jsonb_typeof(p_input) IS DISTINCT FROM 'object'
       OR COALESCE(p_input->>'network', '') = '' OR COALESCE(p_input->>'account', '') = ''
       OR jsonb_typeof(p_input->'campaigns') IS DISTINCT FROM 'array' OR jsonb_array_length(p_input->'campaigns') = 0 THEN
        RAISE EXCEPTION 'input needs network, account and campaigns';
    END IF;
    IF COALESCE(p_origin, '') = '' THEN
        RAISE EXCEPTION 'origin is needed';
    END IF;
    INSERT INTO launch.request (kind, input, requested_by, origin)
    VALUES (p_kind, p_input, COALESCE(p_requested_by, ''), p_origin)
    ON CONFLICT (origin) DO NOTHING
    RETURNING id INTO v_id;
    IF v_id IS NULL THEN
        SELECT id INTO v_id FROM launch.request WHERE origin = p_origin;
    END IF;
    RETURN v_id;
END;
$$;
