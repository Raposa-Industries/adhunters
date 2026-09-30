-- The headlines of each set, in order.
CREATE VIEW library_api.set_headline_v1 AS
SELECT set_id, headline_id, position
FROM library.set_headline;
