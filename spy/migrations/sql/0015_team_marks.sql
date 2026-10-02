-- lint: new-table
-- What people mark in Spy's pages (the team's WhatsApp asks of 2 Oct 2026):
--   operator_mark   an operator hidden from the lists, watched, or given a
--                   nickname. A mark is carried to the operator that takes
--                   most of its accounts when the grouping merges its own away.
--   watch_notice    a watched operator turned rising or scaled. spy-numbers
--                   sends each one through Pushcut (decision 0004); the
--                   operator page lists them either way.
--   vertical_fix    a person's vertical for a creative (a hand fix). It wins
--                   over the classifier's answer and teaches its model.
-- Accounts whose names differ only in their numbers ("memo-nb-1-sc",
-- "memo-nb-2-sc") now share a name root, so the grouping joins them, unless
-- the root is an agency's.
--
-- spy-web changes these only through spy_api.mark_operator_v1 and
-- spy_api.fix_vertical_v1, the first spy_api functions that write, granted
-- to the spy_web login alone.

-- Name root of an account. The first dash-separated part, digits at the end
-- removed, as before. When that is short or generic, the whole name without
-- "taboolaaccount-", "-sc", parts that are only digits and the digits ending
-- each part ("memo-nb-1-sc" is "memo-nb"), if 4 letters or more are left and
-- the name holds no email (the email rule joins those). Otherwise the whole
-- account, which groups nothing.
CREATE OR REPLACE FUNCTION spy.account_root(p_external_id TEXT)
RETURNS TEXT
LANGUAGE sql IMMUTABLE
AS $$
    SELECT CASE
        WHEN length(r) >= 5 AND r NOT IN ('taboolaaccount', 'taboola', 'account', 'media', 'digital',
                                          'global', 'native', 'group', 'agency', 'ads', 'adv')
            THEN r
        WHEN length(replace(s, '-', '')) >= 4 AND s NOT IN ('taboolaaccount', 'taboola', 'account', 'media', 'digital',
                                                            'global', 'native', 'group', 'agency', 'ads', 'adv')
             AND spy.account_email(p_external_id) IS NULL
            THEN s
        ELSE lower(p_external_id)
    END
    FROM (SELECT regexp_replace(split_part(lower(p_external_id), '-', 1), '\d+$', '') AS r,
                 array_to_string(ARRAY(
                     SELECT regexp_replace(u.part, '\d+$', '')
                     FROM unnest(string_to_array(
                              regexp_replace(regexp_replace(lower(p_external_id), '^taboolaaccount-', ''), '-sc$', ''),
                              '-')) WITH ORDINALITY AS u(part, i)
                     WHERE regexp_replace(u.part, '\d+$', '') <> ''
                     ORDER BY u.i), '-') AS s) x
$$;

