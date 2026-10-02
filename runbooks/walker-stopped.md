# WalkerStopped

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/collection.yaml`.

## What it means

tracks-walker on the worker box has walked no landing page for 30 minutes. Ads keep running and their landing pages keep changing, but nothing reads them: what changes now is not kept.

## Check

1. `systemctl status tracks-walker` and `journalctl -u tracks-walker --since -30min` on the worker box.
2. "no proxy line to walk with" in the log: every walker line is resting or gone.
3. "reading the ads due for a walk" errors: the database (see **PostgresDown**).
4. **CaptureStopped** mutes this: with no new sightings there is nothing due.

## Fix

- Stopped or stuck: `systemctl restart tracks-walker`.
- No lines: the walker uses capture's lines (`/etc/adhunters/tracks-capture/proxies.env`); check them with the proxy provider.

## After

It clears with the next walk. Ads still running are walked again when their turn comes; pages that changed in between are lost.
