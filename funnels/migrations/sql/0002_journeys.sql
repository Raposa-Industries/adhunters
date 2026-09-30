-- lint: new-table
-- Journeys, rebuilt from their events each time their hour closes: one row
-- per journey, the steps it reached in order, and what it watched of each
-- video. Nothing here is typed in by hand.

CREATE TABLE funnels.journey (
    id TEXT PRIMARY KEY,
    hour TIMESTAMPTZ NOT NULL,         -- the hour of its first event
    started_at TIMESTAMPTZ NOT NULL,
    last_at TIMESTAMPTZ NOT NULL,
    site TEXT NOT NULL,
    first_lp TEXT NOT NULL,
    last_lp TEXT NOT NULL,
    last_step TEXT NOT NULL,           -- where it stopped
    lps INTEGER NOT NULL,              -- landing page views
    clickid TEXT NOT NULL,
    sub1 TEXT NOT NULL,                -- Taboola campaign, through RedTrack's preset
    sub4 TEXT NOT NULL,                -- Taboola item
    sub8 TEXT NOT NULL,                -- Taboola site id
    subs JSONB NOT NULL,
    device TEXT NOT NULL,              -- phone, tablet or desktop
    country TEXT NOT NULL,
    visible_ms BIGINT NOT NULL,        -- time with the page in view, all pages
    max_scroll SMALLINT NOT NULL,      -- deepest scroll, percent, any page
    had_input BOOLEAN NOT NULL,
    bot_suspect BOOLEAN NOT NULL,
    bot_reason TEXT NOT NULL
);
CREATE INDEX journey_hour_idx ON funnels.journey (hour);
CREATE INDEX journey_clickid_idx ON funnels.journey (clickid) WHERE clickid <> '';

CREATE TABLE funnels.journey_step (
    journey TEXT NOT NULL REFERENCES funnels.journey (id) ON DELETE CASCADE,
    seq SMALLINT NOT NULL,             -- order of first reach, from 1
    lp TEXT NOT NULL,
    step TEXT NOT NULL,
    at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (journey, seq)
);

CREATE TABLE funnels.journey_video (
    journey TEXT NOT NULL REFERENCES funnels.journey (id) ON DELETE CASCADE,
    video TEXT NOT NULL,
    arm TEXT NOT NULL,
    lp TEXT NOT NULL,
    len_s INTEGER NOT NULL,            -- 0 when the player never read it
    pitch_s INTEGER,
    autoplayed BOOLEAN NOT NULL,
    played BOOLEAN NOT NULL,           -- unmuted the autoplay, or pressed play
    watched int4multirange NOT NULL,   -- seconds heard, each once
    watched_s INTEGER NOT NULL,
    last_s INTEGER NOT NULL,           -- furthest second heard, -1 for none
    reached_pitch BOOLEAN NOT NULL,
    PRIMARY KEY (journey, video)
);
