# shared

Code several services need that is not plumbing
([decision 0013](../decisions/0013-shared-code.md)). A package lands here when
a second service needs it, instead of being copied. It imports only `kit`, and
holds no service's rules: guards, messages for people and where answers are
saved stay in each service. One exception: `taboola/write`, the client that
changes things on Taboola, carries its guards with it, because every write
must pass the same ones; only Launch may import it (and create-web until
Launch replaces it), which `scripts/check-walls.sh` enforces.

| Package | Gives | Used by |
|---|---|---|
| `frame` | The Frame: the shell every app's pages sit in (Command Frame, in Ember). Top bar with the app selector, page tabs, ⌘K search and the account; a left column for filters only; G then a letter to switch apps, keeping the Taboola object between Launch and Intel. Its CSS, JS and fonts are built into each binary that mounts `frame.Handler()` under `/<app>/_frame/`; `assets/frame.js` says how a page uses it. | Launch, Create |
| `taboola` | Taboola's Backstage API: the client-credentials token (cached, refetched once on a 401), one request at a time with retries (a 429 always, a 5xx only on requests marked safe, `Retry-After` honoured), every attempt handed to a recorder before it is read, typed errors, and the multipart form for an image upload. | `taboola/write`, `intel/taboola` (read-only), `intel/taboola/act` (write tests) |
| `taboola/write` | The Taboola client that changes things: groups, campaigns (made, copied into a group, paused, bid and caps changed), image uploads and ads, all made paused, with the guards (allowed accounts only, never a network account, ceilings on bid and caps, only-own on a lent login) and every exchange kept raw (`kit/keep`). `SettingsFromEnv` reads the `TABOOLA_*` variables. | Launch (`launch/internal/network/taboola`), create-web until Launch replaces it |
| `adsweb` | Browser code for building ads, served by `adsweb.Handler()` under `/<app>/_ads/`: pairing (Sortido, every combination), Taboola's title, image and link warnings, ad ids, the bulk sheet in Realize's own template (`realize-base.xlsx`, made by `tools/realize-base.mjs`) and the ZIP of images, and the tracker link split. | Launch, create-web |
| `files` | Content-addressed file stores (`files/<md5>`): a folder, or an S3 bucket (Hetzner Object Storage) from `S3_*`. Moved from Raposa when the library needed it. | `raposa` (pages kept whole), `library` (safe copies) |
| `text` | `CleanLine`: removes the hidden characters Taboola rejects and folds whitespace. | `library` (headlines) |

Modules in the workspace (`go.work`) import it without a `require` line.

Tests: `go test ./...`, `node --test frame/test/*.test.js` and `node --test adsweb/test/*.test.js`.
