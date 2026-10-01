-- A set an app renames (Create's session) has its Drive folder renamed on
-- the next pass; this marks the ones waiting for it.
ALTER TABLE library.set ADD COLUMN rename_folder BOOLEAN NOT NULL DEFAULT false;
