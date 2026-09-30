-- lint: new-table
-- Auction prices: what the ad networks' answers say an ad paid or bid, per
-- day, ad, publisher and device, kept for good (Tracks keeps its auction rows
-- 14 days and sightings 3). Taboola cards carry the clearing price
-- (auctionPrice), the bid (bval) and a cap (capAuctionPrice) in
-- tracks_api.auction_v1; NewsBreak answers carry the winning bid and the
-- second price on each sighting. Units are as the networks send them; the
-- unit of Taboola's clearing price is not confirmed yet (GLOSSARY).
--
-- Sums are kept beside the medians, so an average over any days is exact;
-- a median over several days is only the sightings-weighted middle of the
-- daily ones, and is named "typical" where it is published.

CREATE TABLE spy.price_day (
    day DATE NOT NULL,
    ad_id INTEGER NOT NULL,
    creative_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    auctions INTEGER NOT NULL,          -- Taboola cards with auction values
    rtb INTEGER NOT NULL,               -- of those, won through RTB
    clearing_n INTEGER NOT NULL,
    clearing_sum DOUBLE PRECISION NOT NULL,
    clearing_p25 REAL,
    clearing_p50 REAL,
    clearing_p75 REAL,
    bid_n INTEGER NOT NULL,             -- Taboola bval, or NewsBreak's winning bid
    bid_sum DOUBLE PRECISION NOT NULL,
    bid_p50 REAL,
    cap_p50 REAL,                       -- Taboola capAuctionPrice
    second_n INTEGER NOT NULL,          -- NewsBreak: what the ad would pay
    second_sum DOUBLE PRECISION NOT NULL,
    second_p50 REAL,
    PRIMARY KEY (day, ad_id, publisher_id, device_id)
);
CREATE INDEX price_day_creative_idx ON spy.price_day (creative_id, day);

-- The newest day summed; days before it are final.
CREATE TABLE spy.price_state (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    done_through DATE NOT NULL,
    refreshed_at TIMESTAMPTZ NOT NULL
);

-- Sums the days from yesterday (in UTC) to today again; the first run goes
-- back as far as Tracks keeps auctions (14 days). A day is redone whole, so
-- a run is safe to repeat. Returns the rows written, 0 when another run
-- holds the lock.
CREATE FUNCTION spy.refresh_prices(p_now TIMESTAMPTZ DEFAULT now()) RETURNS BIGINT
LANGUAGE plpgsql AS $$
DECLARE
    today DATE := (p_now AT TIME ZONE 'UTC')::date;
    since DATE;
    n BIGINT;
