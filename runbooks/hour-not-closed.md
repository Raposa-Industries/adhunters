# HourNotClosed

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/`.

## What it means

The newest closed hour started more than 2.5 hours ago. Hourly counts, and everything computed from them, are late.

## Check

1. Is the loader behind? Then an hour cannot close yet: see [loader-lag](loader-lag.md).
2. `journalctl -u tracks-loader --since -3h | grep -i close`: an hour close that fails, times out or is cut off.
3. `tracks_loader_hour_close_seconds`: closes getting slower point at Postgres (see the Postgres dashboard).

## Fix

- A close failing on a statement timeout: look at the query plan with the owner; do not raise timeouts blindly.
- The loader stuck: `systemctl restart tracks-loader`. A close cut off rolls back and runs again.

## After

It clears on the next closed hour.
