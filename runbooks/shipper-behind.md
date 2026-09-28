# ShipperBehind

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/`.

## What it means

A sealed raw file has waited over 15 minutes to be archived on the named box. Nothing is lost: the file sits in the spool, which holds weeks. But the loader cannot see it, so the numbers fall behind.

## Check

1. `journalctl -u tracks-shipper --since -30min` on the box: the error says whether object storage, the database or one file is the problem.
2. `tracks_shipper_files_waiting` growing steadily means every upload fails; stuck at one means one file.
3. "different bytes" in the log means a key already in the archive with other content. The shipper never overwrites it.

## Fix

- Object storage or the database down: it retries by itself; fix the outage.
- Wrong keys after a change: fix `/etc/adhunters/tracks-shipper.env`, then `systemctl restart tracks-shipper`.
- A key with different bytes: never delete either copy. Keep the local file aside and ask the owner; both are received data.

## After

When it clears, the loader catches up by itself.
