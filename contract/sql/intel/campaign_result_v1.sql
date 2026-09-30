-- Money and steps per Taboola campaign over whole days in its account's time
-- zone: today (so far), yesterday, 7d and 30d. Launch shows today's in grey.
CREATE VIEW intel_api.campaign_result_v1 AS
SELECT campaign_id, time_window, account, impressions, clicks, spent, network_sales, tracker_clicks, lp_views,
       lp_clicks, sales, revenue, profit, roi, cost_per_sale, cost_per_sale_low, cost_per_sale_high, sales_source,
       refreshed_at
FROM intel.campaign_result;
