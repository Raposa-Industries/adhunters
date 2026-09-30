-- lint: new-table
-- The library: every creative and headline the team keeps, shared by Create
-- and Launch (decisions/0014-library.md). The rows here are the truth; the
-- team's Google Drive folder holds a copy of each file that people can see
-- and add to, and the safe copy of each file's bytes stays on our side, so a
-- file deleted in Drive is never lost.

-- The verticals names are minted from. id is Spy's vertical id
-- (spy/verticals/verticals.yaml); name is what people read and the name of
-- the vertical's folder in Drive.
CREATE TABLE library.vertical (
    id TEXT PRIMARY KEY CHECK (id ~ '^[a-z0-9-]+$'),
    name TEXT NOT NULL CHECK (name <> ''),
    -- 2 to 4 capitals: the start of every name minted in this vertical.
    code TEXT NOT NULL UNIQUE CHECK (code ~ '^[A-Z]{2,4}$'),
    -- The ad network the names are for: T Taboola, N NewsBreak, O Outbrain.
    network_letter TEXT NOT NULL DEFAULT 'T' CHECK (network_letter ~ '^[A-Z]$'),
    -- The number the next creative saved into this vertical gets. It only
    -- goes up: a number is never given twice, even after a creative is hidden.
    next_number INTEGER NOT NULL DEFAULT 1 CHECK (next_number > 0),
    drive_folder_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The team's must-have verticals, with the codes their files already use
-- where we know them (auto-creative's MM for Memory Loss). Others are added
-- the first time something is saved into them.
INSERT INTO library.vertical (id, name, code) VALUES
    ('blood-pressure', 'Blood Pressure', 'BP'),
    ('memory-loss', 'Memory Loss', 'MM'),
    ('weight-loss', 'Weight Loss', 'WL'),
    ('tinnitus', 'Tinnitus', 'TN'),
    ('diabetes', 'Diabetes', 'DB'),
    ('neuropathy', 'Neuropathy', 'NP'),
    ('prostate-health', 'Prostate Health', 'PR'),
    ('joint-pain', 'Joint Pain', 'JP'),
    ('vision', 'Vision', 'VS');

-- A set: creatives and headlines made or uploaded together (one brief in
-- Create, one folder in Drive). Launch picks a set to make ads from.
CREATE TABLE library.set (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL CHECK (name <> ''),
    vertical_id TEXT REFERENCES library.vertical (id),
    origin TEXT NOT NULL CHECK (origin IN ('create', 'upload', 'drive')),
    -- What made it, in the maker's words: create:brief:12.
    origin_ref TEXT NOT NULL DEFAULT '',
    made_by TEXT NOT NULL DEFAULT '',
    drive_folder_id TEXT,
    -- The set's headlines are written to Drive as one text file whenever
    -- they changed after it was last written.
    headlines_changed_at TIMESTAMPTZ,
    headlines_written_at TIMESTAMPTZ,
    headlines_file_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One folder name per vertical in Drive.
CREATE UNIQUE INDEX set_name ON library.set (COALESCE(vertical_id, ''), lower(name));

-- One creative: one picture, found by its bytes. Saving the same bytes again
-- returns the same creative.
CREATE TABLE library.creative (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Minted from the vertical (BPT43) when saved from an app; the file's own
    -- name when it came from Drive. Not unique: Drive files can share names.
    name TEXT NOT NULL CHECK (name <> ''),
    vertical_id TEXT REFERENCES library.vertical (id),
    vertical_number INTEGER,
    angle TEXT NOT NULL DEFAULT '',
    -- The one-line idea the picture was made from, when an app made it.
    idea TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL CHECK (origin IN ('create', 'upload', 'drive')),
    origin_ref TEXT NOT NULL DEFAULT '',
    -- The person's answer to "made with AI?". Nobody answered: unset.
    ai_label TEXT NOT NULL DEFAULT 'unset' CHECK (ai_label IN ('unset', 'ai', 'not_ai')),
    made_by TEXT NOT NULL DEFAULT '',
    sha256 TEXT NOT NULL UNIQUE CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    md5 TEXT NOT NULL CHECK (md5 ~ '^[0-9a-f]{32}$'),
    -- The safe copy and the thumbnail, in the library's file store.
    file_key TEXT NOT NULL,
    thumb_key TEXT NOT NULL,
    media_type TEXT NOT NULL,
    width INTEGER NOT NULL CHECK (width > 0),
    height INTEGER NOT NULL CHECK (height > 0),
    bytes BIGINT NOT NULL CHECK (bytes > 0),
    -- waiting: not copied to Drive yet; in_drive: a Drive file holds it;
    -- gone: its Drive file was deleted (the safe copy stays).
    drive_state TEXT NOT NULL DEFAULT 'waiting' CHECK (drive_state IN ('waiting', 'in_drive', 'gone')),
    drive_file_id TEXT,
    drive_error TEXT NOT NULL DEFAULT '',
    -- A person took it out of the lists. Nothing is deleted.
    hidden_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (vertical_id, vertical_number)
);
CREATE INDEX creative_recent ON library.creative (created_at DESC, id DESC);
CREATE INDEX creative_vertical ON library.creative (vertical_id, created_at DESC);
CREATE INDEX creative_waiting ON library.creative (id) WHERE drive_state = 'waiting';

-- One headline, found by its text (cleaned of hidden characters). Saving the
-- same text again returns the same headline.
CREATE TABLE library.headline (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    text TEXT NOT NULL CHECK (text <> ''),
    sha256 TEXT NOT NULL UNIQUE CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    vertical_id TEXT REFERENCES library.vertical (id),
    angle TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL CHECK (origin IN ('create', 'upload', 'drive')),
    origin_ref TEXT NOT NULL DEFAULT '',
    ai_label TEXT NOT NULL DEFAULT 'unset' CHECK (ai_label IN ('unset', 'ai', 'not_ai')),
    made_by TEXT NOT NULL DEFAULT '',
    hidden_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX headline_vertical ON library.headline (vertical_id, created_at DESC);

-- What is in a set, in order. A creative or headline can be in several sets.
CREATE TABLE library.set_creative (
    set_id BIGINT NOT NULL REFERENCES library.set (id),
    creative_id BIGINT NOT NULL REFERENCES library.creative (id),
    position INTEGER NOT NULL,
    PRIMARY KEY (set_id, creative_id)
);
CREATE INDEX set_creative_creative ON library.set_creative (creative_id);

CREATE TABLE library.set_headline (
    set_id BIGINT NOT NULL REFERENCES library.set (id),
    headline_id BIGINT NOT NULL REFERENCES library.headline (id),
    position INTEGER NOT NULL,
    PRIMARY KEY (set_id, headline_id)
);
CREATE INDEX set_headline_headline ON library.set_headline (headline_id);

-- Every file and folder seen in, or written to, the team's Drive folder.
CREATE TABLE library.drive_file (
    file_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('folder', 'image', 'headlines', 'other')),
    name TEXT NOT NULL,
    mime_type TEXT NOT NULL DEFAULT '',
    md5 TEXT NOT NULL DEFAULT '',
    size BIGINT NOT NULL DEFAULT 0,
    parent_id TEXT NOT NULL DEFAULT '',
    -- The folders above it under the library folder, joined by "/".
    path TEXT NOT NULL DEFAULT '',
    modified_at TIMESTAMPTZ,
    creative_id BIGINT REFERENCES library.creative (id),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- No longer in the folder at the last whole listing.
    gone_at TIMESTAMPTZ,
    -- Why it could not be read, when it could not.
    error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX drive_file_creative ON library.drive_file (creative_id);

-- The Google login the library reaches Drive with. One row at most; written
-- by `library drive-login`. Readers of library_api never see it.
CREATE TABLE library.drive_login (
    id INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    account TEXT NOT NULL,
    client_id TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    signed_in_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One pass of the Drive sync: what it copied out and read in.
CREATE TABLE library.drive_run (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    written INTEGER NOT NULL DEFAULT 0,
    listed INTEGER NOT NULL DEFAULT 0,
    added INTEGER NOT NULL DEFAULT 0,
    gone INTEGER NOT NULL DEFAULT 0,
    -- Drive's listing pages as they came, in the file store (raw first).
    pages TEXT[] NOT NULL DEFAULT '{}',
    error TEXT NOT NULL DEFAULT ''
);
