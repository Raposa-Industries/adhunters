-- The last 3 days only: older sightings live in the raw archive.
CREATE VIEW tracks_api.sighting_v1 AS
SELECT seen_at, scrape_id, ad_id, creative_id, publisher_id, device_id, placement_id, campaign_id, link_id,
       account_id, brand_id, site_id, ecpa_percentile, feed_position, block_position, bid_price, second_price
FROM tracks.sighting;
