-- Published: the daily sums, per ad, publisher and device.
CREATE VIEW spy_api.price_day_v1 AS
SELECT day, ad_id, creative_id, publisher_id, device_id, auctions, rtb, clearing_n, clearing_sum, clearing_p25,
       clearing_p50, clearing_p75, bid_n, bid_sum, bid_p50, cap_p50, second_n, second_sum, second_p50
FROM spy.price_day;
