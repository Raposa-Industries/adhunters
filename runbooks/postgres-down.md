# PostgresDown

Page: Telegram, with sound, every 5 minutes until it clears. Rule: `platform/observe/rules/`.

## What it means

Postgres on the data box is not answering Alloy's checks. The loader, Raposa and the apps stop; capture and the shipper keep going (the spool waits).

## Check

1. `systemctl status postgresql@17-main` and `journalctl -u postgresql@17-main -n 100` on the data box.
2. Disk full? `df -h /var/lib/postgresql`.
3. Memory: an out-of-memory kill of a Postgres process restarts the whole server.

## Fix

- Stopped: `systemctl start postgresql`.
- Disk full: see [disk-full-soon](disk-full-soon.md). Never delete files under the data folder by hand.
- It will not start and the log shows damage: stop, and ask the owner before anything else (restore is the path, and it needs their word).

## After

It clears when Postgres answers. The loader catches up from the archive by itself.
