-- lint: new-table
-- Counts for hours not closed yet, the data-center networks the bot check
-- uses, and read access for funnels-web.

-- Drafts: an hour that has not closed yet is counted every few minutes into
-- these copies of the journey and count tables, so the pages can show it,
-- marked partial. funnels_api never reads them, and an hour's drafts go
-- when it closes.
CREATE SCHEMA funnels_draft;
CREATE TABLE funnels_draft.journey (LIKE funnels.journey INCLUDING ALL);
CREATE TABLE funnels_draft.journey_step (
    LIKE funnels.journey_step INCLUDING ALL,
    FOREIGN KEY (journey) REFERENCES funnels_draft.journey (id) ON DELETE CASCADE
);
CREATE TABLE funnels_draft.journey_video (
    LIKE funnels.journey_video INCLUDING ALL,
    FOREIGN KEY (journey) REFERENCES funnels_draft.journey (id) ON DELETE CASCADE
);
CREATE TABLE funnels_draft.journey_hourly (LIKE funnels.journey_hourly INCLUDING ALL);
CREATE TABLE funnels_draft.step_hourly (LIKE funnels.step_hourly INCLUDING ALL);
CREATE TABLE funnels_draft.video_hourly (LIKE funnels.video_hourly INCLUDING ALL);
CREATE TABLE funnels_draft.video_second_hourly (LIKE funnels.video_second_hourly INCLUDING ALL);

ALTER TABLE funnels.hour_state ADD COLUMN draft_at TIMESTAMPTZ;  -- last draft count, while not closed

-- Networks of data centers and clouds. A journey whose visitor's network is
-- inside one is flagged as a bot suspect ("data-center network"). Each
-- source's list is replaced whole when it is fetched or imported again.
CREATE TABLE funnels.dc_network (
    source TEXT NOT NULL,              -- aws, gcp, or a name given at import
    prefix CIDR NOT NULL,
    PRIMARY KEY (source, prefix)
);

CREATE TABLE funnels.dc_network_load (
    source TEXT PRIMARY KEY,
    loaded_at TIMESTAMPTZ NOT NULL,
    prefixes INTEGER NOT NULL,
    raw_key TEXT NOT NULL DEFAULT ''   -- the downloaded list as received, in the archive
);

-- funnels-web reads Funnels' own tables, drafts included, and writes
-- nothing. Its login gets them through funnels_web_read, which the box
-- setup creates before this runs.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'funnels_web_read') THEN
        GRANT USAGE ON SCHEMA funnels, funnels_draft TO funnels_web_read;
        GRANT SELECT ON ALL TABLES IN SCHEMA funnels, funnels_draft TO funnels_web_read;
        ALTER DEFAULT PRIVILEGES IN SCHEMA funnels, funnels_draft GRANT SELECT ON TABLES TO funnels_web_read;
    END IF;
END;
$$;
