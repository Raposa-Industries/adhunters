# e2e: the path test

`TestFirstCampaignPath` walks one ad along the team's real path, through the
real binaries, built from this repo the way the deploy builds them:

1. **Spy**: a creative, its ad and its vertical seeded into the `tracks` and
   `spy` tables (both migrated by their own services). The test checks that
   Spy's ad page still links to `/create/?from=spy&creative=<id>`.
2. **Create** (`create`, with a fake OpenAI): opens a session from the Spy ad
   (`/create/api/spy/{id}/session`), runs one turn (2 images, 3 headlines)
   and saves the results to the library.
3. **Library** (`library`, with a fake Google Drive): keeps the set, and its
   Drive pass uploads the pictures under StepNutra › vertical › set. After
   that only Drive holds the bytes (decision 0020), so Launch's copy of each
   picture is read back from Drive.
4. **Launch** (`launch-web`, with a fake Taboola that records every
   request): reads the set, brings in its pictures (`library/use`), and sends
   a new group with a desktop and a mobile campaign carrying the library's
   ads. The campaign settings are the page's own defaults, read from `TEAM`
   in `launch/web/pages/presets.js`.
5. **Intel** (`intel-numbers once`): Taboola's and RedTrack's numbers for those
   campaigns are seeded as intel-collect leaves them. RedTrack's sub slots
   are filled from the tracking code Launch actually sent, the way a click
   fills them. Launch's `/launch/api/numbers` must then show the spend, sales
   and revenue per campaign and per ad.

Launch runs as the box does since the owner's word of 2026-10-02
(`TABOOLA_MAX_DAILY_CAP=500`, `TABOOLA_MAX_SPEND_LIMIT=0`,
`TABOOLA_CREATE_ACTIVE=1`). Every request sent to the fake Taboola is checked:

- only a new group, campaign or ad is switched on (`is_active` true); no
  other request sends anything but false;
- every campaign has a daily cap above 0 and at most $500, the team's total
  limit (none), and the page's tracking code.

Asking Launch for more than the ceilings (a daily cap over $500, a total
over 30 daily caps, or a cap change over $500 on a campaign that already
exists) must be refused before anything reaches Taboola.

Each service must also stop cleanly on SIGTERM within 30 s.

## Running it

It runs in CI's `e2e` job. Locally, with any Postgres the test can create
databases on:

    cd e2e && E2E=1 PG_TEST_URL=postgres://postgres@localhost:5432/postgres?sslmode=disable go test -count=1 -v ./...

Without `E2E` it skips, so the `go` job's loop over modules doesn't run it a
second time. Set `E2E_KEEP_DB=1` to keep the database afterwards.

## What it doesn't cover (yet)

- **Spy's picture download.** Create fetches an ad's picture only from public
  addresses, so the test adds the picture the way the page offers when that
  fetch fails (an upload).
- **Logins and grants.** Everything runs as one database superuser;
  `platform/servers/setup.sh` owns the per-service logins.
- **Pages.** It drives the same HTTP API the pages call, and checks the
  links between apps in the page code, without running a browser.

It imports no service's module (the walls hold). It talks to each service
over HTTP and the database, as the other services do.
