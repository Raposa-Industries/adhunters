-- The ads Launch made, with our ad id (ah-…). The network's ids are also
-- given as numbers where they are numbers.
CREATE VIEW launch_api.item_v1 AS
SELECT network, account, campaign_id, item_id,
       CASE WHEN campaign_id ~ '^[0-9]{1,18}$' THEN campaign_id::bigint END AS campaign_number,
       CASE WHEN item_id ~ '^[0-9]{1,18}$' THEN item_id::bigint END AS item_number,
       ad_id, made_at
FROM launch.item;
