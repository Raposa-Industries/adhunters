-- The published face of Funnels, version 1. Each statement is the matching
-- file in contract/sql/funnels/, word for word (a test checks). Other
-- services read these and nothing else of Funnels.

CREATE SCHEMA funnels_api;

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

-- The steps each journey reached, in the order it first reached them.
CREATE VIEW funnels_api.journey_step_v1 AS
SELECT journey, seq, lp, step, at FROM funnels.journey_step;

-- Journeys per closed hour, by where they began and which ad sent them.
CREATE VIEW funnels_api.journey_hourly_v1 AS
SELECT hour, site, first_lp, sub1, sub4, sub8, device, country, journeys, with_click_id, with_input,
       visible_ms, bots
FROM funnels.journey_hourly;

-- Each step's reach and drop-off per closed hour.
CREATE VIEW funnels_api.step_hourly_v1 AS
SELECT hour, site, lp, step, sub1, sub4, sub8, device, reached, stopped, bots
FROM funnels.step_hourly;

-- Each video's loads, plays, watched seconds and pitch per closed hour.
CREATE VIEW funnels_api.video_hourly_v1 AS
SELECT hour, site, video, arm, sub1, sub4, sub8, device, loads, autoplays, plays, watched_s, len_s,
       reached_pitch
FROM funnels.video_hourly;

-- The retention curve: per closed hour, the plays that heard each second.
CREATE VIEW funnels_api.video_second_hourly_v1 AS
SELECT hour, video, arm, device, second, watching FROM funnels.video_second_hourly;

-- Hours whose counts are current. An hour closes an hour after it ends and
-- closes again when a late event reaches one of its journeys.
CREATE VIEW funnels_api.closed_hour_v1 AS
SELECT hour, closed_at, journeys FROM funnels.hour_state WHERE closed_at IS NOT NULL AND dirty_since IS NULL;

-- Readers get the published views through the funnels_api_read role, which
-- the box setup creates and grants to each reading service's login.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'funnels_api_read') THEN
        GRANT USAGE ON SCHEMA funnels_api TO funnels_api_read;
        GRANT SELECT ON ALL TABLES IN SCHEMA funnels_api TO funnels_api_read;
    END IF;
END;
$$;
