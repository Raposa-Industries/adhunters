-- The hour close as the design has it: once every raw file of hour H is
-- loaded, one INSERT … SELECT … GROUP BY per count table computes H from its
-- sightings. Deleting the hour's rows first makes it safe to run again.
-- The day close sums the day's closed hours into the daily counts.

CREATE FUNCTION spy.close_hour(p_hour TIMESTAMPTZ) RETURNS BIGINT LANGUAGE plpgsql AS $$
DECLARE
    t0 TIMESTAMPTZ;
    n BIGINT;
    total BIGINT := 0;
    p_end TIMESTAMPTZ := p_hour + interval '1 hour';
BEGIN
    t0 := clock_timestamp();
    DELETE FROM spy.ad_hourly WHERE hour = p_hour;
    INSERT INTO spy.ad_hourly (hour, ad_id, publisher_id, device_id, feed_position_min, feed_position_max,
                               sightings, scrapes, feed_position_sum)
    SELECT p_hour, ad_id, publisher_id, device_id, min(feed_position), max(feed_position),
           count(*), count(DISTINCT scrape_id), COALESCE(sum(feed_position), 0)
    FROM spy.sighting
    WHERE seen_at >= p_hour AND seen_at < p_end
    GROUP BY ad_id, publisher_id, device_id;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;
    INSERT INTO spy.bench_timing VALUES ('close_hour.ad_hourly', p_hour::text,
        extract(epoch FROM clock_timestamp() - t0) * 1000, n);

    t0 := clock_timestamp();
    DELETE FROM spy.ad_account_brand_hourly WHERE hour = p_hour;
    INSERT INTO spy.ad_account_brand_hourly (hour, ad_id, account_id, brand_id, publisher_id, device_id, sightings)
    SELECT p_hour, ad_id, account_id, brand_id, publisher_id, device_id, count(*)
    FROM spy.sighting
    WHERE seen_at >= p_hour AND seen_at < p_end
    GROUP BY ad_id, account_id, brand_id, publisher_id, device_id;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;
    INSERT INTO spy.bench_timing VALUES ('close_hour.ad_account_brand_hourly', p_hour::text,
        extract(epoch FROM clock_timestamp() - t0) * 1000, n);

    t0 := clock_timestamp();
    DELETE FROM spy.publisher_hourly WHERE hour = p_hour;
    INSERT INTO spy.publisher_hourly (hour, publisher_id, device_id, scrapes, sightings)
    SELECT p_hour, publisher_id, device_id, count(DISTINCT scrape_id), count(*)
    FROM spy.sighting
    WHERE seen_at >= p_hour AND seen_at < p_end
    GROUP BY publisher_id, device_id;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;
    INSERT INTO spy.bench_timing VALUES ('close_hour.publisher_hourly', p_hour::text,
        extract(epoch FROM clock_timestamp() - t0) * 1000, n);
    RETURN total;
END;
$$;

CREATE FUNCTION spy.close_day(p_day DATE) RETURNS BIGINT LANGUAGE plpgsql AS $$
DECLARE
    t0 TIMESTAMPTZ := clock_timestamp();
    n BIGINT;
    total BIGINT := 0;
    d_from TIMESTAMPTZ := p_day::timestamp AT TIME ZONE 'UTC';
    d_to TIMESTAMPTZ := (p_day + 1)::timestamp AT TIME ZONE 'UTC';
BEGIN
    DELETE FROM spy.ad_daily WHERE day = p_day;
    INSERT INTO spy.ad_daily (day, ad_id, publisher_id, device_id, creative_id, sightings, scrapes,
                              feed_position_sum, first_seen_at, last_seen_at)
    SELECT p_day, h.ad_id, h.publisher_id, h.device_id, a.creative_id, sum(h.sightings), sum(h.scrapes),
           sum(h.feed_position_sum), min(h.hour), max(h.hour) + interval '1 hour'
    FROM spy.ad_hourly h JOIN spy.ad a ON a.id = h.ad_id
    WHERE h.hour >= d_from AND h.hour < d_to
    GROUP BY h.ad_id, h.publisher_id, h.device_id, a.creative_id;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;

    DELETE FROM spy.ad_account_daily WHERE day = p_day;
    INSERT INTO spy.ad_account_daily (day, ad_id, account_id, brand_id, publisher_id, device_id, creative_id,
                                      sightings, first_seen_at, last_seen_at)
    SELECT p_day, h.ad_id, h.account_id, h.brand_id, h.publisher_id, h.device_id, a.creative_id, sum(h.sightings),
           min(h.hour), max(h.hour) + interval '1 hour'
    FROM spy.ad_account_brand_hourly h JOIN spy.ad a ON a.id = h.ad_id
    WHERE h.hour >= d_from AND h.hour < d_to
    GROUP BY h.ad_id, h.account_id, h.brand_id, h.publisher_id, h.device_id, a.creative_id;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;
    INSERT INTO spy.bench_timing VALUES ('close_day', p_day::text, extract(epoch FROM clock_timestamp() - t0) * 1000, total);
    RETURN total;
END;
$$;
