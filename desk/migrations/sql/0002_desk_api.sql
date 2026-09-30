-- The published face of Desk, version 1. Each statement is the matching
-- file in contract/sql/desk/, word for word (a test checks). Other services
-- read these and call these, and nothing else of Desk.

CREATE SCHEMA desk_api;

-- The to-do list: one piece of work someone holds (a person's email, or
-- 'desk'), open while done_at is NULL.
CREATE VIEW desk_api.todo_v1 AS
SELECT id, title, holder, made_by, made_at, due, link, done_at, done_by
FROM desk.todo;

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

-- Readers get these through the desk_api_read role, which the box setup
-- creates and grants to each reading service's login.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'desk_api_read') THEN
        GRANT USAGE ON SCHEMA desk_api TO desk_api_read;
        GRANT SELECT ON ALL TABLES IN SCHEMA desk_api TO desk_api_read;
        GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA desk_api TO desk_api_read;
    END IF;
END;
$$;
REVOKE EXECUTE ON FUNCTION desk_api.add_todo_v1(TEXT, TEXT, TEXT, TEXT, DATE) FROM PUBLIC;
