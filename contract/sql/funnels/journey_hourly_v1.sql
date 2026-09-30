-- Journeys per closed hour, by where they began and which ad sent them.
CREATE VIEW funnels_api.journey_hourly_v1 AS
SELECT hour, site, first_lp, sub1, sub4, sub8, device, country, journeys, with_click_id, with_input,
       visible_ms, bots
FROM funnels.journey_hourly;
