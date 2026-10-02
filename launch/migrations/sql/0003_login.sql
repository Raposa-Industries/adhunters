-- lint: new-table
-- A login is one ad network login people added on the Contas page, so Launch
-- can use more accounts than the server's own login (TABOOLA_* in its env).
-- The secret is sealed (AES-256-GCM, launch/internal/logins) with a key kept
-- outside the database, on the server's disk; the database never holds it
-- in the clear, and no page shows it again. accounts are the advertiser
-- accounts people chose to use; the network account never is one.
CREATE TABLE launch.login (
    id bigserial PRIMARY KEY,
    network text NOT NULL CHECK (network IN ('taboola')),
    name text NOT NULL CHECK (name <> ''),
    client_id text NOT NULL CHECK (client_id <> ''),
    secret bytea NOT NULL,
    accounts text[] NOT NULL DEFAULT '{}',
    added_by text NOT NULL DEFAULT '',
    added_at timestamptz NOT NULL DEFAULT now(),
    changed_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (network, client_id)
);
