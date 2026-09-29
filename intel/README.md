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
- Without `-account` the probe reads the credentials' own account.
- Reports cover the last `-days` days in the account's time zone. The splits
  read: day, campaign, campaign by day, site, campaign by site by day,
  country, platform, hour of day, campaign by hour, and item.
- A failed read is recorded in the summary and the probe carries on.

Tests: `go test ./...` (against a stand-in server; no network).
