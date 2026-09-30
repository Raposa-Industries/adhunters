-- The published face of the library, version 1. Each statement is the
-- matching file in contract/sql/library/, word for word (a test checks).
-- Create, Launch and later Intel read these and nothing else of the library;
-- they add to it through the library's HTTP API, since that carries files.

CREATE SCHEMA library_api;

-- The verticals the library names creatives in: Spy's vertical id, the name
-- people read and the code at the start of each minted name.
CREATE VIEW library_api.vertical_v1 AS
SELECT id, name, code, network_letter
FROM library.vertical;

-- One creative the team keeps. The picture and its thumbnail are served by
-- the library (GET /files/{id}, /thumbs/{id}); sha256 is of its bytes, and
-- its first 10 characters start the ad id. hidden_at set: taken out of the
-- lists by a person, still here for anything that points at it.
CREATE VIEW library_api.creative_v1 AS
SELECT id, name, vertical_id, angle, idea, origin, origin_ref, ai_label, made_by, sha256,
       media_type, width, height, bytes, drive_state = 'in_drive' AS in_drive, hidden_at, created_at
FROM library.creative;

-- One headline the team keeps, cleaned of hidden characters. sha256 is of
-- its text; its first 10 characters end the ad id.
CREATE VIEW library_api.headline_v1 AS
SELECT id, text, sha256, vertical_id, angle, origin, origin_ref, ai_label, made_by, hidden_at, created_at
FROM library.headline;

-- Creatives and headlines made or uploaded together: one brief in Create,
-- one folder in Drive.
CREATE VIEW library_api.set_v1 AS
SELECT id, name, vertical_id, origin, origin_ref, made_by, created_at
FROM library.set;

-- The creatives of each set, in order.
CREATE VIEW library_api.set_creative_v1 AS
SELECT set_id, creative_id, position
FROM library.set_creative;

-- The headlines of each set, in order.
CREATE VIEW library_api.set_headline_v1 AS
SELECT set_id, headline_id, position
FROM library.set_headline;

-- Readers get the published views through the library_api_read role, which
-- the box setup creates and grants to each reading service's login.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'library_api_read') THEN
        GRANT USAGE ON SCHEMA library_api TO library_api_read;
        GRANT SELECT ON ALL TABLES IN SCHEMA library_api TO library_api_read;
    END IF;
END;
$$;
