-- lint: new-table
-- Counts, computed once per closed hour instead of upserted per sighting
-- (decision 0003). Every count is rebuilt from its sightings by deleting and
-- re-inserting its hour or day, so closing again is always safe.
--
-- A closed hour is one whose raw files are all loaded, 5 minutes after it
-- ended. A file that arrives later marks its hours dirty and they close again.

-- Sightings of one ad on one publisher and device in one closed hour.
CREATE TABLE tracks.ad_hourly (
    hour TIMESTAMPTZ NOT NULL,
    ad_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    sightings INTEGER NOT NULL,
    scrapes INTEGER NOT NULL,           -- scrapes the ad appeared in
    feed_position_sum BIGINT NOT NULL,  -- average = feed_position_sum / sightings
    feed_position_min SMALLINT,
    feed_position_max SMALLINT,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (hour, ad_id, publisher_id, device_id)
) PARTITION BY RANGE (hour);
CREATE INDEX ad_hourly_ad_idx ON tracks.ad_hourly (ad_id, hour);

-- The same per account and brand (the collector's 033).
CREATE TABLE tracks.ad_account_brand_hourly (
    hour TIMESTAMPTZ NOT NULL,
    ad_id INTEGER NOT NULL,
    account_id INTEGER,
    brand_id INTEGER,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    sightings INTEGER NOT NULL,
    CONSTRAINT ad_account_brand_hourly_key UNIQUE NULLS NOT DISTINCT (hour, ad_id, account_id, brand_id, publisher_id, device_id)
) PARTITION BY RANGE (hour);

-- How much each publisher was scraped: the denominator of every rate.
CREATE TABLE tracks.publisher_hourly (
    hour TIMESTAMPTZ NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    scrapes INTEGER NOT NULL,           -- every scrape, errors included
    answered INTEGER NOT NULL,          -- outcome ok or empty
    failed INTEGER NOT NULL,            -- outcome error, http or unparsed
    sightings INTEGER NOT NULL,
    PRIMARY KEY (hour, publisher_id, device_id)
);

-- The hours not closed yet (the current one, and the last one for its first
-- minutes), rewritten every 5 minutes so Direction sees the latest minutes.
CREATE TABLE tracks.ad_hourly_open (
    hour TIMESTAMPTZ NOT NULL,
    ad_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    sightings INTEGER NOT NULL,
    scrapes INTEGER NOT NULL,
    feed_position_sum BIGINT NOT NULL,
    feed_position_min SMALLINT,
    feed_position_max SMALLINT,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (hour, ad_id, publisher_id, device_id)
);

-- Counts per UTC day, kept forever. Rebuilt from the day's sightings each
-- time one of its hours closes, so today's rows cover its closed hours.
CREATE TABLE tracks.ad_daily (
    day DATE NOT NULL,
    ad_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    creative_id INTEGER NOT NULL,
    sightings INTEGER NOT NULL,
    scrapes INTEGER NOT NULL,
    feed_position_sum BIGINT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (day, ad_id, publisher_id, device_id)
);
CREATE INDEX ad_daily_ad_idx ON tracks.ad_daily (ad_id, day);
CREATE INDEX ad_daily_creative_idx ON tracks.ad_daily (creative_id, day);

CREATE TABLE tracks.ad_account_daily (
    day DATE NOT NULL,
    ad_id INTEGER NOT NULL,
    account_id INTEGER,
    brand_id INTEGER,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    creative_id INTEGER NOT NULL,
    sightings INTEGER NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT ad_account_daily_key UNIQUE NULLS NOT DISTINCT (day, ad_id, account_id, brand_id, publisher_id, device_id)
);
CREATE INDEX ad_account_daily_account_idx ON tracks.ad_account_daily (account_id, day);

CREATE TABLE tracks.placement_daily (
    day DATE NOT NULL,
    ad_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL,
    placement_id INTEGER NOT NULL,
    sightings INTEGER NOT NULL,
    PRIMARY KEY (day, ad_id, publisher_id, placement_id)
);
CREATE INDEX placement_daily_ad_idx ON tracks.placement_daily (ad_id, day);

CREATE TABLE tracks.campaign_daily (
    day DATE NOT NULL,
    campaign_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL,
    sightings INTEGER NOT NULL,
    scrapes INTEGER NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (day, campaign_id, publisher_id)
);
CREATE INDEX campaign_daily_campaign_idx ON tracks.campaign_daily (campaign_id, day);

-- Which links and campaigns each creative ran with, per day (the collector
-- kept running totals in creative_link and creative_campaign).
CREATE TABLE tracks.creative_link_daily (
    day DATE NOT NULL,
    creative_id INTEGER NOT NULL,
    link_id INTEGER NOT NULL,
    sightings INTEGER NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (day, creative_id, link_id)
);
CREATE INDEX creative_link_daily_link_idx ON tracks.creative_link_daily (link_id, day);

CREATE TABLE tracks.creative_campaign_daily (
    day DATE NOT NULL,
    creative_id INTEGER NOT NULL,
    campaign_id INTEGER NOT NULL,
    sightings INTEGER NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (day, creative_id, campaign_id)
);
CREATE INDEX creative_campaign_daily_campaign_idx ON tracks.creative_campaign_daily (campaign_id, day);

-- Every hour that has loaded scrapes. dirty means its counts are missing or
-- stale; the closer clears it. The numbers are what the close counted, for
-- the books-balance check.
CREATE TABLE tracks.hour_state (
    hour TIMESTAMPTZ PRIMARY KEY,
    dirty BOOLEAN NOT NULL DEFAULT TRUE,
    closed_at TIMESTAMPTZ,
    closes INTEGER NOT NULL DEFAULT 0,
    scrapes BIGINT,
    sightings BIGINT,
    took_ms INTEGER
);
CREATE INDEX hour_state_dirty_idx ON tracks.hour_state (hour) WHERE dirty;

-- Days whose sighting partition was dropped. Their counts are final; a late
-- file for such a day waits for a replay of the whole day.
CREATE TABLE tracks.day_state (
    day DATE PRIMARY KEY,
    sightings_dropped_at TIMESTAMPTZ
);

-- Makes the monthly partitions of the hourly counts covering [p_from, p_to].
CREATE FUNCTION tracks.ensure_month_partitions(p_from DATE, p_to DATE) RETURNS INTEGER
LANGUAGE plpgsql AS $$
DECLARE
    m DATE := date_trunc('month', p_from)::date;
    t TEXT;
    made INTEGER := 0;
BEGIN
    WHILE m <= p_to LOOP
        FOREACH t IN ARRAY ARRAY['ad_hourly', 'ad_account_brand_hourly'] LOOP
            IF to_regclass('tracks.' || t || '_' || to_char(m, 'YYYYMM')) IS NULL THEN
                EXECUTE format('CREATE TABLE tracks.%I PARTITION OF tracks.%I FOR VALUES FROM (%L) TO (%L)',
                               t || '_' || to_char(m, 'YYYYMM'), t,
                               m::timestamp AT TIME ZONE 'UTC', (m + interval '1 month')::timestamp AT TIME ZONE 'UTC');
                made := made + 1;
            END IF;
        END LOOP;
        m := (m + interval '1 month')::date;
    END LOOP;
    RETURN made;
END;
$$;

-- Closes one hour: its three hourly count tables from its sightings and
-- scrapes. Returns the sightings counted. Measured at 4 to 9 s on a CX43 for
-- a real hour (results-20260927.md), before the seen_at index.
CREATE FUNCTION tracks.close_hour(p_hour TIMESTAMPTZ) RETURNS BIGINT
LANGUAGE plpgsql AS $$
DECLARE
    p_end TIMESTAMPTZ := p_hour + interval '1 hour';
    t0 TIMESTAMPTZ := clock_timestamp();
    n_sightings BIGINT;
    n_scrapes BIGINT;
BEGIN
    IF p_hour <> date_trunc('hour', p_hour) THEN
        RAISE EXCEPTION 'not the start of an hour: %', p_hour;
    END IF;
    PERFORM tracks.ensure_month_partitions(p_hour::date, p_hour::date);

    DELETE FROM tracks.ad_hourly WHERE hour = p_hour;
    INSERT INTO tracks.ad_hourly (hour, ad_id, publisher_id, device_id, sightings, scrapes, feed_position_sum,
                                  feed_position_min, feed_position_max, first_seen_at, last_seen_at)
    SELECT p_hour, ad_id, publisher_id, device_id, count(*), count(DISTINCT scrape_id), COALESCE(sum(feed_position), 0),
           min(feed_position), max(feed_position), min(seen_at), max(seen_at)
    FROM tracks.sighting
    WHERE seen_at >= p_hour AND seen_at < p_end
    GROUP BY ad_id, publisher_id, device_id;

    DELETE FROM tracks.ad_account_brand_hourly WHERE hour = p_hour;
    INSERT INTO tracks.ad_account_brand_hourly (hour, ad_id, account_id, brand_id, publisher_id, device_id, sightings)
    SELECT p_hour, ad_id, account_id, brand_id, publisher_id, device_id, count(*)
    FROM tracks.sighting
    WHERE seen_at >= p_hour AND seen_at < p_end
    GROUP BY ad_id, account_id, brand_id, publisher_id, device_id;

    DELETE FROM tracks.publisher_hourly WHERE hour = p_hour;
    INSERT INTO tracks.publisher_hourly (hour, publisher_id, device_id, scrapes, answered, failed, sightings)
    SELECT p_hour, publisher_id, device_id, count(*),
           count(*) FILTER (WHERE outcome IN ('ok', 'empty')),
           count(*) FILTER (WHERE outcome NOT IN ('ok', 'empty')),
           sum(ad_count)
    FROM tracks.scrape
    WHERE at >= p_hour AND at < p_end
    GROUP BY publisher_id, device_id;

    SELECT COALESCE(sum(sightings), 0), COALESCE(sum(scrapes), 0) INTO n_sightings, n_scrapes
    FROM tracks.publisher_hourly WHERE hour = p_hour;

    DELETE FROM tracks.ad_hourly_open WHERE hour = p_hour;
    INSERT INTO tracks.hour_state AS h (hour, dirty, closed_at, closes, scrapes, sightings, took_ms)
    VALUES (p_hour, FALSE, now(), 1, n_scrapes, n_sightings,
            (extract(epoch FROM clock_timestamp() - t0) * 1000)::int)
    ON CONFLICT (hour) DO UPDATE SET dirty = FALSE, closed_at = now(), closes = h.closes + 1,
        scrapes = EXCLUDED.scrapes, sightings = EXCLUDED.sightings, took_ms = EXCLUDED.took_ms;
    RETURN n_sightings;
END;
$$;

-- Rebuilds one day's daily counts from its sightings, up to p_upto (the end
-- of its last closed hour). Returns the rows written.
CREATE FUNCTION tracks.close_day(p_day DATE, p_upto TIMESTAMPTZ) RETURNS BIGINT
LANGUAGE plpgsql AS $$
DECLARE
    d_from TIMESTAMPTZ := p_day::timestamp AT TIME ZONE 'UTC';
    d_to TIMESTAMPTZ := LEAST(p_upto, (p_day + 1)::timestamp AT TIME ZONE 'UTC');
    n BIGINT;
    total BIGINT := 0;
