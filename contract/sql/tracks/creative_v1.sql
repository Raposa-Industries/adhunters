CREATE VIEW tracks_api.creative_v1 AS
SELECT id, creative_key, image_url, format_type, video_duration, thumb_dimensions, language,
       first_seen_at, last_seen_at
FROM tracks.creative;
