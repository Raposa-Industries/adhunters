-- Published, for spy-web: marks an operator. Every argument is the new
-- state; with none left (not hidden, not watched, no nickname) the mark
-- goes. A nickname becomes the operator's name, kept through regrouping;
-- taking it away gives the name back to the grouping.
CREATE FUNCTION spy_api.mark_operator_v1(p_operator_id INTEGER, p_hidden BOOLEAN, p_watched BOOLEAN,
                                         p_nickname TEXT, p_made_by TEXT DEFAULT '')
RETURNS VOID
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    nick TEXT := NULLIF(btrim(p_nickname), '');
    had TEXT;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM spy.operator WHERE id = p_operator_id) THEN
        RAISE EXCEPTION 'no operator %', p_operator_id USING ERRCODE = 'no_data_found';
    END IF;
    SELECT nickname INTO had FROM spy.operator_mark WHERE operator_id = p_operator_id;
    INSERT INTO spy.operator_mark AS m (operator_id, hidden, watched, nickname, watched_since, scaled, accounts,
                                        made_by, updated_at)
    VALUES (p_operator_id, COALESCE(p_hidden, FALSE), COALESCE(p_watched, FALSE), nick,
            CASE WHEN p_watched THEN now() END,
            COALESCE((SELECT z.scaled FROM spy.size_stats z WHERE z.kind = 'operator' AND z.subject_id = p_operator_id), FALSE),
            ARRAY(SELECT ao.account_id FROM spy.account_operator ao WHERE ao.operator_id = p_operator_id ORDER BY 1),
            COALESCE(p_made_by, ''), now())
    ON CONFLICT (operator_id) DO UPDATE SET
        hidden = EXCLUDED.hidden,
        watched = EXCLUDED.watched,
        nickname = EXCLUDED.nickname,
        watched_since = CASE WHEN NOT EXCLUDED.watched THEN NULL
                             WHEN m.watched THEN m.watched_since ELSE now() END,
        scaled = CASE WHEN EXCLUDED.watched AND NOT m.watched THEN EXCLUDED.scaled ELSE m.scaled END,
        accounts = EXCLUDED.accounts,
        made_by = EXCLUDED.made_by,
        updated_at = now();
    IF nick IS NOT NULL THEN
        UPDATE spy.operator o SET display_name = nick, name_is_manual = TRUE,
            name = concat_ws(' · ', nick, o.seller, o.code), updated_at = now()
        WHERE o.id = p_operator_id;
    ELSIF had IS NOT NULL THEN
        UPDATE spy.operator o SET display_name = NULL, name_is_manual = FALSE,
            name = concat_ws(' · ', o.seller, o.code), updated_at = now()
        WHERE o.id = p_operator_id;
    END IF;
    DELETE FROM spy.operator_mark
    WHERE operator_id = p_operator_id AND NOT hidden AND NOT watched AND nickname IS NULL;
END;
$$;
