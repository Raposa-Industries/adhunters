-- The new layout's tables, as far as the hour close and Direction need them,
-- on a throwaway database. Schema "spy" keeps the collector's names so the
-- copied Direction code (05_direction.sql) runs unchanged.
--
-- A fake clock: every function that calls now() gets clock.now(), because the
-- database's search_path lists pg_catalog last (see bench.sh). It reads the
-- setting bench.now, which bench.sh moves to run Direction at chosen moments
-- of the archived day. A plain expression, so the planner inlines it and it
-- costs what the real now() does.

CREATE SCHEMA IF NOT EXISTS clock;
CREATE FUNCTION clock.now() RETURNS TIMESTAMPTZ LANGUAGE sql STABLE
AS $$ SELECT COALESCE(current_setting('bench.now', true)::timestamptz, pg_catalog.now()) $$;

CREATE SCHEMA IF NOT EXISTS spy;

-- The day as archived by collector-cli db archive-sightings, before parsing.
CREATE UNLOGGED TABLE spy.sighting_archive (
    seen_at TIMESTAMPTZ NOT NULL,
    scrape_id BIGINT NOT NULL,
    ad_id INTEGER NOT NULL,
    creative_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL,
    placement_id INTEGER,
    campaign_id INTEGER,
    link_id INTEGER,
    site_id INTEGER,
    ecpa_percentile REAL,
    device_id SMALLINT NOT NULL,
    feed_position SMALLINT,
    block_position SMALLINT
);

-- Sightings in the new layout: daily partitions, COPY only, one index. The
-- account and brand ride on the sighting, so the hour close is one GROUP BY.
CREATE TABLE spy.sighting (
    seen_at TIMESTAMPTZ NOT NULL,
    scrape_id BIGINT NOT NULL,
    ad_id INTEGER NOT NULL,
    creative_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL,
    placement_id INTEGER,
    campaign_id INTEGER,
    link_id INTEGER,
    site_id INTEGER,
    ecpa_percentile REAL,
    device_id SMALLINT NOT NULL,
    feed_position SMALLINT,
    block_position SMALLINT,
    account_id INTEGER,
    brand_id INTEGER,
    bid_price REAL
) PARTITION BY RANGE (seen_at);
CREATE INDEX ON spy.sighting (ad_id, seen_at);

-- Counts per closed hour, monthly partitions (bench.sh makes them).
CREATE TABLE spy.ad_hourly (
    hour TIMESTAMPTZ NOT NULL,
    ad_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    feed_position_min SMALLINT,
    feed_position_max SMALLINT,
    sightings INTEGER NOT NULL,
    scrapes INTEGER NOT NULL,
    feed_position_sum BIGINT NOT NULL,
    PRIMARY KEY (hour, ad_id, publisher_id, device_id)
) PARTITION BY RANGE (hour);
CREATE INDEX ON spy.ad_hourly (ad_id, hour);

CREATE TABLE spy.ad_account_brand_hourly (
    hour TIMESTAMPTZ NOT NULL,
    ad_id INTEGER NOT NULL,
    account_id INTEGER,
    brand_id INTEGER,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    sightings INTEGER NOT NULL,
    UNIQUE NULLS NOT DISTINCT (hour, ad_id, account_id, brand_id, publisher_id, device_id)
) PARTITION BY RANGE (hour);

-- Scrapes per publisher and device. The archive keeps sightings only, so empty
-- scrapes are missing here: scrapes are counted from the scrapes that had ads.
CREATE TABLE spy.publisher_hourly (
    hour TIMESTAMPTZ NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    scrapes INTEGER NOT NULL,
    sightings INTEGER NOT NULL,
    PRIMARY KEY (hour, publisher_id, device_id)
);

CREATE TABLE spy.ad_daily (
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
CREATE INDEX ON spy.ad_daily (ad_id, day);
CREATE INDEX ON spy.ad_daily (creative_id, day);

CREATE TABLE spy.ad_account_daily (
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
    UNIQUE NULLS NOT DISTINCT (day, ad_id, account_id, brand_id, publisher_id, device_id)
);

-- Lookups, filled by 02_load.sql from the archived ids. Only the columns
-- Direction reads.
CREATE TABLE spy.ad (
    id INTEGER PRIMARY KEY,
    creative_id INTEGER NOT NULL,
    account_id INTEGER,
    brand_id INTEGER,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX ON spy.ad (account_id);
CREATE INDEX ON spy.ad (creative_id);

CREATE TABLE spy.account (
    id INTEGER PRIMARY KEY,
    operator_id INTEGER
);

CREATE TABLE spy.creative_stats (
    creative_id INTEGER PRIMARY KEY,
    is_junk BOOLEAN NOT NULL,
    vertical TEXT -- only so 025's first view compiles; 026's view replaces it
);

CREATE TABLE spy.creative_vertical (
    creative_id INTEGER PRIMARY KEY,
    vertical TEXT
);

CREATE TABLE spy.creative_campaign (
    creative_id INTEGER NOT NULL,
    campaign_id INTEGER NOT NULL,
    sightings BIGINT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (creative_id, campaign_id)
);
CREATE INDEX ON spy.creative_campaign (campaign_id);

-- Every timed step, read back into the results.
CREATE TABLE spy.bench_timing (
    step TEXT NOT NULL,
    detail TEXT,
    ms NUMERIC NOT NULL,
    row_count BIGINT
);

-- Makes monthly partitions of the hourly counts covering [p_from, p_to).
CREATE FUNCTION spy.bench_month_partitions(p_from DATE, p_to DATE) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    m DATE := date_trunc('month', p_from)::date;
    t TEXT;
BEGIN
    WHILE m < p_to LOOP
        FOREACH t IN ARRAY ARRAY['ad_hourly', 'ad_account_brand_hourly'] LOOP
            EXECUTE format('CREATE TABLE IF NOT EXISTS spy.%I PARTITION OF spy.%I FOR VALUES FROM (%L) TO (%L)',
                           t || '_' || to_char(m, 'YYYYMM'), t,
                           (m::timestamp AT TIME ZONE 'UTC'), ((m + interval '1 month')::timestamp AT TIME ZONE 'UTC'));
        END LOOP;
        m := (m + interval '1 month')::date;
    END LOOP;
END;
$$;

-- Makes the daily sighting partition for p_day.
CREATE FUNCTION spy.bench_day_partition(p_day DATE) RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format('CREATE TABLE IF NOT EXISTS spy.%I PARTITION OF spy.sighting FOR VALUES FROM (%L) TO (%L)',
                   'sighting_' || to_char(p_day, 'YYYYMMDD'),
                   (p_day::timestamp AT TIME ZONE 'UTC'), ((p_day + 1)::timestamp AT TIME ZONE 'UTC'));
END;
$$;
