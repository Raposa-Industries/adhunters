-- Starts a brief and asks for its first round of options: Create's worker
-- reads the references, writes the headlines and picture ideas, and makes
-- the pictures. p_input: {"name", "vertical_id", "vertical_name", "ages",
-- "images" (0-12, default 6), "headlines" (0-30, default 10), "angles":
-- [text], "own_headlines": [text], "extra", "references": [{"kind":
-- "spy_ad" | "library_creative", "id"}]}. When p_origin was used before, that
-- brief's id comes back and nothing new is asked.
CREATE FUNCTION create_api.new_brief_v1(p_input JSONB, p_requested_by TEXT, p_origin TEXT)
RETURNS BIGINT
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_id BIGINT;
    v_images INTEGER := COALESCE((p_input ->> 'images')::INTEGER, 6);
    v_headlines INTEGER := COALESCE((p_input ->> 'headlines')::INTEGER, 10);
    v_ref JSONB;
BEGIN
    IF COALESCE(p_origin, '') <> '' THEN
        PERFORM pg_advisory_xact_lock(hashtext('create brief ' || p_origin));
        SELECT id INTO v_id FROM create_app.brief WHERE origin_key = p_origin;
        IF FOUND THEN
            RETURN v_id;
        END IF;
    END IF;
    IF v_images NOT BETWEEN 0 AND 12 OR v_headlines NOT BETWEEN 0 AND 30 OR v_images + v_headlines = 0 THEN
        RAISE EXCEPTION 'images must be 0 to 12 and headlines 0 to 30, not both 0';
    END IF;
    INSERT INTO create_app.brief (name, vertical_id, vertical_name, ages, images, headlines, angles, own_headlines,
                                  extra, rounds, state, requested_by, origin, origin_key)
    VALUES (COALESCE(p_input ->> 'name', ''), COALESCE(p_input ->> 'vertical_id', ''),
            COALESCE(p_input ->> 'vertical_name', ''), COALESCE(p_input ->> 'ages', ''), v_images, v_headlines,
            ARRAY(SELECT jsonb_array_elements_text(COALESCE(p_input -> 'angles', '[]'))),
            ARRAY(SELECT jsonb_array_elements_text(COALESCE(p_input -> 'own_headlines', '[]'))),
            COALESCE(p_input ->> 'extra', ''), 1, 'making', COALESCE(p_requested_by, ''),
            COALESCE(NULLIF(split_part(COALESCE(p_origin, ''), ':', 1), ''), 'api'), NULLIF(p_origin, ''))
    RETURNING id INTO v_id;
    FOR v_ref IN SELECT * FROM jsonb_array_elements(COALESCE(p_input -> 'references', '[]')) LOOP
        IF v_ref ->> 'kind' NOT IN ('spy_ad', 'library_creative') OR COALESCE(v_ref ->> 'id', '') = '' THEN
            RAISE EXCEPTION 'a reference is {"kind": "spy_ad" or "library_creative", "id"}, not %', v_ref;
        END IF;
        INSERT INTO create_app.reference (brief_id, kind, ref_id) VALUES (v_id, v_ref ->> 'kind', v_ref ->> 'id');
    END LOOP;
    INSERT INTO create_app.job (brief_id, kind, input)
    VALUES (v_id, 'plan', jsonb_build_object('round', 1, 'images', v_images, 'headlines', v_headlines));
    INSERT INTO create_app.event (brief_id, line) VALUES (v_id, 'Pedido recebido de ' || COALESCE(NULLIF(p_requested_by, ''), 'alguém'));
    RETURN v_id;
END;
$$;
