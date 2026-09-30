-- One journey: one person's way through our landing pages from one tracker
-- click, with where it stopped and what it heard of the videos. Join to
-- RedTrack's conversions by clickid. Only journeys of closed hours.
CREATE VIEW funnels_api.journey_v1 AS
SELECT j.id, j.started_at, j.last_at, j.site, j.first_lp, j.last_lp, j.last_step, j.lps,
       j.clickid, j.sub1, j.sub4, j.sub8, j.subs, j.device, j.country, j.visible_ms, j.max_scroll,
       j.had_input, j.bot_suspect, j.bot_reason,
       COALESCE(v.played, false) AS played, COALESCE(v.watched_s, 0) AS watched_s,
       COALESCE(v.last_s, -1) AS last_s, COALESCE(v.reached_pitch, false) AS reached_pitch
FROM funnels.journey j
LEFT JOIN LATERAL (
    SELECT bool_or(played) AS played, sum(watched_s)::integer AS watched_s, max(last_s) AS last_s,
           bool_or(reached_pitch) AS reached_pitch
    FROM funnels.journey_video jv WHERE jv.journey = j.id
) v ON true;
