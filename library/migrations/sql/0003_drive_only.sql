-- lint: new-table
-- Drive only (decisions/0020-library-on-drive-only.md): a creative's bytes
-- live in the team's Drive folder and nowhere else of ours. Until a picture is
-- uploaded, its bytes wait in pending; the upload clears them. Its thumbnail,
-- a small JPEG, is kept here so lists never wait on Drive. file_key and
-- thumb_key were the old bucket's keys; new rows leave them empty.
ALTER TABLE library.creative
    ADD COLUMN pending BYTEA,
    ADD COLUMN thumb BYTEA,
    ALTER COLUMN file_key SET DEFAULT '',
    ALTER COLUMN thumb_key SET DEFAULT '';

-- Drive's listing pages as they came (raw first), one row per distinct page;
-- library.drive_run.pages holds each pass's sha256s in order.
CREATE TABLE library.drive_page (
    sha256 TEXT PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    body BYTEA NOT NULL,
    kept_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
