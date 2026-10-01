-- lint: new-table
-- Create as a chat (decision 0019): a person opens a session in a vertical,
-- sends turns (a prompt and the items they picked), and each turn makes new
-- pictures and headlines from them. Any item can be picked for the next
-- turn, so a picture or headline is iterated on as long as the person likes.
-- What they keep is saved into the library, in the session's own folder
-- inside the vertical's.
--
-- The brief tables of 0001 stay as they are, with their rows: nothing writes
-- them any more.

-- One session: a vertical and a name, which are also the library folders its
-- saves go in (<vertical>/<session name>). library_set_id is that set once
-- the first save made it.
CREATE TABLE create_app.session (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 120),
    vertical_id    TEXT NOT NULL CHECK (vertical_id ~ '^[a-z0-9-]{1,60}$'),
    vertical_name  TEXT NOT NULL,
    library_set_id BIGINT,
    made_by        TEXT NOT NULL DEFAULT '',
    origin_key     TEXT UNIQUE,
    cost_usd       NUMERIC(10, 4) NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX session_updated ON create_app.session (updated_at DESC);

-- A name is one folder in its vertical, so it is used once per vertical.
CREATE UNIQUE INDEX session_name ON create_app.session (vertical_id, lower(name));

-- One turn: what the person sent. picked are the items it starts from
-- (pictures go to the picture model, headlines are varied); images and
-- headlines are how many of each to make. state is making while anything of
-- it runs, then done, or failed when nothing came out (error says why).
CREATE TABLE create_app.turn (
    id         BIGSERIAL PRIMARY KEY,
    session_id BIGINT NOT NULL REFERENCES create_app.session (id),
    prompt     TEXT NOT NULL DEFAULT '',
    picked     BIGINT[] NOT NULL DEFAULT '{}',
    images     INT NOT NULL DEFAULT 0 CHECK (images BETWEEN 0 AND 8),
    headlines  INT NOT NULL DEFAULT 0 CHECK (headlines BETWEEN 0 AND 20),
    state      TEXT NOT NULL DEFAULT 'making' CHECK (state IN ('making', 'done', 'failed')),
    error      TEXT NOT NULL DEFAULT '',
    made_by    TEXT NOT NULL DEFAULT '',
    origin_key TEXT UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (images + headlines > 0)
);

CREATE INDEX turn_session ON create_app.turn (session_id, id);

-- One item of a session: a picture (kind image) or a headline (kind
-- headline). origin says where it came from: made by a turn, uploaded from a
-- computer, taken from the library, or typed. from_ids are the items its
-- turn started from. A made picture is waiting, making, done or failed;
-- everything else is done when it is added. brief is what the picture model
-- was told. library_id is the library's creative or headline once saved.
CREATE TABLE create_app.item (
    id          BIGSERIAL PRIMARY KEY,
    session_id  BIGINT NOT NULL REFERENCES create_app.session (id),
    turn_id     BIGINT REFERENCES create_app.turn (id),
    kind        TEXT NOT NULL CHECK (kind IN ('image', 'headline')),
    origin      TEXT NOT NULL CHECK (origin IN ('made', 'upload', 'library', 'typed')),
    from_ids    BIGINT[] NOT NULL DEFAULT '{}',
    text        TEXT NOT NULL DEFAULT '',
    brief       TEXT NOT NULL DEFAULT '',
    angle       TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'done' CHECK (state IN ('waiting', 'making', 'done', 'failed')),
    error       TEXT NOT NULL DEFAULT '',
    file_key    TEXT,
    media_type  TEXT NOT NULL DEFAULT '',
    width       INT NOT NULL DEFAULT 0,
    height      INT NOT NULL DEFAULT 0,
    sha256      TEXT NOT NULL DEFAULT '',
    cost_usd    NUMERIC(10, 4) NOT NULL DEFAULT 0,
    library_ref TEXT NOT NULL DEFAULT '',
    library_id  BIGINT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    CHECK (kind = 'image' OR btrim(text) <> '')
);

CREATE INDEX item_session ON create_app.item (session_id, id);

-- One save of picked items into the library, in the session's set. state is
-- waiting, saving, done or failed.
CREATE TABLE create_app.session_save (
    id          BIGSERIAL PRIMARY KEY,
    session_id  BIGINT NOT NULL REFERENCES create_app.session (id),
    item_ids    BIGINT[] NOT NULL CHECK (cardinality(item_ids) > 0),
    ai_label    TEXT NOT NULL DEFAULT 'ai' CHECK (ai_label IN ('ai', 'not_ai', 'unset')),
    state       TEXT NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'saving', 'done', 'failed')),
    error       TEXT NOT NULL DEFAULT '',
    made_by     TEXT NOT NULL DEFAULT '',
    origin_key  TEXT UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE INDEX session_save_session ON create_app.session_save (session_id, id);

-- The worker's queue: a turn (writes headlines and the pictures' briefs), a
-- picture, or a save. Claimed with FOR UPDATE SKIP LOCKED.
CREATE TABLE create_app.work (
    id          BIGSERIAL PRIMARY KEY,
    session_id  BIGINT NOT NULL REFERENCES create_app.session (id),
    kind        TEXT NOT NULL CHECK (kind IN ('turn', 'image', 'save')),
    turn_id     BIGINT REFERENCES create_app.turn (id),
    item_id     BIGINT REFERENCES create_app.item (id),
    save_id     BIGINT REFERENCES create_app.session_save (id),
    state       TEXT NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'running', 'done', 'failed')),
    error       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE INDEX work_waiting ON create_app.work (id) WHERE state = 'waiting';

-- settle_turn puts a turn's state right from its work and items: making
-- while any of its work is open, then done when anything came out of it,
-- failed when nothing did.
CREATE FUNCTION create_app.settle_turn(p_turn_id BIGINT) RETURNS VOID
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE create_app.turn t SET state = CASE
        WHEN EXISTS (SELECT 1 FROM create_app.work w WHERE w.turn_id = t.id AND w.state IN ('waiting', 'running'))
          OR EXISTS (SELECT 1 FROM create_app.item i WHERE i.turn_id = t.id AND i.state IN ('waiting', 'making')) THEN 'making'
        WHEN EXISTS (SELECT 1 FROM create_app.item i WHERE i.turn_id = t.id AND i.state = 'done') THEN 'done'
        ELSE 'failed' END
    WHERE t.id = p_turn_id;
END;
$$;
