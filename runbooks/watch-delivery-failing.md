# WatchDeliveryFailing

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

Raposa's watched events could not be posted to the ops group "AdHunters operation" on Telegram and were given up. The people who set those watches did not get them.

## Check

1. `journalctl -u raposa-engine --since -1h | grep -i telegram`: the error Telegram answered.
2. A 401 is the bot token; a 400 "chat not found" or a 403 is the chat id, or the bot was removed from the group.

## Fix

- Token or chat id: fix `TELEGRAM_BOT_TOKEN` or `OPS_TELEGRAM_CHAT_ID` in `/etc/adhunters/raposa-engine.env` on the worker (the same values as `observe-bot.env` on the data box), then `systemctl restart raposa-engine`.
- A bot removed from the group: add it back.

## After

It clears after 30 minutes without a failed delivery. Failed deliveries are not re-sent.
