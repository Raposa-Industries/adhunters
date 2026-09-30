-- Each step's reach and drop-off per closed hour.
CREATE VIEW funnels_api.step_hourly_v1 AS
SELECT hour, site, lp, step, sub1, sub4, sub8, device, reached, stopped, bots
FROM funnels.step_hourly;
