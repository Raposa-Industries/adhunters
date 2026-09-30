-- lint: new-table
-- Beacons from our landing pages: the raw files they arrived in, and each
-- event parsed from them. funnels-loader writes both; the events of a file
-- are replaced whole each time it loads, so loading twice equals once.

-- One row per raw file in the archive, inserted once its upload is verified.
-- Kept forever: it is the index of the archive.
CREATE TABLE funnels.raw_file (
    key TEXT PRIMARY KEY,              -- events/<yyyy>/<mm>/<dd>/<hh>/edge-<instance>-<hhmm>[-n].ndjson.zst
    minute TIMESTAMPTZ NOT NULL,       -- the minute the edge wrote it in
    rows INTEGER NOT NULL,             -- beacons in the file
    bytes BIGINT NOT NULL,             -- compressed size in the archive
    sha256 TEXT NOT NULL,              -- of the compressed file
    archived_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    loaded_at TIMESTAMPTZ,
    events INTEGER,                    -- events loaded from it
    bad_beacons INTEGER,               -- beacons that did not parse (kept in the archive, counted here)
    attempts SMALLINT NOT NULL DEFAULT 0,
    last_error TEXT,
    quarantined_at TIMESTAMPTZ         -- set aside after 3 failed loads; an alert, and replay once fixed
);
CREATE INDEX raw_file_pending_idx ON funnels.raw_file (minute) WHERE loaded_at IS NULL AND quarantined_at IS NULL;
CREATE INDEX raw_file_minute_idx ON funnels.raw_file (minute);

-- One event of one beacon. received_at is the edge's clock and decides the
-- hour; sent_at is the browser's, kept for order within a page.
CREATE TABLE funnels.event (
    file_key TEXT NOT NULL REFERENCES funnels.raw_file (key),
    line INTEGER NOT NULL,             -- the beacon's line in the file, from 1
    n SMALLINT NOT NULL,               -- the event's place in its beacon, from 0
    received_at TIMESTAMPTZ NOT NULL,
    sent_at TIMESTAMPTZ,
    journey TEXT NOT NULL,
    site TEXT NOT NULL,
    lp TEXT NOT NULL,                  -- the landing page: its ah-lp name, or its path
    url TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,                -- view, beat, scroll, step, click, form, exit, video
    clickid TEXT NOT NULL DEFAULT '',  -- the tracker click id (RedTrack's clickid)
    subs JSONB NOT NULL DEFAULT '{}',  -- sub1…sub10 and utm_* from the landing URL
    ua TEXT NOT NULL DEFAULT '',
    country TEXT NOT NULL DEFAULT '',
    ip_hash TEXT NOT NULL DEFAULT '',
    net TEXT NOT NULL DEFAULT '',
    webdriver BOOLEAN NOT NULL DEFAULT false,
    had_input BOOLEAN NOT NULL DEFAULT false,
    screen_w INTEGER,
    data JSONB NOT NULL,               -- the event as the page script sent it
    PRIMARY KEY (file_key, line, n)
);
CREATE INDEX event_journey_idx ON funnels.event (journey, received_at);
CREATE INDEX event_received_idx ON funnels.event (received_at);

-- Hours whose journeys changed since their counts were computed. A journey
-- belongs to the hour of its first event.
CREATE TABLE funnels.hour_state (
    hour TIMESTAMPTZ PRIMARY KEY,
    dirty_since TIMESTAMPTZ,           -- NULL when the counts are current
    closed_at TIMESTAMPTZ,             -- last time its counts were computed
    journeys INTEGER                   -- journeys that started in it, at the last close
);
CREATE INDEX hour_state_dirty_idx ON funnels.hour_state (hour) WHERE dirty_since IS NOT NULL;
