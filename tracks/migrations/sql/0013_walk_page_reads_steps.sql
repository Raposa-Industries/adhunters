-- Walk pages keep each URL once (decision 0025, step 2 of 3: switch). The
-- published view reads walk_step and page_url instead of walk_page: the same
-- columns, the same rows, the same values, so its readers change nothing.
-- The joins to page_url are LEFT so a reader that takes neither URL does not
-- pay for them.

-- Published: every page walked, with what it said.
CREATE OR REPLACE VIEW tracks_api.walk_page_v1 AS
SELECT w.id AS walk_id, w.at, w.ad_id, w.creative_id, w.account_id, w.link_id, w.publisher_id, w.outcome,
       p.step, u.url, f.url AS final_url, p.host, p.status, p.hops, p.page_type, p.checkout_platform, p.seller_account,
       p.version_hash
FROM tracks.walk w
JOIN tracks.walk_step p ON p.walk_id = w.id
LEFT JOIN tracks.page_url u ON u.id = p.url_id
LEFT JOIN tracks.page_url f ON f.id = p.final_url_id;
