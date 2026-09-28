# Box setup

`cloud-init.yaml.tftpl` runs once, at a box's first boot: an admin user,
Tailscale, security updates, and `/etc/adhunters/role`. Everything else is
`setup.sh`, run over Tailscale, so the same script sets up a replacement box.

```
sudo ./setup.sh --bin DIR [--role worker|data|standby]
```

`DIR` holds the binaries, built on any machine from the repository root:

```
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(git rev-parse --short HEAD)" -o DIR/ ./tracks/cmd/...
```

It can run again at any time. It changes only what differs, keeps the
previous build of each binary as `NAME.prev`, never overwrites a secrets file
(`/etc/adhunters/*.env`, owner-only) and never deletes data. A unit whose
settings still hold `FILL_ME` is enabled but not started, and the script ends
with a list of what is still to do.

| Role | What it gets |
|---|---|
| worker | `tracks-capture@a` and `@b` (4 workers each), `tracks-shipper` |
| standby | `tracks-capture@standby` (1 worker), `tracks-shipper` |
| data | Postgres 17 (UTC, TLS, sized for a CX43), the `adhunters` database, the `tracks_loader`, `tracks_shipper` and `observe` logins and the `tracks_api_read` role, `tracks-loader` (it runs its migrations each time it starts) |

Every box: timezone UTC, the `tracks` user, `/var/lib/tracks/spool`, the
binaries in `/opt/adhunters/bin`, and Grafana Alloy (from Grafana's apt
repository) with the config in `platform/observe/alloy/`. Alloy scrapes the
`/metrics` of every unit enabled on the box, the host, and on the data box
Postgres through the read-only `observe` login; it starts once
`/etc/adhunters/alloy.env` has no `FILL_ME` left. Every unit also reads
`/etc/adhunters/observe.env` (`SENTRY_DSN`, empty leaves Sentry off). See
`platform/observe/README.md`. The units are in `units/`: each runs as
`tracks`, restarts on failure, gets 45 s to stop, and serves `/healthz` and
`/metrics` on `127.0.0.1` (ports 9101 to 9104).

Capture needs `targets.yaml` (the collector's `config/publishers.yaml`) and
`proxies.env` (its `secrets/proxies.env`) in `/etc/adhunters/tracks-capture/`,
and the box's Primary IP on the proxy provider's allowlist.

**Nothing has been run on a real box.** Not built yet: raising the standby's
workers when the worker box goes quiet, pgBackRest backups, and a home for
secrets (still to be decided); until then the `.env` files are written
by hand on each box.
