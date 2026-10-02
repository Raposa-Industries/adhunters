-- lint: new-table
-- Headlines a person typed in Drive (2 Oct 2026): a Google Doc or text file
-- named "Headlines" in a folder, one headline per line. Every change only
-- adds.

-- Each text such a file held when it was read, as it came (raw before
-- parsing), found by its sha256.
CREATE TABLE library.drive_text (
    sha256 TEXT PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    body BYTEA NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One person's headline file: the text it held when last read, and the
-- folder it was read into. read_at is cleared when the file is gone, so it
-- is read again if it comes back.
CREATE TABLE library.drive_headlines (
    file_id TEXT PRIMARY KEY REFERENCES library.drive_file (file_id),
    raw_sha256 TEXT REFERENCES library.drive_text (sha256),
    vertical_id TEXT,
    set_id BIGINT REFERENCES library.set (id),
    read_at TIMESTAMPTZ
);

-- The headlines each file lists. removed_at: its line was taken out (or the
-- file is gone); listed again, it is cleared.
CREATE TABLE library.drive_file_headline (
    file_id TEXT NOT NULL REFERENCES library.drive_file (file_id),
    headline_id BIGINT NOT NULL REFERENCES library.headline (id),
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    removed_at TIMESTAMPTZ,
    PRIMARY KEY (file_id, headline_id)
);
CREATE INDEX drive_file_headline_headline ON library.drive_file_headline (headline_id);
