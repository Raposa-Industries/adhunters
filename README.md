# AdHunters

The AdHunters platform: collecting what ads run on Taboola and NewsBreak,
researching the winners, making creatives, and judging our own campaigns.
One repository, with a walled room per service.

> App names people see are still being chosen. The service names below are
> the design's, and a folder is renamed when its name is settled.

| Folder | What it does | Status |
|---|---|---|
| `kit/` | Shared plumbing for every Go binary: logging, stop handling, `/healthz` and `/metrics`, the Postgres pool, migrations. Nothing domain-specific. | started |
| `contract/` | The data contract: the views each service publishes for others. | empty |
| `platform/` | Servers as code (Terraform + HCP Terraform), box setup, deploys, secrets. | started |
| `tracks/` | Collection: capture, shipper, loader, walker. Was adhunters-collector. | next |
| `raposa/` | Dark funnel forensics. | later |
| `scout/` | Research app and its derived numbers. Was spy (adhunters-v4). | later |
| `compass/` | Our campaigns' performance, briefs and alerts. | later |
| `forge/` | Images and headlines. Was auto-creative. | later |
| `lure/` | Landing pages, funnels and visitor behaviour. | later |

The full design lives at the platform design page, and every rule has a file
in [`decisions/`](decisions/) saying what, why and when.

## Working here

- Go 1.25+. `go.work` ties the modules together: `go test ./kit/...`.
- Each service is its own Go module. It may import `kit` and `contract`,
  never another service; `scripts/check-walls.sh` enforces it in CI.
- Each service owns one Postgres schema, `<svc>`, and publishes `<svc>_api`
  for everyone else. Migrations live in the service's folder and run at its
  deploy with `lock_timeout = 3s`; `kit/migrate` lints them.
- CI runs on Blacksmith (`.github/workflows/ci.yml`).
- Words mean one thing everywhere: see [GLOSSARY.md](GLOSSARY.md).
