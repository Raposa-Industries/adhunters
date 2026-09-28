# Measure before committing

Two numbers decide whether two CX43s are enough (design page, "Measure
first"). Both are measured with the code that will run, on data we already
have, and neither touches prodbox or the running collector.

## 1. Raw bytes per scrape: a shadow capture on bigworker

`shadow-capture.sh` builds `tracks-capture` on your machine, copies it to
bigworker, and runs it for 24 hours beside the collector as a transient
systemd unit that writes only to `/opt/tracks-shadow`. From a checkout of this
repo, where `ssh bigworker` works:

```
tracks/measure/shadow-capture.sh start     # then wait a day
tracks/measure/shadow-capture.sh stats     # bytes per scrape, per network
tracks/measure/shadow-capture.sh clean     # stop and delete /opt/tracks-shadow
```

It reads the collector's `publishers.yaml` and `proxies.env` and changes
neither. It shares the collector's proxy lines, so by default it runs one
worker with a 1 s pause, about 15% on top of the collector's rate; bytes per
scrape do not depend on the rate. `WORKERS`, `THROTTLE` and `FOR` change that.
It is sandboxed (read-only system, 512 MB, one core at most, low priority) and
stops by itself after `FOR`.

`stats` prints, per network and day: scrapes, errors, empty answers, answer
KB per scrape, spool KB per scrape before and after zstd, the compression
ratio, and what the archive and the spool would grow by per day at the live
collector's 14,000 scrapes an hour.

## 2. Hour close and Direction speed: one archived day on a CX43

`hourclose-direction/` loads one archived day of sightings (the gzip TSVs
`spy-archive.timer` writes to bigworker's `/opt/backups/spy-sightings`) into
PostgreSQL 17 on a fresh CX43, in the new layout, and times:

- loading the day (COPY, then into a daily partition, as the loader will);
- closing each of the 24 hours: one `INSERT … SELECT … GROUP BY` each into
  `ad_hourly`, `ad_account_brand_hourly` and `publisher_hourly`, then the day
  into the daily counts; it checks every sighting lands in exactly one count;
- Direction and Size, today's SQL copied unchanged from the collector
  (`05_direction.sql`), reading the new hourly counts: the day's first run,
  5-minute runs, and the run after a new hour.

Direction needs 3 weeks of history the one day does not have, so the closed
day is copied back 27 days with noise (`04_history.sql`). The archive has ids
only, so accounts, operators, brands and verticals are made up from the ids at
realistic counts (`02_load.sql`). Both only decide how rows group; the volume
and the working set are the real day's.

### Running it

It needs a CX43, which costs money: about €0.03 an hour, so a run of an hour
or two costs a few cents, as long as the server is deleted after.

```
hcloud server create --name measure --type cx43 --image ubuntu-24.04 --location nbg1 --ssh-key <your key>
tracks/measure/hourclose-direction/run-on-server.sh <its ip>
hcloud server delete measure
```

`run-on-server.sh` checks the newest archived day against its `.sha256` on
bigworker, streams it to the server, runs `bench.sh` there (it installs
PostgreSQL 17 sized for 16 GB), and saves `results-sighting_<day>.md` next to
itself. `DAY_FILE=/opt/backups/spy-sightings/sighting_<yyyymmdd>.tsv.gz`
picks another day.

`bench.sh` also runs anywhere with PostgreSQL 15 or newer:
`SKIP_INSTALL=1 ./bench.sh sighting_<yyyymmdd>.tsv.gz`.
