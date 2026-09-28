# PostgresConnections

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/`.

## What it means

Postgres has more than 70 connections open of its 80. New connections will soon be refused.

## Check

1. `SELECT application_name, state, count(*) FROM pg_stat_activity GROUP BY 1, 2 ORDER BY 3 DESC;`: which binary holds them (every login sets its application name).
2. Many `idle in transaction`: a binary holding transactions open.

## Fix

- One binary over its cap: restart it and open an issue; every pool has a connection cap in `kit/pg`.
- Growth by design (a new service): raise `max_connections` in `platform/servers/setup.sh` by PR.

## After

It clears under 70.
