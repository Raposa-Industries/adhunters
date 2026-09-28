-- The landing pages investigations reached, as proof of who runs an ad.
CREATE VIEW raposa_api.evidence_v1 AS
SELECT id, investigation_id, creative_id, ad_id, account_id, campaign_external_id, device, outcome,
       landing_page_id, final_url, domain, redirect_hops, page_kind, title, pixels, checkout_platform,
       checkout_merchant_id, seller_platform, seller_account, funnel_steps, recorded_at
FROM raposa.evidence;
