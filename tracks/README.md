# tracks

Collection: what ads run on Taboola and NewsBreak. It replaces
adhunters-collector, one binary at a time. Internal: no app of its own.

| Binary | What it does | Status |
|---|---|---|
| `tracks-capture` | Scrapes the feeds and writes every answer, as received, to the spool. No database. | shadow run |
| `tracks-shipper` | Uploads sealed raw files to object storage and records them. | later |
| `tracks-loader` | Parses raw files into sightings, closes each hour into counts, replays any range. | later |
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
  network and day, and what the archive would grow by at the live rate.

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

## Measuring before committing

`measure/` holds the two measurements the design asks for before buying
servers. Neither touches prodbox or the running collector. See
[measure/README.md](measure/README.md).

## Working here

`go test ./...` from this folder. Tests use recorded answers and a local
server; nothing reaches the ad networks.