BEGIN
    IF NOT pg_try_advisory_xact_lock(hashtext('spy.refresh_prices')) THEN
        RETURN 0;
    END IF;
    SELECT LEAST(done_through, today - 1) INTO since FROM spy.price_state;
    since := COALESCE(since, today - 14);

    DELETE FROM spy.price_day WHERE day >= since;
    WITH tab AS (
        SELECT (a.seen_at AT TIME ZONE 'UTC')::date AS day, a.ad_id, a.publisher_id, a.device_id,
               count(*) AS auctions, count(*) FILTER (WHERE a.is_rtb) AS rtb,
               count(a.clearing_price) AS clearing_n, COALESCE(sum(a.clearing_price), 0) AS clearing_sum,
               percentile_cont(0.25) WITHIN GROUP (ORDER BY a.clearing_price) AS clearing_p25,
               percentile_cont(0.5) WITHIN GROUP (ORDER BY a.clearing_price) AS clearing_p50,
               percentile_cont(0.75) WITHIN GROUP (ORDER BY a.clearing_price) AS clearing_p75,
               count(a.bid_value) AS bid_n, COALESCE(sum(a.bid_value), 0) AS bid_sum,
               percentile_cont(0.5) WITHIN GROUP (ORDER BY a.bid_value) AS bid_p50,
               percentile_cont(0.5) WITHIN GROUP (ORDER BY a.cap_auction_price) AS cap_p50
        FROM tracks_api.auction_v1 a
        WHERE a.seen_at >= since::timestamp AT TIME ZONE 'UTC'
          AND a.seen_at < (today + 1)::timestamp AT TIME ZONE 'UTC'
        GROUP BY 1, 2, 3, 4
    ),
    nb AS (
        SELECT (s.seen_at AT TIME ZONE 'UTC')::date AS day, s.ad_id, s.publisher_id, s.device_id,
               count(s.bid_price) AS bid_n, COALESCE(sum(s.bid_price), 0) AS bid_sum,
               percentile_cont(0.5) WITHIN GROUP (ORDER BY s.bid_price) AS bid_p50,
               count(s.second_price) AS second_n, COALESCE(sum(s.second_price), 0) AS second_sum,
               percentile_cont(0.5) WITHIN GROUP (ORDER BY s.second_price) AS second_p50
        FROM tracks_api.sighting_v1 s
        WHERE s.seen_at >= since::timestamp AT TIME ZONE 'UTC'
          AND s.seen_at < (today + 1)::timestamp AT TIME ZONE 'UTC'
          AND (s.bid_price IS NOT NULL OR s.second_price IS NOT NULL)
        GROUP BY 1, 2, 3, 4
    )
    INSERT INTO spy.price_day
    SELECT COALESCE(t.day, b.day), COALESCE(t.ad_id, b.ad_id), ad.creative_id,
           COALESCE(t.publisher_id, b.publisher_id), COALESCE(t.device_id, b.device_id),
           COALESCE(t.auctions, 0), COALESCE(t.rtb, 0), COALESCE(t.clearing_n, 0), COALESCE(t.clearing_sum, 0),
           t.clearing_p25, t.clearing_p50, t.clearing_p75,
           COALESCE(t.bid_n, 0) + COALESCE(b.bid_n, 0), COALESCE(t.bid_sum, 0) + COALESCE(b.bid_sum, 0),
           COALESCE(t.bid_p50, b.bid_p50), t.cap_p50,
           COALESCE(b.second_n, 0), COALESCE(b.second_sum, 0), b.second_p50
    FROM tab t
    FULL JOIN nb b ON b.day = t.day AND b.ad_id = t.ad_id AND b.publisher_id = t.publisher_id AND b.device_id = t.device_id
    JOIN tracks_api.ad_v1 ad ON ad.id = COALESCE(t.ad_id, b.ad_id);
    GET DIAGNOSTICS n = ROW_COUNT;

    INSERT INTO spy.price_state (done_through, refreshed_at) VALUES (today, now())
    ON CONFLICT (id) DO UPDATE SET done_through = EXCLUDED.done_through, refreshed_at = EXCLUDED.refreshed_at;
    RETURN GREATEST(n, 1);
END;
$$;

-- Published: the daily sums, per ad, publisher and device.
CREATE VIEW spy_api.price_day_v1 AS
SELECT day, ad_id, creative_id, publisher_id, device_id, auctions, rtb, clearing_n, clearing_sum, clearing_p25,
       clearing_p50, clearing_p75, bid_n, bid_sum, bid_p50, cap_p50, second_n, second_sum, second_p50
FROM spy.price_day;

-- Published: each creative's prices over whole UTC days [p_from, p_to],
-- per network. Averages are exact; "typical" is the weighted middle of the
-- daily medians.
CREATE FUNCTION spy_api.creative_prices_v1(p_from DATE, p_to DATE)
RETURNS TABLE (creative_id INTEGER, network_id INTEGER, days INTEGER, auctions BIGINT, rtb BIGINT,
               clearing_n BIGINT, clearing_avg DOUBLE PRECISION, clearing_typical DOUBLE PRECISION,
               bid_n BIGINT, bid_avg DOUBLE PRECISION, bid_typical DOUBLE PRECISION,
               second_n BIGINT, second_avg DOUBLE PRECISION)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
    SELECT p.creative_id, pub.network_id::int, count(DISTINCT p.day)::int, sum(p.auctions), sum(p.rtb),
           sum(p.clearing_n), sum(p.clearing_sum) / NULLIF(sum(p.clearing_n), 0),
           sum(p.clearing_p50 * p.clearing_n) / NULLIF(sum(p.clearing_n) FILTER (WHERE p.clearing_p50 IS NOT NULL), 0),
           sum(p.bid_n), sum(p.bid_sum) / NULLIF(sum(p.bid_n), 0),
           sum(p.bid_p50 * p.bid_n) / NULLIF(sum(p.bid_n) FILTER (WHERE p.bid_p50 IS NOT NULL), 0),
           sum(p.second_n), sum(p.second_sum) / NULLIF(sum(p.second_n), 0)
    FROM spy.price_day p
    JOIN tracks_api.publisher_v1 pub ON pub.id = p.publisher_id
    WHERE p.day BETWEEN p_from AND p_to
    GROUP BY 1, 2
$$;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'spy_api_read') THEN
        GRANT SELECT ON spy_api.price_day_v1 TO spy_api_read;
        GRANT EXECUTE ON FUNCTION spy_api.creative_prices_v1(DATE, DATE) TO spy_api_read;
    END IF;
END;
$$;
