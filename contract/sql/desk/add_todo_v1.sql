-- Puts a to-do on someone's list (a person's email, or 'desk'), from another
-- app: Launch asking a person to start a pair in Taboola, say. Returns its id.
CREATE FUNCTION desk_api.add_todo_v1(p_title TEXT, p_holder TEXT, p_made_by TEXT, p_link TEXT DEFAULT '', p_due DATE DEFAULT NULL)
RETURNS BIGINT
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_id BIGINT;
BEGIN
    IF btrim(COALESCE(p_title, '')) = '' OR btrim(COALESCE(p_holder, '')) = '' OR btrim(COALESCE(p_made_by, '')) = '' THEN
        RAISE EXCEPTION 'a to-do has a title, a holder and who made it';
    END IF;
    INSERT INTO desk.todo (title, holder, made_by, link, due)
    VALUES (btrim(p_title), lower(btrim(p_holder)), lower(btrim(p_made_by)), COALESCE(p_link, ''), p_due)
    RETURNING id INTO v_id;
    RETURN v_id;
END;
$$;
