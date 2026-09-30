# shared

Code several services need that is not plumbing
([decision 0013](../decisions/0013-shared-code.md)). A package lands here when
a second service needs it, instead of being copied. It imports only `kit`, and
holds no service's rules: guards, messages for people and where answers are
saved stay in each service.

| Package | Gives | Used by |
|---|---|---|
| `frame` | The Frame: the shell every app's pages sit in (Command Frame, in Ember). Top bar with the app selector, page tabs, ⌘K search and the account; a left column for filters only; G then a letter to switch apps, keeping the Taboola object between Launch and Intel. Its CSS, JS and fonts are built into each binary that mounts `frame.Handler()` under `/<app>/_frame/`; `assets/frame.js` says how a page uses it. | Launch, Create |
| `taboola` | Taboola's Backstage API: the client-credentials token (cached, refetched once on a 401), one request at a time with retries (a 429 always, a 5xx only on requests marked safe, `Retry-After` honoured), every attempt handed to a recorder before it is read, typed errors, and the multipart form for an image upload. | `create/internal/taboola` (create-web), `intel/taboola` (read-only), `intel/taboola/act` (write tests) |

Modules in the workspace (`go.work`) import it without a `require` line.

Tests: `go test ./...` and `node --test frame/test/*.test.js`.
