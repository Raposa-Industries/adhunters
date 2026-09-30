-- lint: new-table
-- What Intel concludes: results per ad and campaign with likely ranges,
-- alerts, and suggestions. intel-numbers rebuilds results and re-judges
-- alerts and suggestions every few minutes; people's answers to suggestions
-- (not now, done in Launch) are kept.

INSERT INTO intel.setting (name, value, note) VALUES
    ('usual_days', 30, 'Days of history that set an account''s usual cost per sale.'),
    ('sale_delay_minutes', 60, 'Spend this recent is not expected to have its sales in yet (until the conversions log measures it).'),
    ('runaway_odds', 0.02, 'Runaway: alert when a normal ad would get this far without a sale less often than this (1 in 50).'),
    ('runaway_min_spend', 20, 'Runaway: never alert below this much spend with no sale (account currency).'),
    ('pause_odds', 0.05, 'Suggest pausing ads that a normal ad would reach with no sale less often than this (1 in 20).'),
    ('pause_min_spend', 10, 'Suggest pausing an ad only past this much spend with no sale.'),
    ('tracking_gap_ratio', 0.5, 'Tracking gap: tracker clicks under this share of the network''s clicks in the last full hour.'),
    ('tracking_gap_min_clicks', 30, 'Tracking gap: judged only when the network counted at least this many clicks in the hour.'),
    ('page_gap_ratio', 0.3, 'Landing page gap: page views under this share of tracker clicks in the last full hour.'),
    ('page_gap_min_clicks', 30, 'Landing page gap: judged only past this many tracker clicks in the hour.'),
    ('postback_gap_min_sales', 3, 'Postback gap: judged only when the tracker counted at least this many sales yesterday.'),
    ('postback_gap_ratio', 0.5, 'Postback gap: the network counted under this share of the tracker''s sales.'),
    ('range_level', 0.9, 'How sure a likely range is (0.9: the true value is inside 9 times in 10).'),
    ('prior_strength', 200, 'Clicks'' worth of weight a small ad borrows from its campaign when its rates are judged.');

