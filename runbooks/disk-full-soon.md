# DiskFullSoon

Page: Telegram, with sound, every 5 minutes until it clears. Rule: `platform/observe/rules/`.

## What it means

At the growth rate of the last 6 hours, a disk on this box will be full within 6 hours. A full disk stops the spool, Postgres or both.

## Check

1. What grows? `du -xh --max-depth=2 / | sort -h | tail -20` on the box.
2. The spool (`/var/lib/tracks/spool`) growing means the shipper is not archiving: see [shipper-behind](shipper-behind.md).
3. Postgres (`/var/lib/postgresql`) growing fast: a replay, a table not trimmed to its keep time, or WAL piling up.
4. The journal: `journalctl --disk-usage`.

## Fix

- Spool: fix the shipper. **Never delete spool files that are not shipped**: they are received data.
- Journal: `journalctl --vacuum-size=1G`.
- Postgres: find the table (`\dt+` sorted by size) and ask the owner before dropping anything.
- Out of room for good: resize the server (Hetzner) or add a volume.

## After

It clears when the forecast no longer reaches zero within 6 hours.
