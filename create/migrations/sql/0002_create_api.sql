-- The published face of Create, version 1. Each statement is the matching
-- file in contract/sql/create/, word for word (a test checks). Desk and any
-- other app read these views and call these functions, and nothing else of
-- Create; Create's own pages use the same rows.

CREATE SCHEMA create_api;

-- One brief: what to make, for one vertical. state is draft (being filled
-- in on the page), reading (the performing ads are being read), making
-- (options are being made), ready (options to choose from, nothing
-- running) or failed (nothing could be made; error says why, in pt-BR).
CREATE VIEW create_api.brief_v1 AS
SELECT id, name, vertical_id, vertical_name, ages, images, headlines, angles, extra, state, error,
       rounds, cost_usd, requested_by, origin, origin_key, created_at, updated_at
FROM create_app.brief;

-- One option made for a brief: a picture (kind image) or a headline (kind
-- headline, text). A picture is waiting, making, done or failed; image_url
-- is its path on Create's address once done. chosen and starred are the
-- person's marks on the page.
CREATE VIEW create_api.option_v1 AS
SELECT id, brief_id, round, kind, angle, text,
       CASE WHEN kind = 'image' AND state = 'done' THEN '/create/files/options/' || id END AS image_url,
       width, height, state, error, parent_id, note, chosen, starred, created_at
FROM create_app.option;

-- A save of chosen options into the library, as one set. state is waiting,
-- saving, done or failed; library_set_id is the set (Launch opens it with
-- /launch/new?set=<id>) once it exists.
CREATE VIEW create_api.save_v1 AS
SELECT id, brief_id, name, option_ids, ai_label, state, error, library_set_id, requested_by, origin_key,
       created_at, finished_at
FROM create_app.save;

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

REVOKE EXECUTE ON FUNCTION create_api.new_brief_v1(JSONB, TEXT, TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION create_api.save_set_v1(BIGINT, BIGINT[], TEXT, TEXT, TEXT, TEXT) FROM PUBLIC;

-- create_api_read is made by platform/servers/setup.sh; a test database
-- without it just skips the grants.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'create_api_read') THEN
        GRANT USAGE ON SCHEMA create_api TO create_api_read;
        GRANT SELECT ON ALL TABLES IN SCHEMA create_api TO create_api_read;
        GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA create_api TO create_api_read;
    END IF;
END
$$;
