# Box setup

`cloud-init.yaml.tftpl` runs once, at a box's first boot: an admin user,
Tailscale, security updates, and `/etc/adhunters/role`. Everything else is
`setup.sh`, run over Tailscale, so the same script sets up a replacement box.

```
sudo ./setup.sh --bin DIR [--role worker|data|standby]
```

How to get onto a box and copy the build there: [../OPERATIONS.md](../OPERATIONS.md).

`DIR` holds the binaries, built on any machine from the repository root:

```
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(git rev-parse --short HEAD)" -o DIR/ ./tracks/cmd/... ./raposa/cmd/... ./platform/observe/cmd/...
```

The script runs from a checkout of the repository: it takes the units in
`units/`, and Raposa's units and browser runner from `raposa/`.

It can run again at any time. It changes only what differs, keeps the
previous build of each binary as `NAME.prev`, never overwrites a secrets file
(`/etc/adhunters/*.env`, owner-only) and never deletes data. A unit whose
settings still hold `FILL_ME` is enabled but not started, and the script ends
with a list of what is still to do.

| Role | What it gets |
|---|---|
| worker | `tracks-capture@a` and `@b` (4 workers each), `tracks-shipper`; Raposa: `raposa-browser`, `raposa-engine`, `raposa-web` |
| standby | `tracks-capture@standby` (1 worker), `tracks-shipper` |
| data | Postgres 17 (UTC, TLS, sized for a CX43), the `adhunters` database, the `tracks_loader`, `tracks_shipper`, `raposa` and `observe` logins and the `tracks_api_read` and `raposa_api_read` roles, `tracks-loader` (it runs its migrations each time it starts), Spy (`spy-numbers` and `spy-web`, with the `spy` and `spy_web` logins and the `spy_api_read` role), `observe-bot` (the 08:00 digest, the Sentry relay, the credit checks and the Taboola policy watch), and pgBackRest: WAL archiving and daily backups to the `adhunters-backups` bucket (`pgbackrest-full.timer` Sundays, `pgbackrest-diff.timer` other days, 03:30 UTC), switched on once its keys are filled in |

Every box: timezone UTC, the `tracks` user, `/var/lib/tracks/spool`, the
binaries in `/opt/adhunters/bin`, and Grafana Alloy (from Grafana's apt
repository) with the config in `platform/observe/alloy/`. Alloy scrapes the
`/metrics` of every unit enabled on the box, the host, and on the data box
Postgres through the read-only `observe` login; it starts once
`/etc/adhunters/alloy.env` has no `FILL_ME` left. Every unit also reads
`/etc/adhunters/observe.env` (`SENTRY_DSN`, empty leaves Sentry off). See
`platform/observe/README.md`. The units are in `units/`: each runs as
`tracks`, restarts on failure, gets 45 s to stop, and serves `/healthz` and
`/metrics` on `127.0.0.1` (the ports are in `ops_ports` in `setup.sh`; 9108 is `tracks-bridge`, installed but only started for the switch-over, see `platform/SWITCH-OVER.md`).

Capture needs `targets.yaml` (the collector's `config/publishers.yaml`) and
`proxies.env` (its `secrets/proxies.env`) in `/etc/adhunters/tracks-capture/` (the proxies log in with a password; no IP allowlist).
Each instance's `/etc/adhunters/tracks-capture@<instance>.env` sets `WORKERS`
and, optionally, `THROTTLE` (the wait after each scrape, default `150ms`).

`share-secrets.sh`, run on the owner's computer, fills in what the worker
and standby boxes share with the data box (the object storage keys and the
`tracks_shipper` and `raposa` database URLs) without showing them; see
[../OPERATIONS.md](../OPERATIONS.md).
`set-sentry-dsn.sh`, the same way, puts the Sentry DSN in every box's
`observe.env` and restarts only the running units that read it.

### Raposa on the worker box

The `raposa` user, `/var/lib/raposa` (with `keep/` and `files/`), node 22
(NodeSource; Ubuntu ships 18 and the runner needs 20), the browser runner in
`/opt/adhunters/raposa-browser` with its packages and the Chromium build
playwright-core names (in `/var/lib/raposa/.cache`), the three units from
`raposa/deploy/`, and their settings in `/etc/adhunters/raposa-*.env`,
written once from the examples. `proxies.env` and `targets.yaml` are copied
once from `/etc/adhunters/tracks-capture/` into `/etc/adhunters/raposa/`.
The engine starts once its settings are filled in: the `raposa` password
the data box setup prints, and keys for the `adhunters-raposa` bucket (which
must exist). Its migrations run from the worker box, over the private
network. `raposa-web` listens on `127.0.0.1:8090`.

The data box gets the `raposa` login (owner of the `raposa` and `raposa_api`
schemas, member of `tracks_api_read`), the `raposa_api_read` role, and a
`pg_hba` line for it from the worker box. Set up the data box first.

**Nothing has been run on a real box.** Not built yet: raising the standby's
workers when the worker box goes quiet, the monthly restore test, and a
home for secrets (still to be decided); until then the `.env` files are written
by hand on each box.
