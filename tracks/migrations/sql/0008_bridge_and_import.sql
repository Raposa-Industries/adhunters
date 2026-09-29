-- lint: new-table
-- The switch-over from the collector (platform/SWITCH-OVER.md).
--
-- bridge_file: which raw files tracks-bridge wrote into the collector's
-- database, so today's Spy keeps reading fresh data there. One row per file
-- it took; a file without a row, or with bridged_at empty, is still to do.
-- Writing a file again stores nothing twice (the collector's scrape keys),
-- so tracks-bridge redo only deletes rows here.
CREATE TABLE tracks.bridge_file (
    raw_file_id BIGINT PRIMARY KEY REFERENCES tracks.raw_file(id),
    bridged_at TIMESTAMPTZ,
    scrapes INTEGER,          -- scrapes written (outcome ok; the collector stored no failed scrapes)
    sightings INTEGER,
    attempts SMALLINT NOT NULL DEFAULT 0,
    last_error TEXT,
    quarantined_at TIMESTAMPTZ
);

-- Hours whose counts tracks-loader import-old copied from the collector's
-- database. They have no sightings or raw files here, so a replay must not
-- close them again (it would replace the collector's counts with nothing).
ALTER TABLE tracks.hour_state ADD COLUMN imported_at TIMESTAMPTZ;
