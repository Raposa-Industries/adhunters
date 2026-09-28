-- lint: new-table
-- The investigations copied from the collector's database (spy.raposa_job
-- and the tables under it) by raposa-engine import-old. One row per job
-- copied, in the same transaction as the copy, so running the import again
-- copies only what is not here yet.

CREATE TABLE raposa.imported_investigation (
    -- spy.raposa_job.id and .uid in the collector's database.
    old_id INTEGER PRIMARY KEY,
    old_uid UUID,
    investigation_id BIGINT NOT NULL UNIQUE REFERENCES raposa.investigation (id) ON DELETE CASCADE,
    imported_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
