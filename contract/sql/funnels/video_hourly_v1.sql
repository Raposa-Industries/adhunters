-- Each video's loads, plays, watched seconds and pitch per closed hour.
CREATE VIEW funnels_api.video_hourly_v1 AS
SELECT hour, site, video, arm, sub1, sub4, sub8, device, loads, autoplays, plays, watched_s, len_s,
       reached_pitch
FROM funnels.video_hourly;
