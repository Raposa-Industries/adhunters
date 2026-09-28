# kit

Shared plumbing for every Go binary. Nothing domain-specific lives here; if
it mentions ads, publishers or campaigns, it belongs in a service.

| Package | Gives |
|---|---|
| `logx` | JSON logger with service and version on every line. |
| `run` | The stop rule: SIGTERM cancels, 30 s to drain, then exit non-zero. |
| `ops` | `/healthz` (named checks) and `/metrics` (Prometheus) on `OPS_ADDR`, Tailscale-only in production. |
| `pg` | Postgres pool that refuses to open without an app name, statement timeout and connection cap. |
| `migrate` | Numbered per-schema migrations with an advisory lock, `lock_timeout = 3s`, checksums, and a linter for locking or destructive changes. |

Tests: `go test ./...`. The migration test needs a throwaway database:
`PG_TEST_URL=postgres://… go test ./migrate/`.

A change here reaches every service, so keep it small and backwards compatible.
