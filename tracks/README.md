# tracks

Collection: what ads run on Taboola and NewsBreak. It replaces
adhunters-collector, one binary at a time. Internal: no app of its own.

| Binary | What it does | Status |
|---|---|---|
| `tracks-capture` | Scrapes the feeds and writes every answer, as received, to the spool. No database. | shadow run |
| `tracks-shipper` | Uploads sealed raw files to the archive and records them. | built, not deployed |
| `tracks-loader` | Parses raw files into sightings, closes each hour into counts, replays any range. | built, not deployed |
| `tracks-bridge` | Writes Tracks' scrapes into the old collector's database, so today's Spy keeps working after the switch-over. | built, not deployed |
| `tracks-walker` | Walks running ads' saved links to their landing pages and one next step, and keeps what each page says. | built, not deployed |

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

One file per instance, network and minute (an instance name is lowercase
words joined by `-`, such as the walker's host name `adhunters-worker`; a
writer refuses any other):
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
tracks-loader hourly status|check|drop [-month 2026-09] [-keep 35]
tracks-loader walks check|drop
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
6. **Hour files**: every 5 minutes it writes one final day older than 7 days
   to the archive as hour files (below), oldest first, and again when a
   replay closed it since.
7. **Bring back**: every 10 seconds it loads an archived day a page asked
   for back into the hourly tables, and once an hour drops archived days
   nobody has asked for in 3 days.

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
| `auction` | 14 days, daily partitions; published as `tracks_api.auction_v1`, which Spy sums per day and keeps |
| `ad_hourly`, `ad_account_brand_hourly` | monthly partitions; 35 days after its month ends, a month may move to hour files in the archive (`tracks-loader hourly drop`) |
| `publisher_hourly` | forever (one row per publisher, device and hour) |
| `hour_file`, `archived_month`, `hour_bring_back` | forever (what is in the archive, which months left, which days came back) |
| `ad_hourly_open`, `publisher_hourly_open` | the hours not closed yet |
| `ad_daily`, `ad_account_daily`, `placement_daily`, `campaign_daily`, `creative_link_daily`, `creative_campaign_daily` | forever |
| lookups (`publisher`, `placement`, `brand`, `account`, `campaign`, `creative`, `ad`, `link`, `network_ad`, `proxy_line`) | forever |
| `live_link` (unlogged) | 15 minutes |
| `walk`, `walk_step`, `page_url`, `page_version`, `walk_file`, `walk_state` | forever (a walk's pages take about 150 bytes each, each URL kept once; bodies live only in the archive) |
| `walk_page` | the walk pages as they were kept before 2 Oct 2026, with their URLs in full, until `tracks-loader walks drop` (see Walk pages below) |

Anything dropped comes back by replay from the archive ([decision
0007](../decisions/0007-keep-times.md)). What other services may read is
published in `tracks_api` and listed in [`contract/sql/tracks/`](../contract/sql/tracks/).

### Hour files

Hourly counts leave the database 35 days after their month ends, as hour
files in the archive ([decision 0023](../decisions/0023-hourly-counts-to-the-archive.md)).
An hour file is one UTC day of `ad_hourly` or `ad_account_brand_hourly` as
Parquet (zstd), at `hourly/<table>/<yyyy>/<mm>/<dd>-v<n>.parquet`: the same
columns and types as the table, sorted by hour and key, readable by DuckDB
or pandas as it is. A real day's 1.5 million rows take about 10 s to write.
Before a file is recorded in `tracks.hour_file`, the loader reads the
archive's copy back and compares every hour with the database: rows,
sightings, and a fingerprint of every value of every row. A day closed
again after its file was written (a replay) is written again as the next
version; older versions stay.

```
tracks-loader hourly status
tracks-loader hourly check -month 2026-09
tracks-loader hourly drop  -month 2026-09 [-keep 35]
```

`status` lists each month: days, days written to hour files, whether it is
still in the database, its size, days brought back, and the first day it
may go. `check` compares a month with its hour files again, day by day and
hour by hour (about 9 s a day), and prints each day's rows and sightings in
the database and in its files, the hours that differ, and the daily counts
(which stay) against the hourly sightings. It exits non-zero unless every
hour matches. `drop` runs the same check and, only when everything matches
and the month ended `-keep` days ago, records it in `archived_month` and
drops its two hourly partitions (`DETACH … CONCURRENTLY`); it prints the
space freed and the month's daily counts before and after. The first drop
of a month is run by a person, on the data box, from the deploy's build
folder or the installed binary:

```
sudo bash -c 'set -a; . /etc/adhunters/tracks-loader.env; /opt/adhunters/bin/tracks-loader hourly check -month 2026-09'
```

`tracks_api.hourly_days_v1(from, to, bring_back)` says, per UTC day, whether
its hourly counts are in the `database`, in the `archive` or `coming`. With
`bring_back` true, the archived days of the range (15 at most) are asked
for: the loader copies their hour files back into the hourly tables within
a minute or two, checks the rows and sightings against `hour_file`, and
keeps them until nobody has asked for 3 days. Then, once an hour, it runs
the month's check again over the days back in the database and drops them;
a day a replay closed again waits for its new hour file. Daily counts and
`publisher_hourly` never leave the database. Metrics:
`tracks_loader_hour_files_total{outcome}`, `tracks_loader_hour_days_waiting`,
`tracks_loader_hours_brought_back_total{outcome}`.

### Walk pages

Each page of a walk is a `walk_step` row whose two URLs point at `page_url`,
where each URL is kept once ([decision 0025](../decisions/0025-walk-urls-kept-once.md)):
the same links come back walk after walk, and in full they were two thirds
of each row. Tracks migration 12 copied every saved walk page out of the old
`walk_page` table, and until that table goes, a trigger copies anything still
written to it (a tracks-walker not updated yet). Migration 13 points
`tracks_api.walk_page_v1` at the copy: same columns, rows and values.

```
tracks-loader walks check
tracks-loader walks drop
```

`check` compares every `walk_page` row with its copy, every value, the URLs
as text (about 15 s for a million rows), and prints the rows, the rows
copied, the rows that differ and each table's size. It exits non-zero
unless every row matches. `drop` runs the same check while holding
`walk_page`'s writers back and, only when everything matches and the view
reads the copy, drops `walk_page` and its trigger and prints the space
freed. A person runs it, on the data box:

```
sudo bash -c 'set -a; . /etc/adhunters/tracks-loader.env; /opt/adhunters/bin/tracks-loader walks check'
```

## tracks-walker

```
tracks-walker run    -archive s3://adhunters-raw [-lines proxies.env] [-workers 2] [-rewalk 6h] [-timeout 15s]
tracks-walker replay -archive s3://adhunters-raw -from 2026-10-01T00:00:00Z -to 2026-10-02T00:00:00Z
tracks-walker walk   -url https://… [-referer https://publisher/] [-lines proxies.env]
tracks-walker import-old
```

`run` reads `tracks.walks_due`: ads seen in the last hour with a saved link,
never walked or due again (6 hours after a walk that reached the landing page;
after a failure 15 minutes, doubling up to 6 hours), newest first. Each walk
follows the link as a desktop Chrome would, with the publisher's page as
referer, through up to 10 redirects to the landing page, then one next step
through the page's main button with the same cookies (the collector's funnel
walker at `e20148c`, and the same page reader Raposa uses, now in
[`shared/page`](../shared/page/)). Bodies are cut at 500 KB.

