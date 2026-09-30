-- Published: every page walked, with what it said.
CREATE VIEW tracks_api.walk_page_v1 AS
SELECT w.id AS walk_id, w.at, w.ad_id, w.creative_id, w.account_id, w.link_id, w.publisher_id, w.outcome,
       p.step, p.url, p.final_url, p.host, p.status, p.hops, p.page_type, p.checkout_platform, p.seller_account,
       p.version_hash
FROM tracks.walk w
JOIN tracks.walk_page p ON p.walk_id = w.id;
