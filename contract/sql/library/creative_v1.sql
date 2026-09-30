-- One creative the team keeps. The picture and its thumbnail are served by
-- the library (GET /files/{id}, /thumbs/{id}); sha256 is of its bytes, and
-- its first 10 characters start the ad id. hidden_at set: taken out of the
-- lists by a person, still here for anything that points at it.
CREATE VIEW library_api.creative_v1 AS
SELECT id, name, vertical_id, angle, idea, origin, origin_ref, ai_label, made_by, sha256,
       media_type, width, height, bytes, drive_state = 'in_drive' AS in_drive, hidden_at, created_at
FROM library.creative;
