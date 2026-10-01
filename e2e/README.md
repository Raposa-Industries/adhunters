# e2e: the path test

`TestFirstCampaignPath` walks one ad along the team's real path, through the
real binaries, built from this repo the way the deploy builds them:

1. **Spy**: a creative, its ad and its vertical seeded into the `tracks` and
   `spy` tables (both migrated by their own services). The test checks that
   Spy's ad page still links to `/create/?from=spy&creative=<id>`.
2. **Create** (`create`, with a fake OpenAI): opens a session from the Spy ad
   (`/create/api/spy/{id}/session`), runs one turn (2 images, 3 headlines)
   and saves the results to the library.
3. **Library** (`library`): keeps the set, which Launch then reads.
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

Every request sent to the fake Taboola is checked against the owner's rules
(2026-10-01):

- nothing is ever switched on (`is_active` is never anything but false);
- every campaign has a daily cap and a total (ENTIRE) spending limit, each
  above 0 and at most $20, with the ceilings left to the code's defaults;
- every group and ad is created paused;
- the campaign carries the page's tracking code.

Asking Launch for more than $20 (as a daily cap, as a total limit, or as a
cap change on a campaign that already exists) must be refused before
anything reaches Taboola.

Each service must also stop cleanly on SIGTERM within 30 s.

## Running it

It runs in CI's `e2e` job. Locally, with any Postgres the test can create
databases on:

    cd e2e && E2E=1 PG_TEST_URL=postgres://postgres@localhost:5432/postgres?sslmode=disable go test -count=1 -v ./...

Without `E2E` it skips, so the `go` job's loop over modules doesn't run it a
second time. Set `E2E_KEEP_DB=1` to keep the database afterwards.

## What it doesn't cover (yet)

- **Google Drive.** The library has no setting that points it at a fake
  Drive yet, so it runs with Drive off: saved pictures wait in the library,
  and Launch reads them from there. The upload itself is untested here.
- **Spy's picture download.** Create fetches an ad's picture only from public
  addresses, so the test adds the picture the way the page offers when that
  fetch fails (an upload).
- **Logins and grants.** Everything runs as one database superuser;
  `platform/servers/setup.sh` owns the per-service logins.
- **Pages.** It drives the same HTTP API the pages call, and checks the
  links between apps in the page code, without running a browser.

It imports no service's module (the walls hold). It talks to each service
over HTTP and the database, as the other services do.
