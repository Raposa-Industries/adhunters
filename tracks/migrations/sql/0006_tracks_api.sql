-- The published face of Tracks, version 1 of every view. Each statement is
-- the matching file in contract/sql/tracks/, word for word (a test checks).
-- Other services read these and nothing else in the database.

CREATE SCHEMA tracks_api;

CREATE VIEW tracks_api.network_v1 AS
SELECT id, code, name FROM tracks.network;

CREATE VIEW tracks_api.device_v1 AS
SELECT id, code FROM tracks.device;

CREATE VIEW tracks_api.publisher_v1 AS
SELECT id, network_id, name, domain, first_seen_at, last_seen_at FROM tracks.publisher;

CREATE VIEW tracks_api.placement_v1 AS
SELECT id, name FROM tracks.placement;

CREATE VIEW tracks_api.brand_v1 AS
SELECT id, name FROM tracks.brand;

CREATE VIEW tracks_api.account_v1 AS
SELECT id, network_id, external_id, org_external_id, first_seen_at, last_seen_at FROM tracks.account;

CREATE VIEW tracks_api.campaign_v1 AS
SELECT id, network_id, external_id, name, account_id, parent_external_id, parent_name, objective,
       first_seen_at, last_seen_at
FROM tracks.campaign;

CREATE VIEW tracks_api.creative_v1 AS
SELECT id, creative_key, image_url, format_type, video_duration, thumb_dimensions, language,
       first_seen_at, last_seen_at
FROM tracks.creative;

CREATE VIEW tracks_api.ad_v1 AS
SELECT id, creative_id, headline, description, cta, brand_id, account_id, first_seen_at, last_seen_at
FROM tracks.ad;

CREATE VIEW tracks_api.link_v1 AS
SELECT id, link_key, host, path, item_id, tracker, affiliate_network, params, sample_url,
       first_seen_at, last_seen_at
FROM tracks.link;

CREATE VIEW tracks_api.network_ad_v1 AS
SELECT id, network_id, external_id, ad_id, campaign_id, creative_external_id, name, started_at, icon_url,
       layout, media_aspect, launch_option, iab_tier1, iab_tier2, landing_domain, disclaimer, extra,
       first_seen_at, last_seen_at
FROM tracks.network_ad;

-- The last 3 days only: older sightings live in the raw archive.
CREATE VIEW tracks_api.sighting_v1 AS
SELECT seen_at, scrape_id, ad_id, creative_id, publisher_id, device_id, placement_id, campaign_id, link_id,
       account_id, brand_id, site_id, ecpa_percentile, feed_position, block_position, bid_price, second_price
FROM tracks.sighting;

-- Closed hours and the hours still open, each hour from exactly one side.
-- closed is false for the open hours, which change every 5 minutes.
CREATE VIEW tracks_api.ad_hourly_v1 AS
SELECT hour, ad_id, publisher_id, device_id, sightings, scrapes, feed_position_sum, feed_position_min,
       feed_position_max, first_seen_at, last_seen_at, TRUE AS closed
FROM tracks.ad_hourly
UNION ALL
SELECT hour, ad_id, publisher_id, device_id, sightings, scrapes, feed_position_sum, feed_position_min,
       feed_position_max, first_seen_at, last_seen_at, FALSE AS closed
FROM tracks.ad_hourly_open;

CREATE VIEW tracks_api.ad_account_brand_hourly_v1 AS
SELECT hour, ad_id, account_id, brand_id, publisher_id, device_id, sightings
FROM tracks.ad_account_brand_hourly;

-- Scrapes per publisher, device and closed hour: the denominator of every rate.
CREATE VIEW tracks_api.scrape_coverage_v1 AS
SELECT hour, publisher_id, device_id, scrapes, answered, failed, sightings
FROM tracks.publisher_hourly;

-- Which hours are closed. A dirty hour got a late file and closes again soon.
CREATE VIEW tracks_api.closed_hour_v1 AS
SELECT hour, closed_at, dirty
FROM tracks.hour_state
WHERE closed_at IS NOT NULL;

CREATE VIEW tracks_api.ad_daily_v1 AS
SELECT day, ad_id, publisher_id, device_id, creative_id, sightings, scrapes, feed_position_sum,
       first_seen_at, last_seen_at
FROM tracks.ad_daily;

CREATE VIEW tracks_api.ad_account_daily_v1 AS
SELECT day, ad_id, account_id, brand_id, publisher_id, device_id, creative_id, sightings,
       first_seen_at, last_seen_at
FROM tracks.ad_account_daily;

CREATE VIEW tracks_api.placement_daily_v1 AS
SELECT day, ad_id, publisher_id, placement_id, sightings FROM tracks.placement_daily;

CREATE VIEW tracks_api.campaign_daily_v1 AS
SELECT day, campaign_id, publisher_id, sightings, scrapes, first_seen_at, last_seen_at
FROM tracks.campaign_daily;

CREATE VIEW tracks_api.creative_link_daily_v1 AS
SELECT day, creative_id, link_id, sightings, first_seen_at, last_seen_at FROM tracks.creative_link_daily;

CREATE VIEW tracks_api.creative_campaign_daily_v1 AS
SELECT day, creative_id, campaign_id, sightings, first_seen_at, last_seen_at FROM tracks.creative_campaign_daily;

-- Live links not handed out yet, seen in the last 15 minutes.
CREATE VIEW tracks_api.live_link_v1 AS
SELECT id, host, url, network, campaign_external_id, device, publisher, page_url, ad_id, creative_id, seen_at
FROM tracks.live_link
WHERE taken_at IS NULL AND seen_at > now() - interval '15 minutes';

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

-- Readers get the published views through the tracks_api_read role, which
-- the box setup creates and grants to each reading service's login.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tracks_api_read') THEN
        GRANT USAGE ON SCHEMA tracks_api TO tracks_api_read;
        GRANT SELECT ON ALL TABLES IN SCHEMA tracks_api TO tracks_api_read;
        GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA tracks_api TO tracks_api_read;
    END IF;
END;
$$;
REVOKE EXECUTE ON FUNCTION tracks_api.take_live_link_v1(TEXT, TEXT, TEXT, BOOLEAN) FROM PUBLIC;
