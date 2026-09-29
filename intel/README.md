# intel

AdHunters Intel: our own campaigns' performance, briefs and alerts. The app
is not built yet (the build order puts it after Raposa). What is here is the
first data source it will use.

| Package | Gives |
|---|---|
| `taboola` | A read-only client for Taboola's Backstage API: token handling, retries on 429 and 5xx (honouring `Retry-After`), and the reads (account, campaigns, items, reports). Answers come back raw so they are saved before they are read. |
| `cmd/intel-taboola` | `probe`: calls every read once for one account and writes the raw answers plus `summary.md`. |

## Read-only, on purpose

The client's transport refuses every request except GETs under
`/backstage/api/1.0/` and the token POST, before it leaves the process. No
code here can create, change, pause or delete anything on Taboola. Writing
comes later, in its own client, once the owner says so.

## Running the probe

    export TABOOLA_CLIENT_ID=… TABOOLA_CLIENT_SECRET=…
    go run ./intel/cmd/intel-taboola probe -out /tmp/taboola-probe [-account acme-sc] [-days 30] [-items 5]

- The credentials come from Taboola (Backstage API client id and secret). They
  live in environment variables, never in files in the repo or in chat. The
  token is never written.
- The host `backstage.taboola.com` must be reachable. Cloud sessions need it
  on the environment's allowed domains.
- Without `-account` the probe reads the credentials' own account. A
  network account lists no campaigns itself, so campaigns and items are also
  read from every advertiser account the credentials may read.
- Reports cover the last `-days` days in the account's time zone. The splits
  read: day, campaign, campaign by day, site, campaign by site by day,
  country, platform, hour of day, campaign by hour (the last 2 days only:
  Backstage allows 48 hours for it), and item.
- Backstage allows 84 requests a minute per client (`Ratelimit-Policy`).
  Its reports refresh about hourly. Campaign by site by day is big (43 MB
  for 30 days of one busy network account).
- A failed read is recorded in the summary and the probe carries on.

Tests: `go test ./...` (against a stand-in server; no network).

## Before building more here

What the first read test found, and what is still unknown, is in the
project's shared files (`research/taboola-api/findings.md`). Anything in
Intel beyond this client and probe waits for the Intel replan the owner
asked for on 2026-09-29, which reads those findings first. Two facts that
shape it: Taboola has everything before the click and the real cost,
RedTrack everything after it; joining them needs the campaign, item and
site ids on both sides.
