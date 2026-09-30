-- The retention curve: per closed hour, the plays that heard each second.
CREATE VIEW funnels_api.video_second_hourly_v1 AS
SELECT hour, video, arm, device, second, watching FROM funnels.video_second_hourly;
