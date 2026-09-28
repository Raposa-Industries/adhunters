-- Scrapes per publisher, device and closed hour: the denominator of every rate.
CREATE VIEW tracks_api.scrape_coverage_v1 AS
SELECT hour, publisher_id, device_id, scrapes, answered, failed, sightings
FROM tracks.publisher_hourly;
