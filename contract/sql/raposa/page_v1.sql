-- One version of one page an investigation reached. The HTML is not here:
-- raposa-web shows it.
CREATE VIEW raposa_api.page_v1 AS
SELECT id, page_key, url, host, path, title, page_kind, word_count, is_dark, checkout_platform,
       checkout_merchant_id, capture_state, first_seen_at, last_seen_at, times_seen
FROM raposa.page;