BEGIN
    IF EXISTS (SELECT 1 FROM tracks.day_state WHERE day = p_day AND sightings_dropped_at IS NOT NULL) THEN
        RAISE EXCEPTION 'the sightings of % were dropped; replay the day first', p_day;
    END IF;

    DELETE FROM tracks.ad_daily WHERE day = p_day;
    INSERT INTO tracks.ad_daily (day, ad_id, publisher_id, device_id, creative_id, sightings, scrapes,
                                 feed_position_sum, first_seen_at, last_seen_at)
    SELECT p_day, ad_id, publisher_id, device_id, min(creative_id), count(*), count(DISTINCT scrape_id),
           COALESCE(sum(feed_position), 0), min(seen_at), max(seen_at)
    FROM tracks.sighting WHERE seen_at >= d_from AND seen_at < d_to
    GROUP BY ad_id, publisher_id, device_id;
    GET DIAGNOSTICS n = ROW_COUNT; total := total + n;

    DELETE FROM tracks.ad_account_daily WHERE day = p_day;
    INSERT INTO tracks.ad_account_daily (day, ad_id, account_id, brand_id, publisher_id, device_id, creative_id,
                                         sightings, first_seen_at, last_seen_at)
    SELECT p_day, ad_id, account_id, brand_id, publisher_id, device_id, min(creative_id), count(*),
           min(seen_at), max(seen_at)
    FROM tracks.sighting WHERE seen_at >= d_from AND seen_at < d_to
    GROUP BY ad_id, account_id, brand_id, publisher_id, device_id;
    GET DIAGNOSTICS n = ROW_COUNT; total := total + n;

    DELETE FROM tracks.placement_daily WHERE day = p_day;
    INSERT INTO tracks.placement_daily (day, ad_id, publisher_id, placement_id, sightings)
    SELECT p_day, ad_id, publisher_id, placement_id, count(*)
    FROM tracks.sighting WHERE seen_at >= d_from AND seen_at < d_to AND placement_id IS NOT NULL
    GROUP BY ad_id, publisher_id, placement_id;
    GET DIAGNOSTICS n = ROW_COUNT; total := total + n;

    DELETE FROM tracks.campaign_daily WHERE day = p_day;
    INSERT INTO tracks.campaign_daily (day, campaign_id, publisher_id, sightings, scrapes, first_seen_at, last_seen_at)
    SELECT p_day, campaign_id, publisher_id, count(*), count(DISTINCT scrape_id), min(seen_at), max(seen_at)
    FROM tracks.sighting WHERE seen_at >= d_from AND seen_at < d_to AND campaign_id IS NOT NULL
    GROUP BY campaign_id, publisher_id;
    GET DIAGNOSTICS n = ROW_COUNT; total := total + n;

    DELETE FROM tracks.creative_link_daily WHERE day = p_day;
    INSERT INTO tracks.creative_link_daily (day, creative_id, link_id, sightings, first_seen_at, last_seen_at)
    SELECT p_day, creative_id, link_id, count(*), min(seen_at), max(seen_at)
    FROM tracks.sighting WHERE seen_at >= d_from AND seen_at < d_to AND link_id IS NOT NULL
    GROUP BY creative_id, link_id;
    GET DIAGNOSTICS n = ROW_COUNT; total := total + n;

    DELETE FROM tracks.creative_campaign_daily WHERE day = p_day;
    INSERT INTO tracks.creative_campaign_daily (day, creative_id, campaign_id, sightings, first_seen_at, last_seen_at)
    SELECT p_day, creative_id, campaign_id, count(*), min(seen_at), max(seen_at)
    FROM tracks.sighting WHERE seen_at >= d_from AND seen_at < d_to AND campaign_id IS NOT NULL
    GROUP BY creative_id, campaign_id;
    GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
    RETURN total;
