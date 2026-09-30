# FunnelsSpoolFailing

Page: Telegram with sound, repeated until it clears. Rule: `platform/observe/rules/funnels.yaml`.

## What it means

funnels-edge could not write the last beacon to its spool (`/var/lib/funnels/spool` on the data box) for 2 minutes. The page script does not send a beacon again, so every event since is lost. Landing pages still load.

## Check

1. `journalctl -u funnels-edge -n 50`: the error on "writing a beacon to the spool" (disk full, permissions, a read-only file system).
2. `df -h /var/lib/funnels` for space; the DiskFilling alert may be up too.
3. `ls -la /var/lib/funnels/spool` owned by `funnels`.

## Fix

- Disk full: free space (see disk-full-soon.md). Archived files older than 48 hours are deleted by funnels-loader; if it is down, start it first (`systemctl status funnels-loader`), it archives and then clears the spool.
- Permissions: `chown -R funnels:funnels /var/lib/funnels/spool`, with the owner's word.

## After

It clears on the first beacon written. The events lost while it fired cannot be recovered; say which hours in the alerts group.
