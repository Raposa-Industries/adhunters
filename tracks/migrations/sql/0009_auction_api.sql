-- Publishes Taboola's auction telemetry (tracks.auction), which Spy sums per
-- day into its auction prices. New view, nothing else changes.

-- Taboola auction telemetry: one row per card that carried auction values
-- (auctionPrice, bval, capAuctionPrice). Kept 14 days, like tracks.auction;
-- older days come back only by a replay. The unit of the prices is not
-- confirmed yet (GLOSSARY: clearing price).
CREATE VIEW tracks_api.auction_v1 AS
SELECT seen_at, scrape_id, ad_id, publisher_id, device_id, auction_id, placement, clearing_price, bid_value,
       cap_auction_price, currency, winning_seat, is_rtb
FROM tracks.auction;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tracks_api_read') THEN
        GRANT SELECT ON tracks_api.auction_v1 TO tracks_api_read;
    END IF;
END;
$$;
