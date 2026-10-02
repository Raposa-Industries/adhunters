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
| `raposa-engine run` | Claims due investigations one visit at a time (10 workers), keeps pages whole, delivers watches through Pushcut, and every 5 minutes refreshes burned lines and queues automatic quick investigations. | ops on `OPS_ADDR` (9105) |
| `raposa-web` | Plain pages: ask for an investigation, follow it, read its visits, variants and evidence, open stored pages, set watches, see burned lines. | `127.0.0.1:8090`, ops on 9106 |
| `browser/runner.js` | Headless Chromium for the browser rungs and the keeper. See [browser/README.md](browser/README.md). | `127.0.0.1:8086` |

`raposa-engine migrate` applies the migrations (the unit runs it before each
start); `request`, `stop` and `status` do the same as the pages from a shell;
`import-old` copies the collector's investigations (below). Units and example
settings are in [deploy/](deploy/); `platform/servers/setup.sh` installs them
on the worker box ([its README](../platform/servers/README.md)).

## How an investigation moves

1. **Asked for** through `raposa_api.request_investigation_v1` (the pages,
   the command, or any other service), or **queued automatically** (below).
   Asking again while one waits or runs returns that one.
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

## Automatic quick investigations

Every 5 minutes the engine tops the queue of automatic quick investigations
up to `quick_queue_depth` (30) with `raposa.queue_quick()`. As in the
collector, Raposa looks at an ad on its own only when its landing page could
not be fetched when it was scraped. It picks a creative when:

- an ad of it on one of `quick_networks` (`taboola`) was seen in the last
  hour, so a live link exists;
- Tracks first saw the creative within `quick_new_days` (7);
- `tracks-walker` tried the landing page of its ads at least
  `quick_walk_failures` (2) times (`tracks_api.walk_page_v1`, step 0) and
  never read it whole: no walk got a usable page (40 words or more, not a bot
  check or an error page). A creative the walker has not tried yet waits for
  it;
- no visit of any investigation of it landed on a usable page;
- none of its investigations waits or runs, and none was asked for within
  `quick_repeat_hours` (24); a stopped one does not count.

Newest creatives first. `quick_max_running` (6) still holds them back so a
deep one someone asked for finds room. Setting `quick_queue_depth` to 0 turns
this off.

A quick investigation climbs only the rungs that cost nothing, and never a
rung on the residential line, whatever that rung's `cost_kb` says: automatic
investigations never spend metered traffic.

## Copying the collector's investigations

`raposa-engine import-old` copies every job of the collector's Raposa
(`spy.raposa_job` and its visits, steps, variants, pages, page files and log)
into the raposa schema. It only reads the collector's database. Each job is
copied in one transaction together with its row in
`raposa.imported_investigation` (the job's old id and uid), so a job is
copied whole or not at all, and running it again copies only what is missing.

```bash
# On the worker box, with raposa-engine's settings. OLD_DATABASE_URL is the
# collector's database (adplatform_v2 on prodbox, reached over Tailscale);
# the BLOB_* lines are the collector's object storage settings (BLOB_ENDPOINT,
# BLOB_BUCKET, BLOB_REGION, BLOB_KEY_ID, BLOB_KEY_SECRET) from its env file.
sudo -u raposa bash -c 'set -a; . /etc/adhunters/raposa-engine.env; . ./collector-blob.env
  export OLD_DATABASE_URL=postgres://USER:PASSWORD@prodbox:5432/adplatform_v2
  /opt/adhunters/bin/raposa-engine import-old -dry-run'   # then again without -dry-run
```

Stop the collector's Raposa first: a job still waiting there is copied as
waiting and runs here, and one running there is copied as stopped.

- **Creatives** are matched on `creative_key`, which Tracks computes the same
  way. A job whose creative Tracks has not seen yet waits for a later run;
  the report counts them and names the first keys. Ads are matched on the
  headline.
- **Pages** are matched on their content hash: a page Raposa has already is
  not copied twice. `page_key` and `text_digest` are filled when the
  collector had none. A page waiting for the collector's keeper waits for
  this one.
- **Files**: small ones come from `spy.raposa_asset.bytes`, large ones from
  the collector's object storage (`-old-files`, by default
  `s3://$BLOB_BUCKET/raposa`), and each goes into the files store under its
  md5 after its bytes are checked. A job whose file cannot be read is not
  copied, and the command exits non-zero naming it.
- **What changes**: the collector's `blocked` outcome becomes `error` with
  the error `blocked`; disguises are matched on their code; `burn_scope` comes
  from the click link's site; a paced retry keeps its release time.
- **Not copied**: evidence (the collector wrote it into Spy's landing page
  visits, which stay where they are), burned lines (rebuilt from the copied
  visits), settings (the report lists the ones the collector had at another
  value) and `spy.raposa_page.prices`, which this Raposa does not keep.

## What it reads and publishes

Reads Tracks only through `tracks_api` (the login needs `tracks_api_read`):
`ad_v1`, `ad_daily_v1`, `creative_link_daily_v1`, `link_v1`,
`creative_campaign_daily_v1`, `campaign_v1`, `sighting_v1`, `publisher_v1`,
`device_v1`, `creative_v1`, `network_ad_v1`, `network_v1`, `walk_page_v1`,
`page_version_v1` and `take_live_link_v1`.

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
  the collector keeps running until this one is. Moving over: set up the
  boxes, stop the collector's Raposa, start `raposa-engine`, then run
  `import-old`.
- **No design.** The pages are plain until the app's design is ready.
