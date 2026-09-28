# WalArchiveFailing

Page: Telegram, with sound, every 5 minutes until it clears. Rule: `platform/observe/rules/`.

## What it means

Postgres cannot copy its WAL files to object storage, and the newest one it did copy is over 15 minutes old. The database still works, but if the data box died now, the changes since then would be lost (raw files would still let the loader rebuild the facts). WAL files also pile up on the data box's disk until archiving works again.

## Check

1. `journalctl -u postgresql@17-main --since -30min | grep -i archive` and `/var/log/pgbackrest/` on the data box: the error pgBackRest gave.
2. `sudo -u postgres pgbackrest --stanza=adhunters check`: it tries one archive-push and says what fails.
3. Hetzner Object Storage: is the bucket `adhunters-backups` there, and are the keys in `/etc/pgbackrest/pgbackrest.conf` still valid?
4. `du -sh /var/lib/postgresql/17/main/pg_wal`: how fast WAL is piling up.

## Fix

- Keys or bucket: fix them in `/etc/pgbackrest/pgbackrest.conf`. Archiving retries by itself; no restart needed.
- Object storage down: wait, and watch **DiskFullSoon**. Never delete files in `pg_wal` by hand.
- If the disk is about to fill before storage comes back, ask the owner before turning archiving off.

## After

It clears once a WAL file is archived. Then run `sudo -u postgres pgbackrest --stanza=adhunters check` once more.
