-- Creatives and headlines made or uploaded together: one brief in Create,
-- one folder in Drive.
CREATE VIEW library_api.set_v1 AS
SELECT id, name, vertical_id, origin, origin_ref, made_by, created_at
FROM library.set;
