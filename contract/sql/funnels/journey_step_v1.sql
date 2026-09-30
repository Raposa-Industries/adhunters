-- The steps each journey reached, in the order it first reached them.
CREATE VIEW funnels_api.journey_step_v1 AS
SELECT journey, seq, lp, step, at FROM funnels.journey_step;
