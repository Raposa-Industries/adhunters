-- lint: new-table
-- When Launch first used each account of the server's own login
-- (TABOOLA_ACCOUNTS in its env), for Contas' "Adicionada" column. An added
-- login's accounts show the login's own added_at instead. Accounts already
-- in the env when this ships get the day it ships.
CREATE TABLE launch.account_seen (
    network text NOT NULL CHECK (network IN ('taboola')),
    account text NOT NULL CHECK (account <> ''),
    seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (network, account)
);
