-- lint: new-table
-- Launch's own records: the presets people save, the pairs it made, every
-- change it made on an ad network (History), the moves waiting for their
-- copies to start, and drafts of new pairs. Ad networks keep the campaigns
-- themselves; these tables keep what only Launch knows about them.

-- A preset is a set of fields people save to fill a new group or a new
-- campaign pair. Nobody ships fixed ones: the team makes its own
-- (2026-09-30). account '' is a preset for every account of the network.
CREATE TABLE launch.preset (
    id bigserial PRIMARY KEY,
    level text NOT NULL CHECK (level IN ('group', 'campaign')),
    network text NOT NULL,
    account text NOT NULL DEFAULT '',
    name text NOT NULL CHECK (name <> ''),
    fields jsonb NOT NULL,
    made_by text NOT NULL DEFAULT '',
    made_at timestamptz NOT NULL DEFAULT now(),
    changed_by text NOT NULL DEFAULT '',
    changed_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (network, account, level, name)
);

-- A pair is the desktop and the mobile campaign Launch made together, with
-- the same settings. Either id is '' until that campaign exists.
CREATE TABLE launch.pair (
    id bigserial PRIMARY KEY,
    network text NOT NULL,
    account text NOT NULL,
    group_id text NOT NULL DEFAULT '',
    name text NOT NULL,
    desktop_id text NOT NULL DEFAULT '',
    mobile_id text NOT NULL DEFAULT '',
    preset_id bigint REFERENCES launch.preset (id) ON DELETE SET NULL,
    made_by text NOT NULL DEFAULT '',
    made_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pair_desktop ON launch.pair (network, account, desktop_id) WHERE desktop_id <> '';
CREATE INDEX pair_mobile ON launch.pair (network, account, mobile_id) WHERE mobile_id <> '';

-- History: one row per thing Launch did on an ad network, who did it, who
-- asked (a person, an Intel suggestion, Desk), before and after.
CREATE TABLE launch.change (
    id bigserial PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT now(),
    who text NOT NULL DEFAULT '',
    asked_by text NOT NULL DEFAULT '',
    network text NOT NULL,
    account text NOT NULL,
    group_id text NOT NULL DEFAULT '',
    campaign_id text NOT NULL DEFAULT '',
    kind text NOT NULL CHECK (kind IN ('new_group', 'new_pair', 'copy', 'move', 'pause', 'change', 'move_done')),
    summary text NOT NULL,
    before jsonb,
    after jsonb,
    result text NOT NULL CHECK (result IN ('done', 'partial', 'failed', 'waiting')),
    problems text[] NOT NULL DEFAULT '{}'
);
CREATE INDEX change_at ON launch.change (at DESC);
CREATE INDEX change_campaign ON launch.change (network, account, campaign_id, at DESC);

-- A move is a copy in another group; the original pauses when a person
-- starts the copy (originals = 'when_started'), at once ('now'), or never
-- ('leave').
CREATE TABLE launch.move (
    id bigserial PRIMARY KEY,
    change_id bigint NOT NULL REFERENCES launch.change (id),
    network text NOT NULL,
    account text NOT NULL,
    from_campaign text NOT NULL,
    to_campaign text NOT NULL,
    to_group text NOT NULL,
    originals text NOT NULL CHECK (originals IN ('when_started', 'now', 'leave')),
    state text NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'done', 'cancelled')),
    made_at timestamptz NOT NULL DEFAULT now(),
    done_at timestamptz
);
CREATE INDEX move_waiting ON launch.move (made_at) WHERE state = 'waiting';

-- A draft is a new pair not sent yet, saved as the page holds it.
CREATE TABLE launch.draft (
    id bigserial PRIMARY KEY,
    network text NOT NULL,
    account text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    body jsonb NOT NULL,
    made_by text NOT NULL DEFAULT '',
    changed_at timestamptz NOT NULL DEFAULT now()
);
