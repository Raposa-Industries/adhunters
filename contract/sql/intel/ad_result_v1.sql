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
