# kit

Shared plumbing for every Go binary. Nothing domain-specific lives here; if
it mentions ads, publishers or campaigns, it belongs in a service.

| Package | Gives |
|---|---|
| `logx` | JSON logger with service and version on every line. With `SENTRY_DSN` set, error lines also go to Sentry. |
| `errs` | Sentry: off without `SENTRY_DSN`; error-level log lines become events grouped by service and message, tied to the release `service@version`. |
| `run` | The stop rule: SIGTERM cancels, 30 s to drain, then exit non-zero. Reports a panic to Sentry and flushes before the exit. |
| `ops` | `/healthz` (named checks) and `/metrics` (Prometheus) on `OPS_ADDR`, Tailscale-only in production. `Tasks` reports periodic work (last success, duration, rows, promise) so one alert watches every task. `OutOfCredit(provider)` counts a paid service refusing us for lack of credit, which pages. `Spent(provider, usd)` counts what a call to a prepaid service cost, for the credit estimates. |
| `pg` | Postgres pool that refuses to open without an app name, statement timeout and connection cap. |
| `keep` | Saves what a service receives from outside (a paid reply, an ad network's answer) whole on disk, one folder per UTC day, before the service reads it (the saved-raw rule). |
| `migrate` | Numbered per-schema migrations with an advisory lock, `lock_timeout = 3s`, checksums, and a linter for locking or destructive changes. |

Tests: `go test ./...`. The migration test needs a throwaway database:
`PG_TEST_URL=postgres://… go test ./migrate/`.

A change here reaches every service, so keep it small and backwards compatible.
