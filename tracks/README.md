# tracks

Collection: what ads run on Taboola and NewsBreak. It replaces
adhunters-collector, one binary at a time. Internal: no app of its own.

| Binary | What it does | Status |
|---|---|---|
| `tracks-capture` | Scrapes the feeds and writes every answer, as received, to the spool. No database. | shadow run |
| `tracks-shipper` | Uploads sealed raw files to the archive and records them. | built, not deployed |
| `tracks-loader` | Parses raw files into sightings, closes each hour into counts, replays any range. | built, not deployed |
| `tracks-bridge` | Writes Tracks' scrapes into the old collector's database, so today's Spy keeps working after the switch-over. | built, not deployed |
| `tracks-walker` | Follows ad links to landing pages. | later |

## tracks-capture

The collector's sweeper, ported from adhunters-collector `e20148c`
(`internal/sweeper`, `internal/proxy`). It sends byte-for-byte the same
requests: a random target and device per scrape, datacenter proxy lines first
and ISP lines only while every datacenter line cools down, a 30 s cooldown
after any error, 150 ms between scrapes. Two things differ:

- It does not parse. Every answer goes to the spool whole, errors included,
  because errors count as scrapes. Parsing moves to the loader.
- Each proxy line keeps one HTTP transport, so connections are reused between
  scrapes. The collector dialled a new one for every request.

```
tracks-capture run   -targets targets.yaml -lines proxies.env -spool spool [-instance a] [-workers 4] [-for 24h]
tracks-capture stats [-rate 14000] spool
```

- `-targets` is the collector's `config/publishers.yaml`, loaded as it is
  (enabled targets only). `-lines` is its `secrets/proxies.env`
  (`key=host:port:user:pass`); the backup lines `isp-1` to `isp-5` are kept out,
  as the collector keeps them on a 24 h cooldown. Without a usable line it
  refuses to start rather than scrape from the box's own address.
- It stops cleanly on SIGTERM, or by itself after `-for`. A request in flight
  finishes and is written; open files are sealed before it exits.
- `/healthz` and `/metrics` on `OPS_ADDR`. Metrics: scrapes by network,
  publisher, device and outcome; answer bytes; latency; sealed raw and zstd
  bytes; files waiting to be sealed; lines cooling down. Health fails when
  nothing was written for 2 minutes or sealing falls behind.
- Logs one line per sealed file (rows, raw and compressed bytes), not one per
  scrape.
