-- Desk's own data: conversations, the model's side of them, plans and their
-- steps, every call Desk makes, the to-do list and the settings. Other
-- services read desk_api (0002), never these tables.
-- lint: new-table (every index here is on a table this file creates)

CREATE TABLE desk.setting (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    says       TEXT NOT NULL DEFAULT '',
    changed_by TEXT NOT NULL DEFAULT '',
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO desk.setting (key, value, says) VALUES
    ('stopped', 'false', 'true stops Desk at once: no model call and no step runs until someone starts it again'),
    ('model', 'claude-opus-5-5', 'the Claude model Desk talks with'),
    ('effort', 'medium', 'how hard the model thinks: low, medium, high, xhigh or max'),
    ('daily_usd', '20', 'the most Desk spends on Claude in a UTC day; past it, Desk waits for the next day and says so'),
    ('turn_calls', '12', 'the most model calls one turn may make before Desk stops and asks the person how to go on');

-- One conversation with one person. Desk acts for that person, with their
-- rights. wants_turn is set when something new waits for Desk's answer;
-- heard_up_to is the last message the model has been given.
CREATE TABLE desk.conversation (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title         TEXT NOT NULL DEFAULT '',
    person        TEXT NOT NULL CHECK (person <> ''),
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    state         TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'stopped', 'closed')),
    wants_turn    BOOLEAN NOT NULL DEFAULT false,
    heard_up_to   BIGINT NOT NULL DEFAULT 0,
    claim_token   UUID,
    claimed_until TIMESTAMPTZ
);
CREATE INDEX conversation_person ON desk.conversation (person, last_at DESC);
CREATE INDEX conversation_wants_turn ON desk.conversation (id) WHERE wants_turn AND state = 'open';