It uses the same proxy lines file as capture and only its `dc` and `isp`
lines (today dc-us-5 and isp-10), never the box's own address. Every walk is
written whole (all hops, headers kept, bodies) to its own spool,
`/var/lib/tracks/walk-spool/walk/<yyyy>/<mm>/<dd>/<hh>/…ndjson.zst`, before
anything reaches the database; nothing is saved that is not in a raw file.
Every 30 seconds it uploads sealed files to the archive under `walk/` and
lists them in `tracks.walk_file`. `replay` parses a range of those files again
and replaces what they wrote, so a parser change reaches old walks.

What parsing keeps: `walk` (one per walk, by the record's ULID), `walk_step`
(each step's address and final address, as ids in `page_url`, where each URL
is kept once; status, redirects, page type, checkout platform and seller), and `page_version` (each distinct content once: title, headings,
meta, favicon, pixels, emails, phones, company names, disclaimers, VSL, and
the visible text up to 20,000 characters). `walk_state` holds when each ad may
be walked again. Published as `tracks_api.walk_page_v1` and
`tracks_api.page_version_v1`, which Spy reads to group operators and to
classify from the landing page.

`walk` walks one link and prints the pages without bodies and what was read
from them; nothing is saved. Without `-lines` it goes from the machine's own
address, so use that only on a laptop.

`import-old` copies the collector's landing pages (its funnel walker's and
its quick investigations', `spy.landing_page_version` and
`spy.landing_page_use`) into walks, reading `OLD_DATABASE_URL` and never
writing it. The collector kept each page's versions and, apart, which
creative and ad led to the page; so each such use becomes one walk per
version seen while the use was (record id `old-<use>-<version>`, dated the
last time both were seen), with the page as step 0 (status 200: the
collector kept only pages that answered) and the first page past it it
recorded as step 1, without content. A use without an ad gets its
creative's most seen ad, and each walk its creative's most clicked link.
Versions are hashed as `run` hashes its own. Running it again replaces the
copied walks. It needs tracks-loader's login (it reads creatives, ads and
counts), and runs after `tracks-loader import-old`; then
`spy-numbers refresh -pages-since` with a date before the collector's first
page brings them into Spy. On the data box, from the deploy's build folder
(the walker itself is installed only on the worker):

```
cd ~/adhunters && sudo bash -c 'set -a; . /etc/adhunters/tracks-loader.env; . /etc/adhunters/tracks-bridge.env; ./bin/tracks-walker import-old'
```

`DATABASE_URL` is the `tracks_walker` login: it reads `sighting`, `link` and
`publisher` and writes only the walk tables (grants in `0010_walks.sql` and
`0012_walk_urls_once.sql`, applied when the role exists). It does not start
before the data box's tracks-loader has applied migration 12: update
tracks-loader first. The archive's keys are set as for
`tracks-shipper`. Metrics: `tracks_walker_walks_total{outcome,step}`,
`tracks_walker_walk_seconds`, `tracks_walker_due`,
`tracks_walker_files_archived_total`. The unit is
[`tracks-walker.service`](../platform/servers/units/tracks-walker.service),
installed on the worker box by `setup.sh` (settings in
`/etc/adhunters/tracks-walker.env`, ops on 9119).

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

- Replaying into a shadow schema (`replay --into`) to compare a parser change
  before switching.
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
