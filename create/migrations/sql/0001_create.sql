-- lint: new-table
-- Create: briefs, the options made from them, and saves into the library
-- (create/README.md). "create" is a reserved word in SQL, so the schema is
-- create_app. Pictures are kept in Create's file store; the rows hold their
-- keys. Nothing is deleted: a person hides an option by not choosing it.

-- A brief: what to make, for one vertical. state moves draft (being filled
-- in), reading (the performing ads are being read), making (options are
-- being made), ready (nothing running, options to choose from), failed
-- (nothing could be made; error says why). Create's worker moves it.
CREATE TABLE create_app.brief (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    -- Spy's vertical id, and its name as people read it.
    vertical_id TEXT NOT NULL DEFAULT '',
    vertical_name TEXT NOT NULL DEFAULT '',
    ages TEXT NOT NULL DEFAULT '',
    images INTEGER NOT NULL DEFAULT 6 CHECK (images BETWEEN 0 AND 12),
    headlines INTEGER NOT NULL DEFAULT 10 CHECK (headlines BETWEEN 0 AND 30),
    -- Angles to try, in the team's words ("Colher", "Garrafa").
    angles TEXT[] NOT NULL DEFAULT '{}',
    -- Headlines the person typed or pasted as their own references.
    own_headlines TEXT[] NOT NULL DEFAULT '{}',
    extra TEXT NOT NULL DEFAULT '',
    -- What the performing ads share: [{aspect, fixed, variable}], written by
    -- the reading and then edited by the person.
    analysis JSONB NOT NULL DEFAULT '[]',
    state TEXT NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'reading', 'making', 'ready', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    -- How many rounds of options were asked for.
    rounds INTEGER NOT NULL DEFAULT 0,
    cost_usd NUMERIC(12, 6) NOT NULL DEFAULT 0,
    requested_by TEXT NOT NULL DEFAULT '',
    -- Who asked: 'page' for Create's own pages, or the caller's key through
    -- create_api (desk:…). A key asks once: asking again returns the brief.
    origin TEXT NOT NULL DEFAULT 'page',
    origin_key TEXT UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX brief_recent ON create_app.brief (id DESC);

-- A performing ad (or any picture) the brief is made from: a Spy ad (the ad
-- id in tracks_api.ad_v1), a creative in the library, or a picture uploaded
-- from the person's computer. Its bytes are fetched and kept here before
-- anything reads them.
CREATE TABLE create_app.reference (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    brief_id BIGINT NOT NULL REFERENCES create_app.brief (id),
    kind TEXT NOT NULL CHECK (kind IN ('spy_ad', 'library_creative', 'upload')),
    ref_id TEXT NOT NULL DEFAULT '',
    -- The ad's headline, when it has one: a reference headline for the plan.
    headline TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'kept', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    file_key TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL DEFAULT '',
    removed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX reference_brief ON create_app.reference (brief_id);

-- One option: a picture or a headline made for a brief. A picture waits,
-- is made, and is done or failed; a headline is done when the plan is.
-- parent_id and note: a picture made again from another, with the person's
-- note. idea is the brief the picture was drawn from.
CREATE TABLE create_app.option (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    brief_id BIGINT NOT NULL REFERENCES create_app.brief (id),
    round INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('image', 'headline')),
    angle TEXT NOT NULL DEFAULT '',
    idea TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL DEFAULT '',
    parent_id BIGINT REFERENCES create_app.option (id),
    note TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('waiting', 'making', 'done', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    file_key TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL DEFAULT '',
    cost_usd NUMERIC(12, 6) NOT NULL DEFAULT 0,
    chosen BOOLEAN NOT NULL DEFAULT false,
    starred BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    CHECK (kind = 'image' OR text <> '')
);
CREATE INDEX option_brief ON create_app.option (brief_id, id);

-- A save of chosen options into the library, as one set. The worker writes
-- it; library_set_id is the set once it exists, so a retry reuses it.
CREATE TABLE create_app.save (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    brief_id BIGINT NOT NULL REFERENCES create_app.brief (id),
    name TEXT NOT NULL CHECK (name <> ''),
    option_ids BIGINT[] NOT NULL,
    -- The person's answer, for every picture in the save.
    ai_label TEXT NOT NULL CHECK (ai_label IN ('unset', 'ai', 'not_ai')),
    state TEXT NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'saving', 'done', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    library_set_id BIGINT,
    requested_by TEXT NOT NULL DEFAULT '',
    origin_key TEXT UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);
CREATE INDEX save_brief ON create_app.save (brief_id);

-- The worker's queue. kind: read (the performing ads), plan (one round of
-- headlines and picture ideas), image (one picture: option_id), save
-- (save_id). input holds what the job needs beyond its brief. A job left
-- running when the service stopped is picked up again at its start, except
-- a picture, which fails (it may have been paid for; the person decides).
CREATE TABLE create_app.job (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    brief_id BIGINT NOT NULL REFERENCES create_app.brief (id),
    kind TEXT NOT NULL CHECK (kind IN ('read', 'plan', 'image', 'save')),
    option_id BIGINT REFERENCES create_app.option (id),
    save_id BIGINT REFERENCES create_app.save (id),
    input JSONB NOT NULL DEFAULT '{}',
    state TEXT NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'running', 'done', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE INDEX job_open ON create_app.job (id) WHERE state IN ('waiting', 'running');
CREATE INDEX job_brief ON create_app.job (brief_id);

-- The making log a brief's page shows, one line per step, in pt-BR.
CREATE TABLE create_app.event (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    brief_id BIGINT NOT NULL REFERENCES create_app.brief (id),
    line TEXT NOT NULL,
    failed BOOLEAN NOT NULL DEFAULT false,
    at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX event_brief ON create_app.event (brief_id, id);

-- settle puts a brief's state right after its jobs changed: making while a
-- plan or picture is waiting or running, reading while only a read is,
-- ready when it has options, failed when its last plan or picture failed
-- with nothing to show, draft otherwise.
CREATE FUNCTION create_app.settle(p_brief_id BIGINT) RETURNS TEXT
LANGUAGE plpgsql AS $$
DECLARE
    v_state TEXT;
    v_error TEXT := '';
BEGIN
    IF EXISTS (SELECT 1 FROM create_app.job WHERE brief_id = p_brief_id AND kind IN ('plan', 'image') AND state IN ('waiting', 'running')) THEN
        v_state := 'making';
    ELSIF EXISTS (SELECT 1 FROM create_app.job WHERE brief_id = p_brief_id AND kind = 'read' AND state IN ('waiting', 'running')) THEN
        v_state := 'reading';
    ELSIF EXISTS (SELECT 1 FROM create_app.option WHERE brief_id = p_brief_id AND state = 'done') THEN
        v_state := 'ready';
    ELSE
        SELECT CASE WHEN j.state = 'failed' THEN 'failed' ELSE 'draft' END, CASE WHEN j.state = 'failed' THEN j.error ELSE '' END
        INTO v_state, v_error
        FROM create_app.job j WHERE j.brief_id = p_brief_id AND j.kind IN ('plan', 'image') ORDER BY j.id DESC LIMIT 1;
        IF NOT FOUND THEN
            v_state := 'draft';
            v_error := '';
        END IF;
    END IF;
    UPDATE create_app.brief SET state = v_state, error = COALESCE(v_error, ''), updated_at = now()
    WHERE id = p_brief_id AND (state <> v_state OR error <> COALESCE(v_error, ''));
    RETURN v_state;
END;
$$;
