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
