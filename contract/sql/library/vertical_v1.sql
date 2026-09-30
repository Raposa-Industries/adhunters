-- The verticals the library names creatives in: Spy's vertical id, the name
-- people read and the code at the start of each minted name.
CREATE VIEW library_api.vertical_v1 AS
SELECT id, name, code, network_letter
FROM library.vertical;
