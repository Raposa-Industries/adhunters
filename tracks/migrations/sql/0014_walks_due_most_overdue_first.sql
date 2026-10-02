-- The walker takes the ads it walked before most overdue first, not the
-- most recently seen first. Ads that run on many pages are seen every few
-- minutes and always sorted to the top, so when the walker was busy an ad
-- seen once an hour waited: on 2 Oct 2026 1,532 ads seen in the last hour
-- were due again, the median 141 minutes overdue and the longest 27 hours.
-- Never walked ads still go first, the newest first. Otherwise as in 0010.
CREATE OR REPLACE FUNCTION tracks.walks_due(p_limit INTEGER, p_now TIMESTAMPTZ DEFAULT now())
RETURNS TABLE (ad_id INTEGER, creative_id INTEGER, account_id INTEGER, link_id INTEGER, url TEXT,
               publisher_id INTEGER, referer TEXT, seen_at TIMESTAMPTZ)
LANGUAGE sql STABLE AS $$
    SELECT s.ad_id, s.creative_id, s.account_id, s.link_id, l.sample_url, s.publisher_id,
           CASE WHEN p.domain IS NOT NULL THEN 'https://' || p.domain || '/' END, s.seen_at
    FROM (SELECT DISTINCT ON (x.ad_id) x.ad_id, x.creative_id, x.account_id, x.link_id, x.publisher_id, x.seen_at
          FROM tracks.sighting x
          WHERE x.seen_at > p_now - interval '1 hour' AND x.seen_at <= p_now AND x.link_id IS NOT NULL
          ORDER BY x.ad_id, x.seen_at DESC) s
    JOIN tracks.link l ON l.id = s.link_id
    LEFT JOIN tracks.publisher p ON p.id = s.publisher_id
    LEFT JOIN tracks.walk_state w ON w.ad_id = s.ad_id
    WHERE (w.ad_id IS NULL OR w.next_at <= p_now) AND l.sample_url ~ '^https?://'
    ORDER BY w.ad_id IS NOT NULL, w.next_at, s.seen_at DESC
    LIMIT p_limit
$$;
