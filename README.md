# AdHunters

The AdHunters platform: collecting what ads run on Taboola and NewsBreak,
researching the winners, making creatives, and judging our own campaigns.
One repository, with a walled room per service.

The apps people use are AdHunters Spy, Create, Intel, Funnels and Raposa
([decision 0006](decisions/0006-app-names.md)); each folder has its app's name.

| Folder | What it does | Status |
|---|---|---|
| `kit/` | Shared plumbing for every Go binary: logging, stop handling, `/healthz` and `/metrics`, the Postgres pool, migrations. Nothing domain-specific. | started |
| `shared/` | Code several services need that is not plumbing, such as an ad network client ([decision 0013](decisions/0013-shared-code.md)). | `taboola`: the Backstage token, sending and retries, used by Launch, Create and Intel; `taboola/write`: the one Taboola client that changes things (Launch only); `frame`: the shell every app's pages sit in; `adsweb`: browser code for building ads; `spool` and `archive`: the raw file writer and the archive (Tracks, Funnels) |
| `contract/` | The data contract: the views each service publishes for others, and the action catalog: what each app lets a teammate (or Desk) do. | tracks_api v1 (scrape_coverage v2), raposa_api v1, spy_api v1, create_api v1, library_api v1, desk_api v1, funnels_api v1; actions of Raposa, Spy, Tracks, Create and the library |
| `platform/` | Servers as code (Terraform + HCP Terraform), box setup, observability (Alloy, alert rules, Telegram routing), deploys, secrets. | Terraform and box setup written; servers partly applied; observability written, accounts not created |
| `runbooks/` | One page per alert: what it means, what to check, how to fix it. | one per alert |
| `tracks/` | Collection: capture, shipper, loader, walker. Was adhunters-collector. Internal, no app. | capture in shadow; shipper and loader built |
| `raposa/` | AdHunters Raposa: dark funnel forensics. Investigations, the browser runner, the keeper, watches and plain pages. Replaces the collector's Raposa. | built, not deployed |
| `spy/` | AdHunters Spy: research app and its derived numbers (any range, momentum, Size, Direction). Was adhunters-v4; the numbers move here from the collector. | numbers built, not deployed; app later |
| `intel/` | AdHunters Intel: our campaigns' performance, briefs and alerts. | RedTrack client and probe, read-only Taboola client and probe; app later |
| `create/` | AdHunters Create: images and headlines. Was auto-creative. | bulk upload page (`create-web`): make options from a brief, choose, pair, download Taboola's bulk sheet; not deployed |
| `funnels/` | AdHunters Funnels: landing sites, journeys through them, and our own VSL player. | edge (hosting, page script, collector) and loader deployed; pages (`funnels-web`) built, not deployed |
| `launch/` | AdHunters Launch: every write on an ad network. The account's groups and campaigns as a tree; new desktop and mobile pairs, paused; move, copy, pause, change; presets, History, drafts. | built on a fake Taboola, not deployed |
| `desk/` | AdHunters Desk: ask for work by talking. Desk reads the apps, proposes a plan, and after a person's OK carries it out through each app's `_api`, the door the app's own buttons use. | conversations, plans, choices, to-dos and the stop switch built, not deployed; makes creatives through Create; waits on Launch's actions |

How to reach, build and deploy the servers: [platform/OPERATIONS.md](platform/OPERATIONS.md).

The full design lives at the platform design page, and every rule has a file
in [`decisions/`](decisions/) saying what, why and when.

## Working here

- Go 1.25+. `go.work` ties the modules together: `go test ./kit/... ./tracks/...`.
- Each service is its own Go module. It may import `kit`, `contract` and
  `shared`, never another service: code a second service needs moves to
  `shared` rather than being copied ([decision 0013](decisions/0013-shared-code.md)).
  `scripts/check-walls.sh` enforces it in CI.
- Each service owns one Postgres schema, `<svc>`, and publishes `<svc>_api`
  for everyone else. Migrations live in the service's folder and run at its
  deploy with `lock_timeout = 3s`; `kit/migrate` lints them.
- CI runs on Blacksmith (`.github/workflows/ci.yml`).
- Words mean one thing everywhere: see [GLOSSARY.md](GLOSSARY.md).