-- Results per ad (Taboola item), over a few fixed windows of whole days in
-- the account's time zone: today (so far), yesterday, 7d and 30d (both
-- ending today). Taboola gives impressions and spend, RedTrack everything
-- after the click. Rates carry a likely range at range_level.
CREATE TABLE intel.ad_result (
    item_id BIGINT NOT NULL,
    time_window TEXT NOT NULL CHECK (time_window IN ('today', 'yesterday', '7d', '30d')),
    campaign_id BIGINT NOT NULL,
    account TEXT NOT NULL,
    impressions BIGINT NOT NULL,
    clicks BIGINT NOT NULL,
    spent NUMERIC(14, 4) NOT NULL,
    tracker_clicks BIGINT NOT NULL,
    lp_views BIGINT NOT NULL,
    lp_clicks BIGINT NOT NULL,
    sales BIGINT NOT NULL,
    revenue NUMERIC(14, 4) NOT NULL,
    profit NUMERIC(14, 4) NOT NULL,
    ctr DOUBLE PRECISION, ctr_low DOUBLE PRECISION, ctr_high DOUBLE PRECISION,
    lp_click_rate DOUBLE PRECISION, lp_click_rate_low DOUBLE PRECISION, lp_click_rate_high DOUBLE PRECISION,
    sale_rate DOUBLE PRECISION, sale_rate_low DOUBLE PRECISION, sale_rate_high DOUBLE PRECISION,
    order_value DOUBLE PRECISION,
    profit_per_1000 DOUBLE PRECISION, profit_per_1000_low DOUBLE PRECISION, profit_per_1000_high DOUBLE PRECISION,
    -- measured: from its own sales; estimated: its click and sale steps with
    -- the sale rate borrowed from its campaign while sales are few.
    profit_basis TEXT NOT NULL DEFAULT '',
    -- Against its campaign, on profit per 1,000 impressions, in Spy's words:
    -- better, worse, usual (the range sits within 20% of the campaign's),
    -- unclear ("can't tell yet") or too_little ("too little data").
    word TEXT NOT NULL CHECK (word IN ('better', 'worse', 'usual', 'unclear', 'too_little')),
    -- clear: the whole likely range is on that side; likely: the point and
    -- most of the range are. Empty for usual and too_little.
    sureness TEXT NOT NULL DEFAULT '' CHECK (sureness IN ('', 'clear', 'likely')),
    -- About how much more spend would tell it from its campaign, for unclear.
    spend_to_tell NUMERIC(14, 2),
    refreshed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (item_id, time_window)
);
CREATE INDEX ON intel.ad_result (campaign_id, time_window);

CREATE TABLE intel.campaign_result (
    campaign_id BIGINT NOT NULL,
    time_window TEXT NOT NULL CHECK (time_window IN ('today', 'yesterday', '7d', '30d')),
    account TEXT NOT NULL,
    impressions BIGINT NOT NULL,
    clicks BIGINT NOT NULL,
    spent NUMERIC(14, 4) NOT NULL,
    network_sales BIGINT NOT NULL,
    tracker_clicks BIGINT NOT NULL,
    lp_views BIGINT NOT NULL,
    lp_clicks BIGINT NOT NULL,
    sales BIGINT NOT NULL,
    revenue NUMERIC(14, 4) NOT NULL,
    profit NUMERIC(14, 4) NOT NULL,
    roi DOUBLE PRECISION,
    cost_per_sale DOUBLE PRECISION, cost_per_sale_low DOUBLE PRECISION, cost_per_sale_high DOUBLE PRECISION,
    -- tracker: sales and revenue come from RedTrack; network: no tracker rows
    -- for this campaign, so sales are Taboola's count and revenue unknown.
    sales_source TEXT NOT NULL,
    refreshed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (campaign_id, time_window)
);
CREATE INDEX ON intel.campaign_result (account, time_window);

-- An alert is open while its condition holds; the same condition on the
-- same object keeps one row (key). sent_at is when it went to Telegram.
CREATE TABLE intel.alert (
    id BIGSERIAL PRIMARY KEY,
    key TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('runaway', 'tracking_gap', 'page_gap', 'postback_gap', 'item_rejected')),
    account TEXT NOT NULL,
    campaign_id BIGINT,
    item_id BIGINT,
    title TEXT NOT NULL,
    detail TEXT NOT NULL,
    numbers JSONB NOT NULL DEFAULT '{}',
    opened_at TIMESTAMPTZ NOT NULL,
    seen_at TIMESTAMPTZ NOT NULL,
    closed_at TIMESTAMPTZ,
    sent_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX alert_open_key ON intel.alert (key) WHERE closed_at IS NULL;

-- A change Intel proposes. Intel never makes it: the card's button opens
-- Launch with the change filled in (launch_url), and a person confirms
-- there. state: open; dismissed ("not now", by a person); gone (the reason
-- no longer holds); done (Launch made the change).
CREATE TABLE intel.suggestion (
    id BIGSERIAL PRIMARY KEY,
    key TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('pause-ads', 'pause-campaign', 'set-daily-cap')),
    account TEXT NOT NULL,
    group_id BIGINT,
    campaign_id BIGINT NOT NULL,
    item_ids BIGINT[] NOT NULL DEFAULT '{}',
    -- The values filled in for Launch, editable there (daily cap, bid…).
    values JSONB NOT NULL DEFAULT '{}',
    title TEXT NOT NULL,
    why TEXT NOT NULL,
    numbers JSONB NOT NULL DEFAULT '{}',
    launch_url TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'dismissed', 'gone', 'done')),
    created_at TIMESTAMPTZ NOT NULL,
    seen_at TIMESTAMPTZ NOT NULL,
    answered_at TIMESTAMPTZ,
    answered_by TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX suggestion_live_key ON intel.suggestion (key) WHERE state = 'open';
CREATE INDEX ON intel.suggestion (campaign_id, created_at);

-- When each job last finished, for the pages' "as of" line.
CREATE TABLE intel.job_mark (
    job TEXT PRIMARY KEY,
    done_at TIMESTAMPTZ NOT NULL,
    note TEXT NOT NULL DEFAULT ''
);
