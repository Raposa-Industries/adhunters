-- lint: new-table
-- What the answers say, one table per thing, filled by intel-numbers from
-- intel.answer. Every row carries the fetched_at of the answer it came from;
-- a newer answer replaces an older one, an older one never replaces a newer
-- (reports change for days after the fact: clicks for 48 hours, conversions
-- for 30 days, billing until the 5th of the next month).
--
-- Days are whole days in the Taboola account's time zone, on both sides:
-- RedTrack is asked in that time zone, so its days line up.
-- Ids are Taboola's: campaign, item and site ids are numbers, unique across
-- accounts. RedTrack's sub slots stay text as it sent them (sub1 = campaign,
-- sub4 = item, sub8 = site with Taboola's preset); the joins cast.

-- Settings Intel can change without a deploy.
CREATE TABLE intel.setting (
    name TEXT PRIMARY KEY,
    value NUMERIC NOT NULL,
    note TEXT NOT NULL
);

-- Taboola accounts the keys can read.
CREATE TABLE intel.tb_account (
    account TEXT PRIMARY KEY,
    login TEXT NOT NULL,
    numeric_id BIGINT,
    name TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL DEFAULT '',
    currency TEXT NOT NULL DEFAULT '',
    time_zone TEXT NOT NULL DEFAULT '',
    fetched_at TIMESTAMPTZ NOT NULL
);

