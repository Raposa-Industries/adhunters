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
| `archive` | Where raw files live for good: a bucket in object storage (`s3://`) or a folder (`file://`). A key is written once and never replaced by different bytes; every put is checked by size and MD5. Moved from `tracks/archive` when Funnels needed it. | `tracks` (shipper, loader, bridge, walker), `funnels` (loader) |
| `spool` | Raw files on local disk: lines appended to one file per stream and minute, compressed with zstd when the minute ends, leftovers of a crash sealed at the next start; `ReadLines` reads a sealed file back. No database. Moved from `tracks/capture/spool` when Funnels needed it. | `tracks/capture/spool` (capture, walker), `funnels/edge` (page events), `funnels/load` |
| `frame` | The Frame: the shell every app's pages sit in (Command Frame, in Ember). Top bar with the app selector, page tabs, ⌘K search and the account; a left column for filters only; G then a letter to switch apps, keeping the Taboola object between Launch and Intel. Its CSS, JS and fonts are built into each binary that mounts `frame.Handler()` under `/<app>/_frame/`; `assets/frame.js` says how a page uses it. | Launch, Create, Intel, Desk |
| `telegram` | Posting an HTML message to the "AdHunters alerts" Telegram group through the bot, the token kept out of every error. | `platform/observe` (observe-bot), `intel` (intel-numbers' alerts) |
| `page` | Reading a landing page's HTML: the main button (call to action) and adding a click id to it, the checkout platform and seller, tracking pixels and ids, and a page's title, headings, meta, contacts, company names, disclaimers, VSL and visible text. Moved from Raposa when Tracks' walker needed it. | `raposa/internal/engine`, `tracks/walk` |
| `taboola` | Taboola's Backstage API: the client-credentials token (cached, refetched once on a 401), one request at a time with retries (a 429 always, a 5xx only on requests marked safe, `Retry-After` honoured), every attempt handed to a recorder before it is read, typed errors, and the multipart form for an image upload. | `taboola/write`, `intel/taboola` (read-only), `intel/taboola/act` (write tests) |
| `taboola/write` | The Taboola client that changes things: groups, campaigns (made, copied into a group, paused, bid and caps changed), image uploads and ads, all made paused, with the guards (allowed accounts only, never a network account, ceilings on bid and caps, only-own on a lent login) and every exchange kept raw (`kit/keep`). `SettingsFromEnv` reads the `TABOOLA_*` variables. | Launch (`launch/internal/network/taboola`), create-web until Launch replaces it |
| `adsweb` | Browser code for building ads, served by `adsweb.Handler()` under `/<app>/_ads/`: pairing (Sortido, every combination), Taboola's title, image and link warnings, ad ids, the bulk sheet in Realize's own template (`realize-base.xlsx`, made by `tools/realize-base.mjs`) and the ZIP of images, and the tracker link split. | Launch, create-web |
| `files` | Content-addressed file stores (`files/<md5>`): a folder, or an S3 bucket (Hetzner Object Storage) from `S3_*`. Moved from Raposa when the library needed it. | `raposa` (pages kept whole), `library` (safe copies) |
| `text` | `CleanLine`: removes the hidden characters Taboola rejects and folds whitespace. | `library` (headlines) |
| `verticals` | Our fixed list of verticals, grouped by category (`verticals.yaml`), and its loader. Moved from Spy when Create needed the list too. | `spy` (classifier, pages), `create` (a session's vertical) |
| `access` | Cloudflare Access's login check: the signed token Access puts on every request (RS256, the application's AUD tag, the team as issuer, not expired), its keys read from the team's certs every hour; `Email(r)` is who is asking, and a request without a valid token is refused, whatever headers it carries. `FromEnv` reads `ACCESS_TEAM` and `ACCESS_AUD`. Moved from Spy when Desk needed it. | `spy` (spy-web), `desk` (desk-web) |

Modules in the workspace (`go.work`) import it without a `require` line.

Tests: `go test ./...`, `node --test frame/test/*.test.js` and `node --test adsweb/test/*.test.js`.