-- An operator a person marked. No foreign key: when the grouping merges an
-- operator away, its mark waits for spy.carry_operator_marks.
CREATE TABLE spy.operator_mark (
    operator_id INTEGER PRIMARY KEY,
    hidden BOOLEAN NOT NULL DEFAULT FALSE,     -- left out of the ads and operators lists
    watched BOOLEAN NOT NULL DEFAULT FALSE,    -- a notice when it turns rising or scales
    nickname TEXT CHECK (nickname IS NULL OR length(nickname) BETWEEN 1 AND 60),  -- its name everywhere
    watched_since TIMESTAMPTZ,
    scaled BOOLEAN NOT NULL DEFAULT FALSE,     -- Size's answer when last looked, so scaling is noticed once
    accounts INTEGER[] NOT NULL DEFAULT '{}',  -- its accounts when last seen, to carry the mark
    made_by TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A watched operator's move, sent through Pushcut by spy-numbers.
CREATE TABLE spy.watch_notice (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    operator_id INTEGER NOT NULL,
    reason TEXT NOT NULL CHECK (reason IN ('rising', 'scaled')),
    event_id BIGINT,                           -- the direction_event, for rising
    at TIMESTAMPTZ NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
    attempts INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    next_try_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at TIMESTAMPTZ,
    UNIQUE (operator_id, event_id)
);
CREATE INDEX watch_notice_pending_idx ON spy.watch_notice (next_try_at) WHERE status = 'pending';
CREATE INDEX watch_notice_operator_idx ON spy.watch_notice (operator_id, at DESC);

-- A creative's vertical, set by a person. creative_class carries it with
-- source 'hand'; the classifier leaves such a creative alone.
CREATE TABLE spy.vertical_fix (
    creative_id INTEGER PRIMARY KEY,
    category_id TEXT NOT NULL,
    vertical_id TEXT NOT NULL,
    made_by TEXT NOT NULL DEFAULT '',
    made_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Watched operators that turned rising (Direction) or scaled (Size) since
-- they were watched get a notice. Returns how many were added.
CREATE FUNCTION spy.watch_operators(p_now TIMESTAMPTZ DEFAULT now()) RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    n BIGINT;
    k BIGINT;
BEGIN
    INSERT INTO spy.watch_notice (operator_id, reason, event_id, at, title, body)
    SELECT m.operator_id, 'rising', e.id, e.at,
           left(COALESCE(m.nickname, o.display_name, o.name) || ' está subindo', 120),
           left(e.reason_text, 500)
    FROM spy.operator_mark m
    JOIN spy.operator o ON o.id = m.operator_id
    JOIN spy.direction_event e ON e.kind = 'operator' AND e.subject_id = m.operator_id
    WHERE m.watched AND e.to_direction = 'rising' AND e.at >= m.watched_since
      AND e.at > p_now - interval '2 days'
    ON CONFLICT (operator_id, event_id) DO NOTHING;
    GET DIAGNOSTICS n = ROW_COUNT;

    INSERT INTO spy.watch_notice (operator_id, reason, at, title, body)
    SELECT m.operator_id, 'scaled', p_now,
           left(COALESCE(m.nickname, o.display_name, o.name) || ' escalou', 120),
           format('Está entre os maiores da vertical nos últimos 7 dias: %s vistas, %s%% do total, posição %s.',
                  z.sightings_7d, round(z.share_7d_pct, 1), z.rank_7d)
    FROM spy.operator_mark m
    JOIN spy.operator o ON o.id = m.operator_id
    JOIN spy.size_stats z ON z.kind = 'operator' AND z.subject_id = m.operator_id
    WHERE m.watched AND z.scaled AND NOT m.scaled;
    GET DIAGNOSTICS k = ROW_COUNT;

    UPDATE spy.operator_mark m SET scaled = s.scaled
    FROM (SELECT m2.operator_id, COALESCE(z.scaled, FALSE) AS scaled
          FROM spy.operator_mark m2
          LEFT JOIN spy.size_stats z ON z.kind = 'operator' AND z.subject_id = m2.operator_id
          WHERE m2.watched) s
    WHERE s.operator_id = m.operator_id AND m.scaled <> s.scaled;
    RETURN n + k;
END;
$$;

-- After a regrouping: a mark whose operator is gone moves to the operator
-- that holds most of its last accounts (merged with that one's own mark);
-- marks keep their operators' accounts; nicknames stay the operators' names.
-- Returns how many marks moved.
CREATE FUNCTION spy.carry_operator_marks() RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    r RECORD;
    n BIGINT := 0;
BEGIN
    FOR r IN
        SELECT m.*, (SELECT ao.operator_id FROM unnest(m.accounts) a(account_id)
                     JOIN spy.account_operator ao ON ao.account_id = a.account_id
                     GROUP BY ao.operator_id ORDER BY count(*) DESC, ao.operator_id LIMIT 1) AS new_id
        FROM spy.operator_mark m
        WHERE NOT EXISTS (SELECT 1 FROM spy.operator o WHERE o.id = m.operator_id)
    LOOP
        CONTINUE WHEN r.new_id IS NULL;
        INSERT INTO spy.operator_mark AS m (operator_id, hidden, watched, nickname, watched_since, scaled, made_by, updated_at)
        VALUES (r.new_id, r.hidden, r.watched, r.nickname, r.watched_since, r.scaled, r.made_by, now())
        ON CONFLICT (operator_id) DO UPDATE SET
            hidden = m.hidden OR EXCLUDED.hidden,
            watched = m.watched OR EXCLUDED.watched,
            nickname = COALESCE(m.nickname, EXCLUDED.nickname),
            watched_since = CASE WHEN m.watched AND EXCLUDED.watched THEN LEAST(m.watched_since, EXCLUDED.watched_since)
                                 WHEN m.watched THEN m.watched_since ELSE EXCLUDED.watched_since END,
            scaled = m.scaled OR EXCLUDED.scaled,
            updated_at = now();
        DELETE FROM spy.operator_mark WHERE operator_id = r.operator_id;
        n := n + 1;
    END LOOP;

    UPDATE spy.operator_mark m SET accounts = x.accounts
    FROM (SELECT ao.operator_id, array_agg(ao.account_id ORDER BY ao.account_id) AS accounts
          FROM spy.account_operator ao
          WHERE ao.operator_id IN (SELECT operator_id FROM spy.operator_mark)
          GROUP BY 1) x
    WHERE x.operator_id = m.operator_id AND m.accounts IS DISTINCT FROM x.accounts;

    UPDATE spy.operator o SET display_name = m.nickname, name_is_manual = TRUE,
        name = concat_ws(' · ', m.nickname, o.seller, o.code), updated_at = now()
    FROM spy.operator_mark m
    WHERE m.operator_id = o.id AND m.nickname IS NOT NULL
      AND (o.display_name IS DISTINCT FROM m.nickname OR NOT o.name_is_manual
           OR o.name IS DISTINCT FROM concat_ws(' · ', m.nickname, o.seller, o.code));
    RETURN n;
END;
$$;

-- Published: what people marked on each operator.
CREATE VIEW spy_api.operator_mark_v1 AS
SELECT operator_id, hidden, watched, nickname, watched_since, made_by, updated_at FROM spy.operator_mark;

-- Published: watched operators' moves, and whether Pushcut delivered them.
CREATE VIEW spy_api.watch_notice_v1 AS
SELECT id, operator_id, reason, at, title, body, status, error, sent_at FROM spy.watch_notice;

-- Published: why each site and account is in its operator, as the last
-- grouping found it: the first rule that tied it in, or the hand fix.
CREATE VIEW spy_api.operator_member_v1 AS
SELECT m.member, m.member_id, m.operator_id, m.reason, m.agency, s.domain
FROM spy.grouping_member m
LEFT JOIN spy.site s ON m.member = 'site' AND s.id = m.member_id;

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

REVOKE EXECUTE ON FUNCTION spy_api.mark_operator_v1(INTEGER, BOOLEAN, BOOLEAN, TEXT, TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION spy_api.fix_vertical_v1(INTEGER, TEXT, TEXT, TEXT) FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'spy_api_read') THEN
        GRANT SELECT ON spy_api.operator_mark_v1, spy_api.watch_notice_v1, spy_api.operator_member_v1 TO spy_api_read;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'spy_web') THEN
        GRANT EXECUTE ON FUNCTION spy_api.mark_operator_v1(INTEGER, BOOLEAN, BOOLEAN, TEXT, TEXT) TO spy_web;
        GRANT EXECUTE ON FUNCTION spy_api.fix_vertical_v1(INTEGER, TEXT, TEXT, TEXT) TO spy_web;
    END IF;
END;
$$;
