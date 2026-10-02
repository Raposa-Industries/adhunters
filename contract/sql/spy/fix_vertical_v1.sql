-- Published, for spy-web: sets a creative's vertical by hand, or with a NULL
-- vertical gives it back to the classifier. The caller checks the ids are in
-- shared/verticals/verticals.yaml.
CREATE FUNCTION spy_api.fix_vertical_v1(p_creative_id INTEGER, p_category_id TEXT, p_vertical_id TEXT,
                                        p_made_by TEXT DEFAULT '')
RETURNS VOID
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF p_vertical_id IS NULL THEN
        DELETE FROM spy.vertical_fix WHERE creative_id = p_creative_id;
        UPDATE spy.creative_class SET category_id = rules_category_id, vertical_id = rules_vertical_id,
            confidence = rules_confidence, source = rules_source, model_at = NULL
        WHERE creative_id = p_creative_id AND source = 'hand';
        RETURN;
    END IF;
    IF p_category_id IS NULL THEN
        RAISE EXCEPTION 'a vertical needs its category';
    END IF;
    INSERT INTO spy.vertical_fix AS f (creative_id, category_id, vertical_id, made_by, made_at)
    VALUES (p_creative_id, p_category_id, p_vertical_id, COALESCE(p_made_by, ''), now())
    ON CONFLICT (creative_id) DO UPDATE SET category_id = EXCLUDED.category_id, vertical_id = EXCLUDED.vertical_id,
        made_by = EXCLUDED.made_by, made_at = EXCLUDED.made_at;
    -- A creative the rules have not read yet gets a row they will fill in.
    INSERT INTO spy.creative_class AS k (creative_id, category_id, vertical_id, confidence, source, rules_hash,
                                         input_ad_id, classified_at, needs_model)
    VALUES (p_creative_id, p_category_id, p_vertical_id, 1, 'hand', '', 0, now(), FALSE)
    ON CONFLICT (creative_id) DO UPDATE SET category_id = EXCLUDED.category_id, vertical_id = EXCLUDED.vertical_id,
        confidence = 1, source = 'hand';
END;
$$;
