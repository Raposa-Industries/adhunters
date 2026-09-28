-- One distinct dark funnel of one investigation and its share of the sample.
CREATE VIEW raposa_api.variant_v1 AS
SELECT id, investigation_id, label, page_ids, first_page_id, visits, share_pct, first_seen_at, last_seen_at
FROM raposa.variant;
