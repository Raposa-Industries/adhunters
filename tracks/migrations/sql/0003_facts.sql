-- lint: new-table
-- Facts, one row per thing seen, written only by tracks-loader with COPY.
-- Daily partitions, made ahead by tracks.ensure_day_partitions() and dropped
-- by the loader after their keep time (scrape 35 days, sighting 3, auction
-- 14). Anything dropped is rebuilt from the raw archive by a replay.

CREATE SEQUENCE tracks.scrape_id_seq AS BIGINT;

-- One fetch of one publisher page, on one device, through one proxy line.
-- Errors are scrapes too.
CREATE TABLE tracks.scrape (
    id BIGINT NOT NULL DEFAULT nextval('tracks.scrape_id_seq'),
    capture_id UUID NOT NULL,          -- the raw record's ULID
    at TIMESTAMPTZ NOT NULL,           -- when capture sent the request
    raw_file_id BIGINT NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    proxy_line_id SMALLINT,
    instance TEXT NOT NULL,
    version TEXT,
    outcome TEXT NOT NULL,             -- ok, empty, error, http, unparsed
    status SMALLINT,                   -- HTTP status; 0 when no answer came
    latency_ms INTEGER,
    ad_count SMALLINT NOT NULL,
    geo_country TEXT,
    trc_route TEXT,
    body_sha256 TEXT,
    error TEXT,
    PRIMARY KEY (id, at)
) PARTITION BY RANGE (at);
CREATE UNIQUE INDEX scrape_capture_key ON tracks.scrape (capture_id, at);
CREATE INDEX scrape_raw_file_idx ON tracks.scrape (raw_file_id);
CREATE INDEX scrape_at_brin ON tracks.scrape USING brin (at);

-- One ad seen once in one scrape. account_id and brand_id ride on the
-- sighting, so each hour's counts are one GROUP BY.
CREATE TABLE tracks.sighting (
    seen_at TIMESTAMPTZ NOT NULL,
    scrape_id BIGINT NOT NULL,
    ad_id INTEGER NOT NULL,
    creative_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    placement_id INTEGER,
    campaign_id INTEGER,
    link_id INTEGER,
    account_id INTEGER,
    brand_id INTEGER,
    site_id INTEGER,                   -- Taboola sub-site id from the link
    ecpa_percentile REAL,
    feed_position SMALLINT,
    block_position SMALLINT,
    bid_price REAL,                    -- NewsBreak: the winning bid (USD CPM)
    second_price REAL                  -- NewsBreak: what the ad would pay
) PARTITION BY RANGE (seen_at);
CREATE INDEX sighting_ad_idx ON tracks.sighting (ad_id, seen_at);
-- The hour close reads one hour of a day's partition. The CX43 run showed
-- each count scanning the whole day without it (results-20260927.md).
CREATE INDEX sighting_seen_brin ON tracks.sighting USING brin (seen_at);

-- Taboola auction telemetry: what a card says about the auction it won. Was
-- the collector's legacy adhunters_rtb_auction_log.
CREATE TABLE tracks.auction (
    seen_at TIMESTAMPTZ NOT NULL,
    scrape_id BIGINT NOT NULL,
    ad_id INTEGER NOT NULL,
    publisher_id INTEGER NOT NULL,
    device_id SMALLINT NOT NULL,
    auction_id TEXT NOT NULL,
    placement TEXT,
    clearing_price REAL,
    bid_value REAL,
    cap_auction_price REAL,
    currency TEXT,
    winning_seat TEXT,
    is_rtb BOOLEAN NOT NULL
) PARTITION BY RANGE (seen_at);
CREATE INDEX auction_seen_brin ON tracks.auction USING brin (seen_at);

-- Makes daily partitions <table>_YYYYMMDD of a fact table for [p_from, p_to].
CREATE FUNCTION tracks.ensure_day_partitions(p_table TEXT, p_from DATE, p_to DATE) RETURNS INTEGER
LANGUAGE plpgsql AS $$
DECLARE
    d DATE := p_from;
    made INTEGER := 0;
    part TEXT;
BEGIN
    IF p_table NOT IN ('scrape', 'sighting', 'auction') THEN
        RAISE EXCEPTION 'not a daily fact table: %', p_table;
    END IF;
    WHILE d <= p_to LOOP
        part := p_table || '_' || to_char(d, 'YYYYMMDD');
        IF to_regclass('tracks.' || part) IS NULL THEN
            EXECUTE format('CREATE TABLE tracks.%I PARTITION OF tracks.%I FOR VALUES FROM (%L) TO (%L)',
                           part, p_table, d::timestamp AT TIME ZONE 'UTC', (d + 1)::timestamp AT TIME ZONE 'UTC');
            made := made + 1;
        END IF;
        d := d + 1;
    END LOOP;
    RETURN made;
END;
$$;
