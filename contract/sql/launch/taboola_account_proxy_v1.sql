-- The proxies people set on Contas for accounts of the server's own login
-- (the "···" menu), for Intel to read those accounts only through them
-- (decision 0028). The proxy stays sealed, bound to its account.
CREATE VIEW launch_api.taboola_account_proxy_v1 AS
SELECT p.account, p.proxy, p.set_at
FROM launch.account_proxy p
WHERE p.network = 'taboola';
