-- One headline the team keeps, cleaned of hidden characters. sha256 is of
-- its text; its first 10 characters end the ad id.
CREATE VIEW library_api.headline_v1 AS
SELECT id, text, sha256, vertical_id, angle, origin, origin_ref, ai_label, made_by, hidden_at, created_at
FROM library.headline;
