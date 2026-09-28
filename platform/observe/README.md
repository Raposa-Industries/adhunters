# Observability

Knowing the platform works without watching it: metrics, logs and errors go
to hosted services outside Hetzner, alerts go to the Telegram group
"AdHunters alerts" (decision 0004), and a heartbeat proves the alerting
itself works (decision 0009).

| Piece | Where | What it does |
|---|---|---|
| Grafana Alloy | every box, `alloy/` here, installed by `platform/servers/setup.sh` | Scrapes every service's `/metrics`, the host and (data box) Postgres every 60 s; ships the journal of our units. Buffers on disk while Grafana Cloud is unreachable. |
| Grafana Cloud | hosted (free tier) | Stores metrics and logs, runs the alert rules (`rules/`) and the Alertmanager (`alertmanager/`). |
| Sentry | hosted (free tier), `kit/errs` | Every log line at error level and every panic becomes a Sentry event, grouped by service and message, tied to the build. |
| Telegram | the "AdHunters alerts" group | Pages (with sound, every 5 minutes until cleared) and chat alerts (silent, 08:00 to 22:00 São Paulo). Each message links its runbook in `runbooks/`. |
| Better Stack | hosted (free tier) | Receives the always-firing `Heartbeat` every minute and calls the owner when it stops. Later: outside checks of the apps. |

## What you sign up for

Nothing here creates an account or holds a secret. Every secret is a
`FILL_ME` until you put it in.

1. **Grafana Cloud** (grafana.com, free). Create a stack in an EU region. From
   the stack's page note the Prometheus URL and user, the Loki URL and user,
   and the Alertmanager URL and user. Create two access policy tokens:
   - `boxes`: `metrics:write`, `logs:write`. It goes on the boxes.
   - `rules`: `rules:read`, `rules:write`, `alerts:read`, `alerts:write`. It
     stays on your laptop, for `push.sh`.
2. **Sentry** (sentry.io, free Developer plan). Create one Go project,
   `adhunters-go`; all Go services report there, told apart by the `service`
   tag. Note its DSN. In Alerts, keep the default "new issue" email alert.
3. **Better Stack** (betterstack.com, free). In Uptime, create one heartbeat,
   "Grafana alerting", expected every 1 minute with a 5-minute grace, alerting
   you by call and email. Note its URL.
4. **Telegram**. Create a bot with @BotFather and note its token. Create the
   group "AdHunters alerts", add the bot, send one message in the group, then
   open `https://api.telegram.org/bot<token>/getUpdates` and note the
   group's `chat.id` (a negative number).
5. **mimirtool** on your laptop (grafana/mimir releases), for `push.sh push`.

## Putting it in place

On each box, after `setup.sh` has run once:

- `/etc/adhunters/alloy.env`: the Grafana Cloud URLs, users and the `boxes`
  token (see `alloy/alloy.env.example`). Then `systemctl restart alloy`. On
  the data box, `POSTGRES_MONITOR_URL` is filled in by `setup.sh`.
- `/etc/adhunters/observe.env`: `SENTRY_DSN`. Then restart the services
  (one capture instance at a time). Empty leaves Sentry off.

On your laptop, once and after every change to `rules/` or `alertmanager/`:

```
cp platform/observe/observe.env.example platform/observe/observe.env   # fill it in
platform/observe/push.sh push
```

Push the rules once collection runs on the new boxes: `CaptureStopped` pages
while no box scrapes, which is true until then.

## Changing alerts

`rules/*.yaml` are Prometheus rule files, one Grafana Cloud namespace per
file. Every alert needs a `tier` label (`page`, `chat` or `heartbeat`), a
`summary` and a `runbook_url` to a file in `runbooks/`.
`push.sh check` validates the rules, runs `tests/rules_test.yaml`, checks the
runbooks and validates the Alertmanager config; CI runs it on every PR.

A periodic task in any Go service gets freshness alerting for free by
reporting through `kit/ops` `Tasks` (`Promise` once, `Done` after each run):
`TaskLate` fires when its last success is older than its promise.

## Not built yet

- The 08:00 digest to Telegram. It needs a small job that reads Grafana
  Cloud and the `_api` views; it comes with the first app that owns it.
- New Sentry issues in Telegram. Sentry has no Telegram integration, so this
  needs a small relay; until then Sentry emails you.
- Dashboards, deploy markers and silencing alerts during a deploy. They come
  with the deploy script.
- Outside checks of the apps in Better Stack: no app is public yet.
- Backup alerts (WAL archive, last backup age): they come with pgBackRest.
- Silent-failure checks in SQL (sightings against usual per publisher, books
  that must balance): `tracks-loader status -books` exists; exporting it as
  metrics is next.