-- What the page shows: the person's messages, Desk's answers, plans, the
-- options Desk asks a person to choose among, and events from the steps.
-- A message that wakes Desk (the person's, a plan refused or ended) makes it
-- take a turn; the others reach the model with the next one.
CREATE TABLE desk.message (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id BIGINT NOT NULL REFERENCES desk.conversation (id),
    at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    author          TEXT NOT NULL CHECK (author <> ''),
    kind            TEXT NOT NULL CHECK (kind IN ('text', 'plan', 'choice', 'event')),
    body            TEXT NOT NULL,
    wakes           BOOLEAN NOT NULL DEFAULT false,
    plan_id         BIGINT,
    step_id         BIGINT
);
CREATE INDEX message_conversation ON desk.message (conversation_id, id);

-- The model's side of each conversation, exactly as sent and received, in
-- order: what was given to it (user) and what it answered (assistant, the
-- API's own JSON message). Rows are only added, so what the model saw never
-- changes. prefix names the instructions and tools the model had then: its
-- reasoning is sent back only while they are the same.
CREATE TABLE desk.turn (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id BIGINT NOT NULL REFERENCES desk.conversation (id),
    at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    role            TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content         JSONB NOT NULL,
    prefix          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX turn_conversation ON desk.turn (conversation_id, id);

-- Every call to Claude, what it cost and how it ended.
CREATE TABLE desk.model_call (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    conversation_id    BIGINT,
    purpose            TEXT NOT NULL CHECK (purpose IN ('turn', 'suggest')),
    model              TEXT NOT NULL,
    input_tokens       INTEGER NOT NULL,
    output_tokens      INTEGER NOT NULL,
    cache_read_tokens  INTEGER NOT NULL,
    cache_write_tokens INTEGER NOT NULL,
    usd                NUMERIC(10, 4) NOT NULL,
    stop_reason        TEXT NOT NULL DEFAULT '',
    ms                 INTEGER NOT NULL
);
CREATE INDEX model_call_at ON desk.model_call (at);

-- A plan: what Desk proposes to do for one goal, as steps. Nothing that
-- changes anything runs before the person OKs it. The OK names the
-- fingerprint of the goal and steps the person saw, so a plan that changed
-- after the page was drawn cannot be OK'd by mistake.
CREATE TABLE desk.plan (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id BIGINT NOT NULL REFERENCES desk.conversation (id),
    goal            TEXT NOT NULL CHECK (goal <> ''),
    fingerprint     TEXT NOT NULL,
    state           TEXT NOT NULL DEFAULT 'proposed'
                    CHECK (state IN ('proposed', 'approved', 'refused', 'replaced', 'running', 'done', 'failed', 'stopped')),
    proposed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_by      TEXT,
    decided_at      TIMESTAMPTZ,
    ended_at        TIMESTAMPTZ,
    note            TEXT NOT NULL DEFAULT '',
    claim_token     UUID,
    claimed_until   TIMESTAMPTZ
);
CREATE INDEX plan_conversation ON desk.plan (conversation_id, id);
CREATE INDEX plan_active ON desk.plan (id) WHERE state IN ('approved', 'running');

-- One step of a plan. action: one catalog action (a change or an ask);
-- choose: Desk suggests some of a read's rows and the person picks; person:
-- a to-do for someone. uses fills inputs from earlier steps' results
-- ([{"step": 1, "into": "brief_id"}]). ref is what the app gave back
-- (followed in its view).
CREATE TABLE desk.step (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    plan_id        BIGINT NOT NULL REFERENCES desk.plan (id),
    n              SMALLINT NOT NULL CHECK (n > 0),
    kind           TEXT NOT NULL CHECK (kind IN ('action', 'choose', 'person')),
    says           TEXT NOT NULL,
    action         TEXT NOT NULL DEFAULT '',
    action_version INTEGER NOT NULL DEFAULT 0,
    input          JSONB NOT NULL DEFAULT '{}',
    uses           JSONB NOT NULL DEFAULT '[]',
    pick           SMALLINT NOT NULL DEFAULT 0,
    holder         TEXT NOT NULL DEFAULT '',
    due            DATE,
    state          TEXT NOT NULL DEFAULT 'waiting'
                   CHECK (state IN ('waiting', 'running', 'asked', 'done', 'failed', 'skipped')),
    result         JSONB,
    ref            TEXT NOT NULL DEFAULT '',
    link           TEXT NOT NULL DEFAULT '',
    todo_id        BIGINT,
    started_at     TIMESTAMPTZ,
    ended_at       TIMESTAMPTZ,
    error          TEXT NOT NULL DEFAULT '',
    UNIQUE (plan_id, n)
);

-- Every call Desk made to an app's action, whatever came of it.
CREATE TABLE desk.action_call (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    conversation_id BIGINT NOT NULL,
    step_id         BIGINT,
    action          TEXT NOT NULL,
    action_version  INTEGER NOT NULL,
    kind            TEXT NOT NULL,
    person          TEXT NOT NULL,
    input           JSONB NOT NULL,
    ok              BOOLEAN NOT NULL,
    result          JSONB,
    error           TEXT NOT NULL DEFAULT '',
    ms              INTEGER NOT NULL
);
CREATE INDEX action_call_action_at ON desk.action_call (action, at);

-- The to-do list: one piece of work someone holds, a person (their email)
-- or Desk. People hand them out by talking to Desk or on the page; plan
-- steps that need a person put one on that person's list.
CREATE TABLE desk.todo (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title           TEXT NOT NULL CHECK (title <> ''),
    holder          TEXT NOT NULL CHECK (holder <> ''),
    made_by         TEXT NOT NULL CHECK (made_by <> ''),
    made_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    due             DATE,
    link            TEXT NOT NULL DEFAULT '',
    conversation_id BIGINT,
    step_id         BIGINT,
    done_at         TIMESTAMPTZ,
    done_by         TEXT
);
CREATE INDEX todo_holder_open ON desk.todo (holder, id) WHERE done_at IS NULL;

CREATE TABLE desk.todo_note (
    id      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    todo_id BIGINT NOT NULL REFERENCES desk.todo (id),
    at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    author  TEXT NOT NULL CHECK (author <> ''),
    body    TEXT NOT NULL CHECK (body <> '')
);
CREATE INDEX todo_note_todo ON desk.todo_note (todo_id, id);

-- History is only added to: what was said, what the model saw, what Desk
-- called and what it cost.
CREATE FUNCTION desk.refuse_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'desk.% rows are only ever added', TG_TABLE_NAME;
END;
$$;
CREATE TRIGGER message_only_added BEFORE UPDATE OR DELETE ON desk.message
    FOR EACH ROW EXECUTE FUNCTION desk.refuse_change();
CREATE TRIGGER turn_only_added BEFORE UPDATE OR DELETE ON desk.turn
    FOR EACH ROW EXECUTE FUNCTION desk.refuse_change();
CREATE TRIGGER model_call_only_added BEFORE UPDATE OR DELETE ON desk.model_call
    FOR EACH ROW EXECUTE FUNCTION desk.refuse_change();
CREATE TRIGGER action_call_only_added BEFORE UPDATE OR DELETE ON desk.action_call
    FOR EACH ROW EXECUTE FUNCTION desk.refuse_change();
