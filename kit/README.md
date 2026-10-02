# kit

Shared plumbing for every Go binary. Nothing domain-specific lives here; if
it mentions ads, publishers or campaigns, it belongs in a service.

| Package | Gives |
|---|---|
| `logx` | JSON logger with service and version on every line. Error lines are counted by message (`adhunters_log_errors_total`, served by `ops`). With `SENTRY_DSN` set, they also go to Sentry. |
| `errs` | Sentry: off without `SENTRY_DSN`; error-level log lines become events grouped by service, message and the error's shape (numbers, quoted text, URLs and ids masked), tied to the release `service@version`. At most one event an hour per issue and 30 an hour in all leave a process, to keep within the plan. |
| `run` | The stop rule: SIGTERM cancels, 30 s to drain, then exit non-zero. Reports a panic to Sentry and flushes before the exit. |
| `ops` | `/healthz` (named checks) and `/metrics` (Prometheus) on `OPS_ADDR`, Tailscale-only in production, with the count of error lines logged. `Tasks` reports periodic work (last success, duration, rows, promise, runs by result) so two alerts watch every task: late and failing. `OutOfCredit(provider)` counts a paid service refusing us for lack of credit, which pages. `Spent(provider, usd)` counts what a call to a prepaid service cost, for the credit estimates. `HTTP(handler, route)` counts and times a server's requests by route (a short fixed list, never the path) and status. `Transport(provider, rt)` counts and times every call a client makes to an outside service, by status, retries included. `Register` puts metrics of shared code that has no `Server` at hand (pools, `Transport`) on every `/metrics` of the process. |
| `pg` | Postgres pool that refuses to open without an app name, statement timeout and connection cap. Each pool shows on `/metrics` under its app name: connections in use and idle, and how often and how long queries waited for one (`adhunters_db_pool_*`). |
| `keep` | Saves what a service receives from outside (a paid reply, an ad network's answer) whole on disk, one folder per UTC day, before the service reads it (the saved-raw rule). |
| `migrate` | Numbered per-schema migrations with an advisory lock, `lock_timeout = 3s`, checksums, and a linter for locking or destructive changes. |

Tests: `go test ./...`. The migration test needs a throwaway database:
`PG_TEST_URL=postgres://… go test ./migrate/`.

A change here reaches every service, so keep it small and backwards compatible.
