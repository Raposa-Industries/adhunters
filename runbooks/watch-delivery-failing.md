# WatchDeliveryFailing

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/`.

## What it means

Watch events could not be sent to Pushcut and were given up. The people who set those watches did not get them.

## Check

1. `journalctl -u raposa-engine --since -1h | grep -i pushcut`: the error Pushcut answered.
2. A 401 or 403 is the API key; a 404 is a notification name that no longer exists in someone's Pushcut app.

## Fix

- Key: fix `PUSHCUT_API_KEY` in `/etc/adhunters/raposa-engine.env`, then `systemctl restart raposa-engine`.
- A removed notification: tell the person to recreate it or remove the watch.

## After

It clears after 30 minutes without a failed delivery. Failed deliveries are not re-sent.
