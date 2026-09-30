-- lint: new-table
-- Counts per closed hour, from the journeys that started in it. Journeys a
-- bot check flagged are left out of every count but the bots columns.
-- Empty sub values are '' so they can be part of a key.

CREATE TABLE funnels.journey_hourly (
    hour TIMESTAMPTZ NOT NULL,
    site TEXT NOT NULL,
    first_lp TEXT NOT NULL,
    sub1 TEXT NOT NULL,
    sub4 TEXT NOT NULL,
    sub8 TEXT NOT NULL,
    device TEXT NOT NULL,
    country TEXT NOT NULL,
    journeys INTEGER NOT NULL,
    with_click_id INTEGER NOT NULL,
    with_input INTEGER NOT NULL,
    visible_ms BIGINT NOT NULL,
    bots INTEGER NOT NULL,
    PRIMARY KEY (hour, site, first_lp, sub1, sub4, sub8, device, country)
);

CREATE TABLE funnels.step_hourly (
    hour TIMESTAMPTZ NOT NULL,
    site TEXT NOT NULL,
    lp TEXT NOT NULL,
    step TEXT NOT NULL,
    sub1 TEXT NOT NULL,
    sub4 TEXT NOT NULL,
    sub8 TEXT NOT NULL,
    device TEXT NOT NULL,
    reached INTEGER NOT NULL,          -- journeys that reached it
    stopped INTEGER NOT NULL,          -- ... and went no further (drop-off)
    bots INTEGER NOT NULL,
    PRIMARY KEY (hour, site, lp, step, sub1, sub4, sub8, device)
);

CREATE TABLE funnels.video_hourly (
    hour TIMESTAMPTZ NOT NULL,
    site TEXT NOT NULL,
    video TEXT NOT NULL,
    arm TEXT NOT NULL,
    sub1 TEXT NOT NULL,
    sub4 TEXT NOT NULL,
    sub8 TEXT NOT NULL,
    device TEXT NOT NULL,
    loads INTEGER NOT NULL,            -- journeys whose page had the player
    autoplays INTEGER NOT NULL,
    plays INTEGER NOT NULL,
    watched_s BIGINT NOT NULL,
    len_s INTEGER NOT NULL,            -- the longest length seen
    reached_pitch INTEGER NOT NULL,
    PRIMARY KEY (hour, site, video, arm, sub1, sub4, sub8, device)
);

-- The retention curve: for each second, the plays that heard it.
CREATE TABLE funnels.video_second_hourly (
    hour TIMESTAMPTZ NOT NULL,
    video TEXT NOT NULL,
    arm TEXT NOT NULL,
    device TEXT NOT NULL,
    second INTEGER NOT NULL,
    watching INTEGER NOT NULL,
    PRIMARY KEY (hour, video, arm, device, second)
);
