CREATE VIEW tracks_api.network_ad_v1 AS
SELECT id, network_id, external_id, ad_id, campaign_id, creative_external_id, name, started_at, icon_url,
       layout, media_aspect, launch_option, iab_tier1, iab_tier2, landing_domain, disclaimer, extra,
       first_seen_at, last_seen_at
FROM tracks.network_ad;
