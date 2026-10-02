# PostgresRestarted

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

Postgres on the data box restarted in the last hour, too briefly for **PostgresDown**. Every service lost its database connections for a few seconds and logged errors (connection refused, "the database system is shutting down"); they reconnect by themselves. **ErrorsLogged** is muted for the hour, since those errors are this restart.

## Check

1. Who restarted it: `journalctl -u postgresql@17-main --since -2h` ("received fast shutdown request" is a clean stop) and `grep -h "upgrade" /var/log/unattended-upgrades/unattended-upgrades-dpkg.log | tail`. On 2 Oct 2026 unattended-upgrades updated libssl, and needrestart restarted every service using it, Postgres included.
2. A crash instead ("terminated by signal", "server process ... was terminated"): `journalctl -k --since -2h` for an out-of-memory kill.
3. Services that did not come back: **ServiceDown**, **TaskLate** and **ErrorsLogged** after the hour say so.

## Fix

- A security update: nothing to do; the restart is how the patched library gets loaded.
- A crash: see [out-of-memory-kill](out-of-memory-kill.md) or [postgres-down](postgres-down.md).

## After

It clears an hour after the restart.
