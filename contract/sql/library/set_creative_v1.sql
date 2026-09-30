-- The creatives of each set, in order.
CREATE VIEW library_api.set_creative_v1 AS
SELECT set_id, creative_id, position
FROM library.set_creative;
