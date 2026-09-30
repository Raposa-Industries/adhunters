# shared

Code several services need that is not plumbing
([decision 0013](../decisions/0013-shared-code.md)). A package lands here when
a second service needs it, instead of being copied. It imports only `kit`, and
holds no service's rules: guards, messages for people and where answers are
saved stay in each service.

| Package | Gives | Used by |
|---|---|---|
| `archive` | Where raw files live for good: a bucket in object storage (`s3://`) or a folder (`file://`). A key is written once and never replaced by different bytes; every put is checked by size and MD5. Moved from `tracks/archive` when Funnels needed it. | `tracks` (shipper, loader, bridge), `funnels` (loader) |
| `taboola` | Taboola's Backstage API: the client-credentials token (cached, refetched once on a 401), one request at a time with retries (a 429 always, a 5xx only on requests marked safe, `Retry-After` honoured), every attempt handed to a recorder before it is read, typed errors, and the multipart form for an image upload. | `create/internal/taboola` (create-web), `intel/taboola` (read-only), `intel/taboola/act` (write tests) |

Modules in the workspace (`go.work`) import it without a `require` line.

Tests: `go test ./...`.
