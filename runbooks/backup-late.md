# BackupLate, BackupRepoUnreadable

BackupLate is a page (Telegram, with sound, every 5 minutes). BackupRepoUnreadable is a chat alert. Rules: `platform/observe/rules/boxes.yaml`.

## What it means

- **BackupLate:** no database backup has finished for 26 hours. Backups run every day at 03:30 UTC (full on Sunday, differential the other days), so at least one was missed.
- **BackupRepoUnreadable:** pgBackRest cannot read the backup repository, or reports the stanza unhealthy.

Without fresh backups, restoring to a point in time needs a longer replay of WAL, and a broken repository may mean no restore at all.

## Check

1. `systemctl list-timers 'pgbackrest-*'` on the data box: are the timers there and when did they last run?
2. `systemctl status pgbackrest-backup@diff pgbackrest-backup@full` and `journalctl -u 'pgbackrest-backup@*' --since -2d`: the error.
3. `sudo -u postgres pgbackrest --stanza=adhunters info`: what is in the repository.
4. `/var/lib/adhunters/textfile/pgbackrest.prom`: what Alloy last read.

## Fix

- A failed run: fix the cause (keys, storage, disk), then `systemctl start pgbackrest-backup@diff`.
- Timers missing: run `platform/servers/setup.sh` again.
- The repository is damaged: stop and ask the owner. Take a new full backup (`systemctl start pgbackrest-backup@full`) only with their word.

## After

It clears when a backup finishes, because the metrics are written after every run.
