-- The published face of Intel, version 1. Each statement is the matching file
-- in contract/sql/intel/, word for word (a test checks). Other services read
-- these and nothing else. Intel never writes to Taboola: Launch does.

CREATE SCHEMA intel_api;

-- Money and steps per Taboola campaign over whole days in its account's time
-- zone: today (so far), yesterday, 7d and 30d. Launch shows today's in grey.
CREATE VIEW intel_api.campaign_result_v1 AS
SELECT campaign_id, time_window, account, impressions, clicks, spent, network_sales, tracker_clicks, lp_views,
       lp_clicks, sales, revenue, profit, roi, cost_per_sale, cost_per_sale_low, cost_per_sale_high, sales_source,
       refreshed_at
FROM intel.campaign_result;

-- Step rates, each with its likely range, and profit per 1,000 impressions
-- per Taboola item over the same windows, with Spy's words. custom_id is our
-- ad id when Launch or Create made the item.
CREATE VIEW intel_api.ad_result_v1 AS
SELECT r.item_id, r.time_window, r.campaign_id, r.account, i.custom_id, r.impressions, r.clicks, r.spent,
       r.tracker_clicks, r.lp_views, r.lp_clicks, r.sales, r.revenue, r.profit, r.ctr, r.ctr_low, r.ctr_high,
       r.lp_click_rate, r.lp_click_rate_low, r.lp_click_rate_high, r.sale_rate, r.sale_rate_low, r.sale_rate_high,
       r.order_value, r.profit_per_1000, r.profit_per_1000_low, r.profit_per_1000_high, r.profit_basis, r.word,
       r.sureness, r.spend_to_tell, r.refreshed_at
FROM intel.ad_result r LEFT JOIN intel.tb_item i USING (item_id);

-- Intel's suggestions. launch_url opens Launch with the change filled in;
-- nothing changes until a person confirms there. Launch and Desk read the
-- open ones; state says what became of the others.
CREATE VIEW intel_api.suggestion_v1 AS
SELECT id, kind, account, group_id, campaign_id, item_ids, values, title, why, numbers, launch_url, state,
       created_at, seen_at, answered_at
FROM intel.suggestion;

-- Intel's alerts, open (closed_at null) and closed.
CREATE VIEW intel_api.alert_v1 AS
SELECT id, kind, account, campaign_id, item_id, title, detail, numbers, opened_at, seen_at, closed_at
FROM intel.alert;

-- Each campaign with every campaign it continues (copied from, back to the
-- first; depth 0 is itself). root_id names the whole line.
CREATE VIEW intel_api.campaign_line_v1 AS
SELECT campaign_id, ancestor_id, depth, root_id FROM intel.campaign_line;
