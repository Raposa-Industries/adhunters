-- The team's feedback on the chat (2 Oct 2026). Every change only adds:
--
-- A session's platform (the ad network its pictures are for): taboola by
-- default, or newsbreak, chosen when the session starts. Its saves make the
-- library set with it, which picks the network letter of minted names and
-- the Drive folder <vertical>/<platform>/<set>.
ALTER TABLE create_app.session
    ADD COLUMN platform TEXT NOT NULL DEFAULT 'taboola' CHECK (platform IN ('taboola', 'newsbreak'));

-- A turn's picture size (landscape 1600x896, vertical 896x1600, newsbreak
-- 1504x786), the model that writes its headlines ('' is OpenAI's, decision
-- 0024), and when a person interrupted it.
ALTER TABLE create_app.turn
    ADD COLUMN size TEXT NOT NULL DEFAULT 'landscape' CHECK (size IN ('landscape', 'vertical', 'newsbreak')),
    ADD COLUMN headline_model TEXT NOT NULL DEFAULT '',
    ADD COLUMN interrupted_at TIMESTAMPTZ;

-- settle_turn as in 0003, except that an interrupted turn no longer waits
-- for the work it had when it was interrupted: only work queued after that
-- (a picture tried again) keeps it making.
CREATE OR REPLACE FUNCTION create_app.settle_turn(p_turn_id BIGINT) RETURNS VOID
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE create_app.turn t SET state = CASE
        WHEN EXISTS (SELECT 1 FROM create_app.work w WHERE w.turn_id = t.id AND w.state IN ('waiting', 'running')
                       AND (t.interrupted_at IS NULL OR w.created_at > t.interrupted_at))
          OR (t.interrupted_at IS NULL
              AND EXISTS (SELECT 1 FROM create_app.item i WHERE i.turn_id = t.id AND i.state IN ('waiting', 'making'))) THEN 'making'
        WHEN EXISTS (SELECT 1 FROM create_app.item i WHERE i.turn_id = t.id AND i.state = 'done') THEN 'done'
        ELSE 'failed' END
    WHERE t.id = p_turn_id;
END;
$$;