-- Each campaign as last seen, and every version of its settings. Taboola
-- forgets a deleted campaign (it drops out of every list and answers "not
-- found"), so gone_at marks when it stopped being listed and the settings
-- stay here.
CREATE TABLE intel.tb_campaign (
    campaign_id BIGINT PRIMARY KEY,
    account TEXT NOT NULL,
    group_id BIGINT,
    name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    is_active BOOLEAN NOT NULL DEFAULT FALSE,
    bid_strategy TEXT NOT NULL DEFAULT '',
    cpc NUMERIC,
    daily_cap NUMERIC,
    spending_limit NUMERIC,
    platforms TEXT[] NOT NULL DEFAULT '{}',
    traffic_allocation_mode TEXT NOT NULL DEFAULT '',
    settings JSONB NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    gone_at TIMESTAMPTZ
);
CREATE INDEX ON intel.tb_campaign (account, group_id);

CREATE TABLE intel.tb_campaign_version (
    campaign_id BIGINT NOT NULL,
    valid_from TIMESTAMPTZ NOT NULL,
    settings JSONB NOT NULL,
    PRIMARY KEY (campaign_id, valid_from)
);

-- Each item (one ad in one campaign) as last seen, and every version: a
-- headline or image can change while the item keeps its id.
CREATE TABLE intel.tb_item (
    item_id BIGINT PRIMARY KEY,
    campaign_id BIGINT NOT NULL,
    account TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    thumbnail_url TEXT NOT NULL DEFAULT '',
    -- Our ad id, when Launch or Create made the item (Taboola's custom id).
    custom_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    is_active BOOLEAN NOT NULL DEFAULT FALSE,
    approval_state TEXT NOT NULL DEFAULT '',
    reject_reason TEXT NOT NULL DEFAULT '',
    settings JSONB NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    gone_at TIMESTAMPTZ
);
CREATE INDEX ON intel.tb_item (campaign_id);

CREATE TABLE intel.tb_item_version (
    item_id BIGINT NOT NULL,
    valid_from TIMESTAMPTZ NOT NULL,
    title TEXT NOT NULL,
    thumbnail_url TEXT NOT NULL,
    url TEXT NOT NULL,
    status TEXT NOT NULL,
    approval_state TEXT NOT NULL,
    settings JSONB NOT NULL,
    PRIMARY KEY (item_id, valid_from)
);

-- Taboola's daily numbers. conversions is Taboola's own count (its pixel or
-- the postback), which RedTrack's can be checked against.
CREATE TABLE intel.tb_campaign_day (
    campaign_id BIGINT NOT NULL,
    day DATE NOT NULL,
    account TEXT NOT NULL,
    impressions BIGINT NOT NULL,
    visible_impressions BIGINT NOT NULL,
    clicks BIGINT NOT NULL,
    spent NUMERIC(14, 4) NOT NULL,
    conversions BIGINT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (campaign_id, day)
);
CREATE INDEX ON intel.tb_campaign_day (account, day);

CREATE TABLE intel.tb_site_day (
    campaign_id BIGINT NOT NULL,
    site_id BIGINT NOT NULL,
    day DATE NOT NULL,
    account TEXT NOT NULL,
    site TEXT NOT NULL DEFAULT '',
    site_name TEXT NOT NULL DEFAULT '',
    impressions BIGINT NOT NULL,
    visible_impressions BIGINT NOT NULL,
    clicks BIGINT NOT NULL,
    spent NUMERIC(14, 4) NOT NULL,
    conversions BIGINT NOT NULL,
    blocking_level TEXT NOT NULL DEFAULT '',
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (campaign_id, day, site_id)
);

-- Items of deleted campaigns come back with no item id; their old version id
-- stands in (old_version = true).
CREATE TABLE intel.tb_item_day (
    item_id BIGINT NOT NULL,
    day DATE NOT NULL,
    campaign_id BIGINT NOT NULL,
    account TEXT NOT NULL,
    old_version BOOLEAN NOT NULL DEFAULT FALSE,
    title TEXT NOT NULL DEFAULT '',
    thumbnail_url TEXT NOT NULL DEFAULT '',
    custom_id TEXT NOT NULL DEFAULT '',
    impressions BIGINT NOT NULL,
    visible_impressions BIGINT NOT NULL,
    clicks BIGINT NOT NULL,
    spent NUMERIC(14, 4) NOT NULL,
    conversions BIGINT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (item_id, day)
);
CREATE INDEX ON intel.tb_item_day (campaign_id, day);

-- Taboola's realtime numbers per campaign in 5-minute buckets (bucket is
-- the bucket's start). Not for billing; the alerts run on them.
CREATE TABLE intel.tb_bucket (
    campaign_id BIGINT NOT NULL,
    bucket TIMESTAMPTZ NOT NULL,
    account TEXT NOT NULL,
    visible_impressions BIGINT NOT NULL,
    clicks BIGINT NOT NULL,
    spent NUMERIC(14, 4) NOT NULL,
    conversions BIGINT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (campaign_id, bucket)
);

-- RedTrack's numbers, per the Taboola ids in its sub slots, per day in the
-- time zone it was asked in. One RedTrack login should see each Taboola
-- campaign; if two do, both rows are kept and the joins add them.
CREATE TABLE intel.rt_item_day (
    login TEXT NOT NULL,
    time_zone TEXT NOT NULL,
    day DATE NOT NULL,
    sub1 TEXT NOT NULL,
    sub4 TEXT NOT NULL,
    clicks BIGINT NOT NULL,
    lp_views BIGINT NOT NULL,
    lp_clicks BIGINT NOT NULL,
    conversions BIGINT NOT NULL,
    revenue NUMERIC(14, 4) NOT NULL,
    cost NUMERIC(14, 4) NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (login, time_zone, day, sub1, sub4)
);
CREATE INDEX ON intel.rt_item_day (sub1, day);

CREATE TABLE intel.rt_site_day (
    login TEXT NOT NULL,
    time_zone TEXT NOT NULL,
    day DATE NOT NULL,
    sub1 TEXT NOT NULL,
    sub8 TEXT NOT NULL,
    clicks BIGINT NOT NULL,
    lp_views BIGINT NOT NULL,
    lp_clicks BIGINT NOT NULL,
    conversions BIGINT NOT NULL,
    revenue NUMERIC(14, 4) NOT NULL,
    cost NUMERIC(14, 4) NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (login, time_zone, day, sub1, sub8)
);
CREATE INDEX ON intel.rt_site_day (sub1, day);

-- Per campaign per hour of the day, for the tracking and landing page gaps.
CREATE TABLE intel.rt_campaign_hour (
    login TEXT NOT NULL,
    time_zone TEXT NOT NULL,
    day DATE NOT NULL,
    hour SMALLINT NOT NULL,
    sub1 TEXT NOT NULL,
    clicks BIGINT NOT NULL,
    lp_views BIGINT NOT NULL,
    lp_clicks BIGINT NOT NULL,
    conversions BIGINT NOT NULL,
    revenue NUMERIC(14, 4) NOT NULL,
    cost NUMERIC(14, 4) NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (login, time_zone, day, hour, sub1)
);

-- One row per conversion. RedTrack's row shape is not confirmed on real
-- traffic yet, so the row is kept whole and the known fields read out of it.
CREATE TABLE intel.rt_conversion (
    login TEXT NOT NULL,
    conversion_id TEXT NOT NULL,
    click_id TEXT NOT NULL DEFAULT '',
    sub1 TEXT NOT NULL DEFAULT '',
    sub4 TEXT NOT NULL DEFAULT '',
    sub8 TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    payout NUMERIC(14, 4),
    clicked_at TIMESTAMPTZ,
    converted_at TIMESTAMPTZ,
    row JSONB NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (login, conversion_id)
);
CREATE INDEX ON intel.rt_conversion (sub1, converted_at);

-- A campaign Launch copied into another group gets a new id (seen
-- 2026-09-30: Taboola refuses to change a campaign's group). Each row says
-- the new campaign continues the old one, so its history carries on.
CREATE TABLE intel.campaign_link (
    old_campaign_id BIGINT NOT NULL,
    new_campaign_id BIGINT NOT NULL,
    account TEXT NOT NULL,
    linked_at TIMESTAMPTZ NOT NULL,
    -- 'launch': read from launch_api; 'person': set by hand in Intel.
    source TEXT NOT NULL,
    PRIMARY KEY (old_campaign_id, new_campaign_id)
);
CREATE INDEX ON intel.campaign_link (new_campaign_id);

-- The whole line a campaign belongs to: itself and every campaign it was
-- copied from, back to the first. root_id is the first one.
CREATE VIEW intel.campaign_line AS
WITH RECURSIVE up AS (
    SELECT c.campaign_id, c.campaign_id AS ancestor_id, 0 AS depth
    FROM intel.tb_campaign c
    UNION ALL
    SELECT up.campaign_id, l.old_campaign_id, up.depth + 1
    FROM up JOIN intel.campaign_link l ON l.new_campaign_id = up.ancestor_id
    WHERE up.depth < 50
)
SELECT campaign_id, ancestor_id, depth,
       first_value(ancestor_id) OVER (PARTITION BY campaign_id ORDER BY depth DESC) AS root_id
FROM up;
