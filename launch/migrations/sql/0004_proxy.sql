-- lint: new-table
-- A proxy is the address every request to the network for an account goes
-- through (http, https or socks5, often with a user and password), so the
-- network sees each account come from its own address. It is sealed like a
-- login's secret (AES-256-GCM, launch/internal/logins), since it holds a
-- password; pages see only its host and port.
--
-- A login added on the Contas page must have one: its accounts never go
-- direct. Logins added before proxies have none, and their requests are
-- refused until one is set.
ALTER TABLE launch.login ADD COLUMN proxy bytea;

-- user_id is the login's Taboola user ID, which Backstage shows next to the
-- client ID and secret. No API call needs it (the token takes only the
-- client ID and secret); it is kept to tell logins apart. Logins added
-- before it have ''.
ALTER TABLE launch.login ADD COLUMN user_id text NOT NULL DEFAULT '';

-- The server's own login's accounts (TABOOLA_* in its env) may have one
-- each; without one they go direct.
CREATE TABLE launch.account_proxy (
    network text NOT NULL CHECK (network IN ('taboola')),
    account text NOT NULL CHECK (account <> ''),
    proxy bytea NOT NULL,
    set_by text NOT NULL DEFAULT '',
    set_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (network, account)
);
