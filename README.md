# AdHunters

The AdHunters platform: collecting what ads run on Taboola and NewsBreak,
researching the winners, making creatives, and judging our own campaigns.
One repository, with a walled room per service.

The apps people use are AdHunters Spy, Create, Intel, Funnels and Raposa
([decision 0006](decisions/0006-app-names.md)); each folder has its app's name.

| Folder | What it does | Status |
|---|---|---|
| `kit/` | Shared plumbing for every Go binary: logging, stop handling, `/healthz` and `/metrics`, the Postgres pool, migrations. Nothing domain-specific. | started |
| `contract/` | The data contract: the views each service publishes for others. | tracks_api v1 (scrape_coverage v2), raposa_api v1, spy_api v1 |
| `platform/` | Servers as code (Terraform + HCP Terraform), box setup, deploys, secrets. | Terraform and box setup written, not applied |
| `tracks/` | Collection: capture, shipper, loader, walker. Was adhunters-collector. Internal, no app. | capture in shadow; shipper and loader built |
| `raposa/` | AdHunters Raposa: dark funnel forensics. Investigations, the browser runner, the keeper, watches and plain pages. Replaces the collector's Raposa. | built, not deployed |
| `spy/` | AdHunters Spy: research app and its derived numbers (any range, momentum, Size, Direction). Was adhunters-v4; the numbers move here from the collector. | numbers built, not deployed; app later |
| `intel/` | AdHunters Intel: our campaigns' performance, briefs and alerts. | later |
| `create/` | AdHunters Create: images and headlines. Was auto-creative. | later |
| `funnels/` | AdHunters Funnels: landing pages, funnels and visitor behaviour. | later |

The full design lives at the platform design page, and every rule has a file
in [`decisions/`](decisions/) saying what, why and when.

## Working here

- Go 1.25+. `go.work` ties the modules together: `go test ./kit/... ./tracks/...`.
- Each service is its own Go module. It may import `kit` and `contract`,
  never another service; `scripts/check-walls.sh` enforces it in CI.
- Each service owns one Postgres schema, `<svc>`, and publishes `<svc>_api`
  for everyone else. Migrations live in the service's folder and run at its
  deploy with `lock_timeout = 3s`; `kit/migrate` lints them.
- CI runs on Blacksmith (`.github/workflows/ci.yml`).
- Words mean one thing everywhere: see [GLOSSARY.md](GLOSSARY.md).
