# Raposa

AdHunters Raposa finds an ad's dark funnel: the pages an operator shows real
readers and hides from an ad network reviewer. It visits the ad the way a
reader would, climbing a ladder of disguises until a visit sees a dark page,
then samples that rung to measure each variant. Words are in
[GLOSSARY.md](../GLOSSARY.md#raposa).

Ported from adhunters-collector e20148c (`internal/raposa`, its database code
and `browser/`), with the work held differently
([decision 0008](../decisions/0008-raposa-one-visit-per-claim.md)).

## What runs

| Binary | Does | Listens |
|---|---|---|
| `raposa-engine run` | Claims due investigations one visit at a time (10 workers), keeps pages whole, delivers watches through Pushcut, refreshes burned lines every 5 minutes. | ops on `OPS_ADDR` (9105) |
| `raposa-web` | Plain pages: ask for an investigation, follow it, read its visits, variants and evidence, open stored pages, set watches, see burned lines. | `127.0.0.1:8090`, ops on 9106 |
| `browser/runner.js` | Headless Chromium for the browser rungs and the keeper. See [browser/README.md](browser/README.md). | `127.0.0.1:8086` |

`raposa-engine migrate` applies the migrations (the unit runs it before each
start); `request`, `stop` and `status` do the same as the pages from a shell.
Units and example settings are in [deploy/](deploy/).

## How an investigation moves

1. **Asked for** through `raposa_api.request_investigation_v1` (the pages,
   the command, or any other service). Asking again while one waits or runs
   returns that one.
2. **Prepare**: read the ad from Tracks: its saved link, device, campaign,
   account and the publisher it ran on.
3. **Baseline**: the reviewer rung loads the saved link to learn the white
   page (another line if one fails, up to 3), then once more: a white page
   that differs on every load is compared on its domain and title only.
4. **Climb**: each rung gets `ladder_tries_per_rung` visits with a live link
   taken from Tracks (`take_live_link_v1`, each link once). The first rung
   whose visit sees a dark page is the breach rung.
5. **Sample** (deep only): `visits_target` visits on the breach rung, spread
   over `visits_window_minutes`, following the funnel, each paired with a
   reviewer visit at the same moment to see whether the white page is split
   tested too.
6. **Finish**: variants, confidence, evidence, a `finished` event; a deep one
   that found nothing is queued again, up to `retry_limit` attempts in all.

Each step is one claim: the worker writes the visit, its pages, the log, the
events and the progress in one transaction fenced by its claim token, and
lets go. A stopping engine writes nothing for the visit in flight; it runs
again at the next claim, here or on another box. A step that fails for a
reason that is not the page's is tried again in 30 s, and after 5 in a row
the investigation fails with the reason. With no live link on hand a visit
waits (up to `live_link_wait_seconds`) without recording anything.

Settings (`raposa.setting`) and the ladder (`raposa.disguise`) are read on
every visit: changing them needs no deploy.

## What it reads and publishes

Reads Tracks only through `tracks_api` (the login needs `tracks_api_read`):
`ad_v1`, `ad_daily_v1`, `creative_link_daily_v1`, `link_v1`,
`creative_campaign_daily_v1`, `campaign_v1`, `sighting_v1`, `publisher_v1`,
`device_v1` and `take_live_link_v1`.

Publishes `raposa_api` (granted to `raposa_api_read`), with a copy of each
definition in [contract/sql/raposa](../contract/sql/raposa):
`investigation_v1`, `page_v1`, `variant_v1`, `line_burn_v1`, `evidence_v1`,
`request_investigation_v1` and `stop_investigation_v1`. Evidence is how Spy
and Intel will learn who runs an ad; Raposa writes nothing into them.

## Pages and files

A visit stores each page's HTML and text as a version (a changed page is a
new version, a page that only changed its noise is not). Each new version a
deep investigation reached is queued for the keeper, which opens it once in
the browser from the stored HTML, over the line the visit used, and stores
its files, the page after its scripts ran, and its video in the files store
(`RAPOSA_FILES`: object storage in production, a folder otherwise) under
`files/<md5>`. Plain video files are streamed to disk by the engine.

raposa-web serves stored pages and files sandboxed and without loading
anything from outside, so opening one runs none of its scripts and tells the
operator nothing; they look unstyled for that reason.

## Burned lines

A line is burned for a site when, on one rung in the last `burn_window_days`,
it made `burn_min_visits` visits there that all saw the white page while
other lines on the same rung saw the dark page on `burn_other_dark_pct` of
theirs. Investigations of that site skip it; a line burned for
`burn_general_sites` sites is skipped everywhere.

## Watches

A watch follows one investigation (and its retries), every investigation of
a creative, or everything, for some of `started`, `dark_found` and
`finished`. Each event is sent once to the Pushcut notification the watch
names, with 3 tries. Without `PUSHCUT_API_KEY` deliveries are recorded as
skipped. System alerts never go through here; they go to Telegram.

## Tests

```bash
PG_TEST_URL="postgres://postgres:test@localhost:5432/postgres?sslmode=disable" go test -race ./...
cd browser && npm ci && npm test   # needs Chromium
```

The engine tests run whole investigations against a local cloaked site and a
stand-in for the Tracks views; the web tests drive the pages over a real
database.

## Not in this yet

- **Not deployed.** Nothing here has run on a real box, and the old Raposa in
  the collector keeps running until this one is. The units in `deploy/` are
  not wired into `platform/servers/setup.sh` yet (the `raposa` user and
  login, `/var/lib/raposa`, the env files, pg_hba for the worker box).
- **Old investigations are not copied** from `spy.raposa_*`. They stay in the
  old database, untouched.
- **Automatic quick investigations** (queued for new ads without anyone
  asking) wait for Tracks to say which ads are new.
- **No design.** The pages are plain until the app's design is ready.
