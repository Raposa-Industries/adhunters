# Retiring prodbox and bigworker

Scripts for moving off the old boxes without losing anything. They read
prodbox and bigworker and never change them. They write only to the archive
bucket, a work folder on the data box (`/var/tmp`), and, for Auto-Creative,
a folder on your computer and the library's counters. The plan and its order
are in the project's `plan/retire-prodbox-bigworker.md`. Each step that
changes an old box (stopping a service, the switch-over, deleting a box)
waits for the owner's word, as `platform/SWITCH-OVER.md` says.

Run them on your own computer, Tailscale up, from the repository. `ssh bigworker`
and `ssh admin@adhunters-data` must work; the Auto-Creative steps also need
`ssh prod`. They ask for prodbox's password (or key passphrase) once at the
start and reuse that connection for 15 minutes.

| Script | What it does |
|---|---|
| `save-old-data.sh check` | What there is, and whether the rest can run: rclone on the data box, a login on the old database and what it can read, bigworker's archived days with their row counts. |
| `save-old-data.sh setup` | Installs rclone on the data box with an `archive` remote, using the object storage keys in `tracks-loader.env` (never shown). |
| `save-old-data.sh db` | Starts `save-db.sh` on the data box as the unit `retire-save-db`: the old database into the bucket (below). Follow it with `sudo journalctl -u retire-save-db -f -o cat` on the data box. Run it again once a week until the collector stops; it adds only the days closed since. `FRESH_MAIN=1` also makes a new main dump (the final one). |
| `save-old-data.sh gz` | bigworker's `/opt/backups/spy-sightings` into the bucket, each file checked against its `.sha256`. |
| `save-old-data.sh auto-creative` | Auto-Creative's database (`pg_dump`) and its MinIO volume (`tar`) into the bucket. |
| `auto-creative-to-drive.sh [folder]` | The creatives the team kept in Auto-Creative, as `<vertical>/Auto-Creative/<name>.<ext>` on your computer, ready to drag into the library's Drive folder. Also raises the library's counters past Auto-Creative's, so new names never repeat old ones. |
| `compare-collection.sh [day]` | One UTC day of Tracks next to the old collector: scrapes per hour and line, ads per scrape per publisher, which creatives each saw, and how many capture workers Tracks needs to match. |
| `reconcile.sh` | Every old table next to its new home with counts on both sides: lookups by natural key (old keys missing in new), daily and hourly sums per day, the hours the collector never counted, creative pairs, Spy's copy, the walker's pages, Raposa (jobs, visits per job, and the pages and files jobs use, by content hash, with the file bytes), what the old database still writes, and what the archive bucket holds. Runs on the data box: `ssh admin@adhunters-data 'sudo bash -s' < platform/retire/reconcile.sh`. Only reads. |

## What lands in the bucket

All under `adhunters-raw/legacy/`:

| Key | What |
|---|---|
| `prodbox/adplatform_v2-main-<day>.dump` | `pg_dump -Fc` of the old database without its two sighting tables. Read back in full through `pg_restore` before its `.ok` is written. |
| `prodbox/adplatform_v2-adhunters_sighting.dump` | The legacy `public.adhunters_sighting` (26 GB in the database). |
| `prodbox/adplatform_v2-spy_sighting-schema.sql` | `spy.sighting`'s definition. |
| `spy-sightings/sighting_<yyyymmdd>.tsv.gz` (+ `.sha256`) | Each closed day of `spy.sighting`, in the collector's own archive format (a `# columns` line, then `COPY` text). From prodbox, or from bigworker for the days the collector already moved there. Row counts are checked before upload. |
| `auto-creative/autocreative-<day>.dump`, `miniodata-<day>.tar.gz` | Auto-Creative's database and files. |

`spy.sighting` stays out of the main dump on purpose: the collector creates
a partition near 00:00 UTC and drops one at 04:10 UTC (`spy-archive.timer`),
and either would wait behind a long dump's lock and stall its writes. Its
days are copied one partition at a time.

## A login on the old database

`save-db.sh` and `compare-collection.sh` read prodbox from the data box with
`OLD_DATABASE_URL`, taken from `/etc/adhunters/tracks-bridge.env` or else
`/etc/adhunters/spy-numbers.env`, and always open a read-only transaction.
`check` says which it found and how many tables that login cannot read. A
dump needs them all. If it finds none, or the login can't read everything,
use the collector's own login, as `platform/SWITCH-OVER.md` steps 4 and 5
do: one `pg_hba.conf` line on prodbox (the owner's word first), then the
login copied into `tracks-bridge.env` without showing it. The bridge itself
stays stopped.
