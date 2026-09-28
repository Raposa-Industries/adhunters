-- Hands over the newest unused live link to p_host, and never again. A link
-- of another campaign on the same host is never handed over when a campaign
-- is asked for: it is another ad. A link served to the other device is
-- handed over only with p_any_device, and the asked device is still preferred.
-- Ported from the collector's LinkBook.Take.
CREATE FUNCTION tracks_api.take_live_link_v1(p_host TEXT, p_campaign TEXT, p_device TEXT, p_any_device BOOLEAN)
RETURNS TABLE (url TEXT, campaign_external_id TEXT, device TEXT, publisher TEXT, page_url TEXT, seen_at TIMESTAMPTZ)
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
    UPDATE tracks.live_link l SET taken_at = now()
    WHERE l.id = (
        SELECT c.id FROM tracks.live_link c
        WHERE c.host = regexp_replace(regexp_replace(lower(btrim(p_host)), '^www\.', ''), '^secure\.', '')
          AND c.taken_at IS NULL
          AND c.seen_at > now() - interval '15 minutes'
          AND (COALESCE(p_campaign, '') = '' OR COALESCE(c.campaign_external_id, '') IN ('', p_campaign))
          AND (c.device = p_device OR p_any_device)
        ORDER BY (c.device = p_device) DESC, c.seen_at DESC, c.id DESC
        LIMIT 1
        FOR UPDATE SKIP LOCKED
    )
    RETURNING l.url, l.campaign_external_id, l.device, l.publisher, l.page_url, l.seen_at
$$;
