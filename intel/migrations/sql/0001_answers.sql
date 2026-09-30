-- lint: new-table
-- Every answer Intel receives from Taboola or RedTrack, kept as received
-- (gzip), before anything reads it (decision 0003). intel-collect writes an
-- answer to its spool on disk first, so collection carries on while the
-- database is away, then moves it here. intel-numbers parses answers into
-- the tables of 0002 and can parse any range again (`intel-numbers reload`).
--
-- Taboola's 5-minute numbers exist nowhere else after 24 hours, so nothing
-- here is ever deleted. Moving old answers to object storage comes later,
-- with its own decision.

CREATE TABLE intel.answer (
    id BIGSERIAL PRIMARY KEY,
    -- The spool file's name: a retry after a crash inserts nothing twice.
    spool_id TEXT NOT NULL UNIQUE,
    source TEXT NOT NULL CHECK (source IN ('taboola', 'redtrack')),
    -- Which key asked: a Taboola login or a RedTrack login, as named in the
    -- collector's settings ("zoltagroup", "sandbox", "team").
    login TEXT NOT NULL,
    -- The Taboola account the answer is about ('' for account lists and for
    -- RedTrack).
    account TEXT NOT NULL DEFAULT '',
    -- What was asked, one of the kinds in intel/collect (taboola.campaign_day,
    -- redtrack.item_day…); the loader picks its parser by it.
    kind TEXT NOT NULL,
    -- The request path and query, keys removed.
    path TEXT NOT NULL,
    -- What the request covered, as the loader needs it: days (from, to, in
    -- the account's time zone), a realtime window, the time zone asked for.
    params JSONB NOT NULL DEFAULT '{}',
    status INTEGER NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    body BYTEA NOT NULL,
    body_bytes INTEGER NOT NULL,
    stored_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    loaded_at TIMESTAMPTZ,
    load_error TEXT
);

CREATE INDEX answer_kind_fetched ON intel.answer (kind, fetched_at);
CREATE INDEX answer_unloaded ON intel.answer (id) WHERE loaded_at IS NULL;
