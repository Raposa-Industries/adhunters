-- Publishes the logins added on Contas to Intel (decision 0028), so their
-- accounts' delivery status, numbers and alerts reach Intel like the
-- server's own login's. The view holds sealed secrets, so it is not given to
-- launch_api_read (Create and Desk read through it): only to the intel login.

-- The Taboola logins people added on Launch's Contas page, for Intel to read
-- the same accounts (decision 0028). The secret and the proxy stay sealed
-- (AES-256-GCM, shared/taboola/logins); only a reader given the key, kept
-- apart from the database, can open them. accounts are the advertiser
-- accounts chosen for Launch; a login with none is left out.
CREATE VIEW launch_api.taboola_login_v1 AS
SELECT l.id, l.name, l.client_id, l.secret, l.proxy, l.accounts, l.changed_at
FROM launch.login l
WHERE l.network = 'taboola' AND cardinality(l.accounts) > 0;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'intel') THEN
        GRANT SELECT ON launch_api.taboola_login_v1 TO intel;
    END IF;
END $$;
