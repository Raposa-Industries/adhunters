-- Direction compares now with the same hour 1 to 3 weeks back and reads 21 to
-- 28 days of counts. One archived day has none of that, so the closed day is
-- copied back p_days days: each copy keeps about 85% of the rows, scales each
-- count by 0.7 to 1.3 (and by 1/0.85, so totals stay level), and leaves out
-- ads not yet first seen at that hour.
-- The counts are made up; their volume and shape (the working set) are not.

CREATE FUNCTION spy.bench_history(p_day DATE, p_days INTEGER) RETURNS BIGINT LANGUAGE plpgsql AS $$
DECLARE
    d_from TIMESTAMPTZ := p_day::timestamp AT TIME ZONE 'UTC';
    d_to TIMESTAMPTZ := (p_day + 1)::timestamp AT TIME ZONE 'UTC';
    total BIGINT := 0;
    n BIGINT;
    k INTEGER;
BEGIN
    PERFORM setseed(0.42);
    FOR k IN 1..p_days LOOP
        INSERT INTO spy.ad_hourly (hour, ad_id, publisher_id, device_id, feed_position_min, feed_position_max,
                                   sightings, scrapes, feed_position_sum)
        SELECT h.hour - make_interval(days => k), h.ad_id, h.publisher_id, h.device_id,
               h.feed_position_min, h.feed_position_max,
               GREATEST(1, round(h.sightings * (0.7 + random() * 0.6) / 0.85))::int,
               GREATEST(1, round(h.scrapes * (0.7 + random() * 0.6) / 0.85))::int,
               h.feed_position_sum
        FROM spy.ad_hourly h
        JOIN spy.ad a ON a.id = h.ad_id
        WHERE h.hour >= d_from AND h.hour < d_to
          AND a.first_seen_at <= h.hour - make_interval(days => k)
          AND random() < 0.85;
        GET DIAGNOSTICS n = ROW_COUNT;
        total := total + n;

        INSERT INTO spy.publisher_hourly (hour, publisher_id, device_id, scrapes, sightings)
        SELECT hour - make_interval(days => k), publisher_id, device_id,
               GREATEST(1, round(scrapes * (0.9 + random() * 0.2)))::int,
               round(sightings * (0.9 + random() * 0.2))::int
        FROM spy.publisher_hourly
        WHERE hour >= d_from AND hour < d_to;

        INSERT INTO spy.ad_daily (day, ad_id, publisher_id, device_id, creative_id, sightings, scrapes,
                                  feed_position_sum, first_seen_at, last_seen_at)
        SELECT p_day - k, h.ad_id, h.publisher_id, h.device_id, a.creative_id, sum(h.sightings), sum(h.scrapes),
               sum(h.feed_position_sum), min(h.hour), max(h.hour) + interval '1 hour'
        FROM spy.ad_hourly h JOIN spy.ad a ON a.id = h.ad_id
        WHERE h.hour >= d_from - make_interval(days => k) AND h.hour < d_to - make_interval(days => k)
        GROUP BY h.ad_id, h.publisher_id, h.device_id, a.creative_id;
    END LOOP;
    RETURN total;
END;
$$;