- `stats` decompresses every sealed file and prints bytes per scrape per
  network and day, both as sealed and recompressed one hour per file, and
  what the archive would grow by at the live rate (from the hour column,
  which matches a minute's file at the full rate).

### Raw files

One JSON line per scrape: `id` (ULID), `at`, `network`, `publisher`, `device`,
`line` (the proxy line's key, never its credentials), `instance`, `version`,
`url`, `request_body` (NewsBreak), `status` (0 when no answer came),
`latency_ms`, `headers` (a few kept ones), `body` and `error`. A body that is
not valid UTF-8 goes in `body_b64` instead, so every answer round-trips
exactly. `truncated` marks one cut at 8 MB, as the collector cut them.

One file per instance, network and minute:
`spool/<network>/<yyyy>/<mm>/<dd>/<hh>/capture-<instance>-<hhmm>.ndjson`. When
the minute ends it is compressed with zstd to the same name plus `.zst`, and
the plain file is removed. The path under `spool/` is the archive key under
`raw/`, so the shipper uploads it unchanged. (The design page drew one file per
instance and minute with the network inside; one per network keeps the
archive's `raw/<network>/` layout and makes bytes per network exact.)

A file that a crash left unsealed is sealed at the next start. A restart inside
the same minute starts `capture-<instance>-<hhmm>-2.ndjson` beside it, so
nothing is ever overwritten.

zstd level 9 is asked for; the Go encoder (klauspost/compress) maps it to its
"better compression" level, close to the zstd command's level 7 or 8.

## tracks-shipper

```
tracks-shipper run -spool /var/lib/tracks/spool -archive s3://adhunters-raw [-box worker] [-every 10s] [-keep 48h]
```

Every 10 s it takes each sealed file in the spool, counts its scrapes, uploads
it under the same key (the path under the spool) and checks the upload's
checksum, records it in `tracks.raw_file`, then leaves a `.shipped` marker
beside it. A shipped file is deleted 48 hours later. A key already in the
archive with different bytes is never overwritten: the file stays in the
spool and the error repeats until a person looks. When the archive or the
database is down, the spool simply grows.

`-archive` takes `s3://bucket[/prefix]` (Hetzner Object Storage, keys from
`S3_ENDPOINT`, `S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`) or `file:///path`
for tests. The database login (`DATABASE_URL`) may only insert into
`tracks.raw_file`. Health fails when no pass has succeeded, or a sealed file
has waited, for 5 minutes.

## tracks-loader

```
tracks-loader migrate
tracks-loader run    -archive s3://adhunters-raw [-max-lag 10m]
tracks-loader replay -from 2026-09-20T00:00:00Z -to 2026-09-21T00:00:00Z [-network taboola]
tracks-loader status [-books]
tracks-loader import-old -before 2026-10-01T00:00:00Z [-from 2026-06-01T00:00:00Z]
```

`run` does, in a loop:

1. **Load** the oldest pending raw file (`FOR UPDATE SKIP LOCKED`, so a
   second loader could run beside it). It checks the file's sha256, parses
   every record with the collector's parsers (`parse/`, ported from
   `internal/sweeper` and `internal/openrtb` at `e20148c`), and in one
   transaction removes whatever an earlier load of the same file stored,
   writes the lookups, scrapes, sightings and Taboola auctions, and marks the
   hours it touched dirty. Loading a file twice therefore gives the same
   facts as loading it once.
2. **Close** each dirty hour 5 minutes after it ends, once every raw file that
   can hold its scrapes is loaded: one `INSERT … SELECT … GROUP BY` per hourly
   table, then the day's daily tables are rebuilt from its sightings. Hours
   with no files close too, so a gap reads as zero, not as missing. A file that
   arrives late makes its hour dirty and it closes again.
3. **Open hours**: every 5 minutes the hours not closed yet are rewritten into
   `ad_hourly_open`, and their scrapes into `publisher_hourly_open`.
   `tracks_api.ad_hourly_v1` and `tracks_api.scrape_coverage_v2` show both,
   with a `closed` column; an hour is never in both. (Spy's Direction reads
   the open hours' sightings and needs their scrapes beside them.)
4. **Live links**: click links from files under 15 minutes old go into the
   unlogged `live_link` table (40 per host at most). Raposa takes one with
   `tracks_api.take_live_link_v1`, and each link is handed out once.
5. **Keep times**: once an hour it makes partitions ahead and drops days past
   their keep time, only once the day is final (every hour closed, no file
   pending). Drops use `DETACH … CONCURRENTLY`.

A file that fails to load is tried 3 times, then quarantined with an alert
metric; a file that cannot load however often it is tried (a bad checksum, a
day whose sightings were dropped) is quarantined at once. An archive or
database outage does not count against the file. Records that no parser
recognises load as scrapes with outcome `unparsed`, and
`tracks_loader_scrapes_total{outcome="unparsed"}` is the format drift signal.

`replay` marks the raw files of a range pending again; the running loader
does the work, and the numbers stay whole while it runs. When a day in the
range had its sightings dropped, the whole day loads again, because a day's
counts come from all of its sightings. `status -books` checks that the books
balance for the last day and exits non-zero when they don't.

`import-old` copies the collector's history into Tracks, reading the
collector's database (`OLD_DATABASE_URL`) and never writing it: the lookups
by their natural keys, then day by day the hourly and daily counts before
`-before` (the switch-over, a UTC midnight), each day replacing what Tracks
held. Those hours become **imported hours** (`hour_state.imported_at`): the
loader quarantines a file that would touch one and `replay` refuses them.
The collector kept the creative and link, and creative and campaign pairs
only as running totals, so each becomes one row on the last day it was seen.
Running it again replaces what the last run wrote.

Sessions always run in UTC (`load.UTC`): days and hours are UTC days and hours.
The publisher's domain comes from the request itself (Taboola's `u`, the
NewsBreak auction's `site.page`), so a replay does not depend on today's
targets file.

### Tables and keep times

| Table | Kept in the database |
|---|---|
| `raw_file` | forever (one row per archived minute file) |
| `scrape` | 35 days, daily partitions |
| `sighting` | 3 days, daily partitions, BRIN on `seen_at` (the CX43 run's suggestion) |
| `auction` | 14 days, daily partitions |
| `ad_hourly`, `ad_account_brand_hourly`, `publisher_hourly` | monthly partitions, all kept for now |
| `ad_hourly_open`, `publisher_hourly_open` | the hours not closed yet |
| `ad_daily`, `ad_account_daily`, `placement_daily`, `campaign_daily`, `creative_link_daily`, `creative_campaign_daily` | forever |
| lookups (`publisher`, `placement`, `brand`, `account`, `campaign`, `creative`, `ad`, `link`, `network_ad`, `proxy_line`) | forever |
| `live_link` (unlogged) | 15 minutes |

Anything dropped comes back by replay from the archive ([decision
0007](../decisions/0007-keep-times.md)). What other services may read is
published in `tracks_api` and listed in [`contract/sql/tracks/`](../contract/sql/tracks/).

## tracks-bridge

Only for the switch-over ([platform/SWITCH-OVER.md](../platform/SWITCH-OVER.md)),
until the new Spy launches: today's Spy reads the collector's database on
prodbox, and after the switch-over the collector no longer scrapes.

```
tracks-bridge run    -archive s3://adhunters-raw [-from 2026-10-01T00:00:00Z] [-max-lag 10m]
tracks-bridge check  [-from …]
tracks-bridge status [-from …]
tracks-bridge redo   -from 2026-10-01T10:00:00Z -to 2026-10-01T12:00:00Z
```

`run` takes the raw files from `-from` (default `BRIDGE_FROM`) on, oldest
first, parses each with `parse/` and writes its `ok` scrapes into the
collector's `spy` tables with the collector's own `SpyWriter`, ported as it is
from adhunters-collector `e20148c` (`bridge/writer.go`). Each scrape's
`batch_uid` is its capture id and every insert skips rows already there, so a
file written twice counts once. It also files each ad's newest click link in
`spy.walk_queue` (at most every 10 minutes per ad), which feeds the
collector's landing page walker while its sweeper is off.

`tracks.bridge_file` records each file written (scrapes, sightings, attempts,
the last error). A file that fails 3 times is quarantined; `redo` writes a
range again. The collector's database being down does not count against a
file. `check` says whether the collector's database is ready (the table, the
login's rights, the newest scrape by the collector and by the bridge). Metrics:
`tracks_bridge_files_total{outcome}`, `…_scrapes_total`, `…_sightings_total`,
`…_files_pending`, `…_files_quarantined`, `…_lag_seconds`; alerts
`BridgeBehind` and `BridgeFileQuarantined` ([runbook](../runbooks/bridge-behind.md)).
`DATABASE_URL` is the loader's login; `OLD_DATABASE_URL` the collector's owner
login, because the writer creates `spy.sighting` partitions.

Tests run the writer against the collector's schema, dumped into
`internal/collectortest/collector_spy.sql` (the tables it writes, at `e20148c`
plus `042_walk_queue.sql`).

### Not built yet

- `tracks-walker`, with its queue in Postgres, and the landing page tables.
- Replaying into a shadow schema (`replay --into`) to compare a parser change
  before switching.
- Moving hourly counts older than 35 days to Parquet in the archive.
- A small copy of each ad image the first time it is seen. Nothing here rules
  it out: the loader knows when a creative is new.

## Measuring before committing

`measure/` holds the two measurements the design asks for before buying
servers. Neither touches prodbox or the running collector. See
[measure/README.md](measure/README.md).

## Working here

`go test ./...` from this folder. Tests use recorded answers and a local
server; nothing reaches the ad networks. The database tests need
`PG_TEST_URL` (any Postgres 16+ the test may create databases on, for example
`postgres://postgres:test@localhost:5432/postgres?sslmode=disable`) and are
skipped without it. The box setup is in [`platform/servers/`](../platform/servers/).
