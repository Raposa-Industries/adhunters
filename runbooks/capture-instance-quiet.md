# CaptureInstanceQuiet

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

One capture instance has written nothing to its spool for over 3 minutes. The other instances still collect, so collection runs at part strength.

## Check

1. `systemctl status tracks-capture@<instance>` and `journalctl -u tracks-capture@<instance> --since -15min` on the box named in the alert.
2. Is the disk full? `df -h /var/lib/tracks` (see [disk-filling](disk-filling.md)).
3. Is the sealer behind? `tracks_capture_spool_unsealed_files` growing means compression cannot keep up.

## Fix

- Stuck or crashed: `systemctl restart tracks-capture@<instance>`.
- Crashing on start after a deploy: roll back to `tracks-capture.prev`.

## After

It clears on the instance's next write.