END;
$$;

-- Rewrites ad_hourly_open for every hour from p_from that was never closed.
-- An hour is in ad_hourly or here, never both: a dirty hour that closed
-- before keeps its counts until it closes again.
CREATE FUNCTION tracks.refresh_open_hours(p_from TIMESTAMPTZ) RETURNS BIGINT
LANGUAGE plpgsql AS $$
DECLARE
    n BIGINT;
BEGIN
    DELETE FROM tracks.ad_hourly_open;
    INSERT INTO tracks.ad_hourly_open (hour, ad_id, publisher_id, device_id, sightings, scrapes, feed_position_sum,
                                       feed_position_min, feed_position_max, first_seen_at, last_seen_at)
    SELECT date_trunc('hour', s.seen_at) AS hour, s.ad_id, s.publisher_id, s.device_id, count(*),
           count(DISTINCT s.scrape_id), COALESCE(sum(s.feed_position), 0), min(s.feed_position),
           max(s.feed_position), min(s.seen_at), max(s.seen_at)
    FROM tracks.sighting s
    WHERE s.seen_at >= date_trunc('hour', p_from)
      AND NOT EXISTS (SELECT 1 FROM tracks.hour_state h
                      WHERE h.hour = date_trunc('hour', s.seen_at) AND h.closed_at IS NOT NULL)
    GROUP BY 1, 2, 3, 4;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END;
$$;
