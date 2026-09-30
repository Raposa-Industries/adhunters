-- Intel's alerts, open (closed_at null) and closed.
CREATE VIEW intel_api.alert_v1 AS
SELECT id, kind, account, campaign_id, item_id, title, detail, numbers, opened_at, seen_at, closed_at
FROM intel.alert;
