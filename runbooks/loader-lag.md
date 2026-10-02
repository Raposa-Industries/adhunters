# LoaderLag

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

The loader is more than 10 minutes behind capture. Raw files are safe in the archive; the numbers in the apps are late.

## Check

1. Is the shipper behind too? Then the loader is waiting, not slow: see [shipper-behind](shipper-behind.md).
2. `tracks-loader status` on the data box: pending files, the oldest minute, quarantined files.
3. `journalctl -u tracks-loader --since -30min`: repeated errors on one file, or database errors.
4. Postgres: is it slow or locked? `tracks_loader_hour_close_seconds` and the Postgres dashboard.

## Fix

- Database down or out of connections: see [postgres-down](postgres-down.md) and [postgres-connections](postgres-connections.md).
- A replay is running: expected; it clears when the replay is done.
- The loader is stuck: `systemctl restart tracks-loader` (a load cut off rolls back and runs again).

## After

It clears when the oldest pending file is under 10 minutes old.
