# Spy

AdHunters Spy finds the ads that are doing well, so we can make our own
like them. This folder holds its derived numbers for now; the app comes once
its design is ready. Every number is sightings per check, built from Tracks'
hourly counts: [METRICS.md](METRICS.md) defines each one, says how precise
it is for any range, and shows how we checked it does not mislead.

The numbers moved here from adhunters-collector (`internal/spy`, its spy
schema 018 to 034, at `e20148c`), which runs them inside the collector
today. The judgements were rebuilt from the Spy metrics handbook; the read
model and Direction were ported.

## What runs

| Binary | Does | Listens |
|---|---|---|
| `spy-numbers run` | Every minute, the last 24 hours (rebuilt only when Tracks closes an hour). Every 5 minutes, the read model, then Size and Direction. | ops on `OPS_ADDR` (9107) |

```
spy-numbers migrate                applies the migrations (the unit runs it before each start)
spy-numbers run
spy-numbers refresh [-rebuild]     every job once; -rebuild redoes Direction's daily part first
spy-numbers import-old             copies the groupings from the collector (OLD_DATABASE_URL, read only)
spy-numbers status
```

`/healthz` fails when the read model or Direction has not succeeded for 20
minutes. Each job is also a kit task (`adhunters_task_*`), promising the
last 24 hours every 90 minutes and the others every 20, so the TaskLate
alert covers them. `/metrics` has `spy_numbers_runs_total{job,outcome}`,
`spy_numbers_seconds{job}`, `spy_numbers_rows{job}`,
`spy_numbers_last_success_timestamp_seconds{job}` and
`spy_recent_window_end_timestamp_seconds`. The unit and example settings
are in [deploy/](deploy/); nothing deploys them yet.

Any other range needs nothing running: the range functions compute it when
asked, from Tracks' counts.

## The numbers

| Part | What | Kept in | Refreshed |
|---|---|---|---|
| Ranges | Presence, share of voice, momentum with its likely range and word, lifespan, launch hit rate, for creatives, operators, verticals and publishers over any start and end | computed when asked | never stored |
| Last 24 hours | The ranges over the last 24 closed hours, stored so lists open fast | `creative_recent`, `operator_recent`, `recent_window` | when Tracks closes an hour |
| Read model | Totals per creative, operator and publisher (today, yesterday, 3, 7 and 30 days), who runs it, its vertical, new, running, junk | `creative_stats`, `operator_stats`, `publisher_stats`, `creative_day` | every 5 minutes |
| Size | Share and rank in its vertical, over 24 hours and 7 days; scaled | `size_stats` | hourly |
| Direction | Rising, steady, fading or stopped right now, against the same hours of the usual weeks, with a sentence for people | `direction_stats`, `direction_event` | every 5 minutes |

### Direction

The last 2 hours (the open hour included) against the same hours and
weekday of the last 3 weeks, on the publishers and devices where the
subject usually runs, for ads, creatives, operators and verticals. The gap
is measured in the subject's own noise (`spy.dispersion`, measured daily),
and entering rising or fading must also pass Benjamini–Hochberg across the
run; leaving uses the old thresholds, so words do not flicker. Every change
is written to `direction_event` for alerts.

The collector's `direction_fill_usual` took about 4 minutes per slot hour
on a CX43 (tracks/measure/hourclose-direction). It now goes through
analysed temporary tables; [measure/](measure/results-20260928.md) has
the timings at Tracks' volume (Direction every 5 minutes: 5 s; a creative
list over a custom range: 10 to 15 s, so the app must run it with a
longer timeout and keep the answer).

### Groupings

Operators, which operator each account belongs to, and each creative's
vertical are still edited in the collector. `spy-numbers import-old` copies
them over (repeatable: each run replaces the last), matching accounts by
network and external id and creatives by creative key; rows Tracks has not
seen yet are skipped and counted in `import_mark`. It refuses to copy an
empty grouping. Operator ids are kept, so OP123 means the same operator
everywhere.

## What it reads and publishes

Reads Tracks only through `tracks_api` (the login needs `tracks_api_read`):
`ad_v1`, `creative_v1`, `account_v1`, `brand_v1`, `publisher_v1`,
`device_v1`, `link_v1`, `sighting_v1`, `ad_hourly_v1`,
`ad_account_brand_hourly_v1`, `scrape_coverage_v2`, `closed_hour_v1`,
`ad_daily_v1`, `ad_account_daily_v1`, `creative_link_daily_v1` and
`creative_campaign_daily_v1`. `scrape_coverage_v2` is new in this change:
it adds the open hours' checks, which Direction needs beside their
sightings.

Publishes `spy_api` (granted to `spy_api_read`), with a copy of each
definition in [contract/sql/spy](../contract/sql/spy):

- Stored: `creative_stats_v1`, `operator_stats_v1`, `publisher_stats_v1`,
  `creative_recent_v1`, `operator_recent_v1`, `recent_window_v1`,
  `size_v1`, `direction_v1`, `direction_event_v1`, `operator_v1`,
  `account_operator_v1`, `creative_vertical_v1`.
- Any range: `range_info_v1`, `creative_range_v1`, `operator_range_v1`,
  `vertical_range_v1`, `publisher_range_v1`.

Settings are rows of `spy.setting`, read on every run: changing one needs
no deploy.

## Working here

Tests need a Postgres the test may create databases on:

```
PG_TEST_URL=postgres://postgres:test@localhost:5432/postgres?sslmode=disable go test ./...
```

`internal/testdb` stands in for the Tracks views as plain tables.
`numbers/checks_test.go` runs the handbook's checks on simulated counts
(about a minute; `-short` skips them). `numbers/volume_test.go` times every
job at Tracks' volume and runs only with `SPY_VOLUME=1`.

Every file in `contract/sql/spy` must appear word for word in a migration
(`migrations_test.go` checks). A published view or function that changes
gets a new version: a new contract file and a new migration.

### Not built yet

- The app.
- Screens to edit groupings; until then the collector stays their source.
- Direction's fading guard for one site (ranges have it; Direction sums its
  usual publishers).
- The nightly checks METRICS.md asks for (stopped accuracy, predictive
  value) once Tracks has weeks of history.
