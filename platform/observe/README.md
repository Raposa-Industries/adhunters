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
| `observe-bot` | data box, `cmd/observe-bot` here | Sends the 08:00 digest (the last 24 hours in numbers; three lines on a quiet day) and posts each new Sentry issue to Telegram, silently, as it first appears. Reads what is left on each prepaid service every 15 minutes and knows when subscriptions renew (see Credits below). |
| Dashboards | `dashboards/`, uploaded by `push.sh` | "AdHunters · Collection" (capture, shipper, loader) and "AdHunters · Boxes" (hosts, services, Postgres, backups, Raposa, task freshness). |
| Backups | data box, pgBackRest (`platform/servers/setup.sh`) | WAL archived every 60 s, a full backup on Sundays and a differential on other days, all to object storage; alerts when archiving fails or a backup is late. |

## What you sign up for

Nothing here creates an account or holds a secret. Every secret is a
`FILL_ME` until you put it in.

1. **Grafana Cloud** (grafana.com, free). Create a stack in an EU region. From
   the stack's page note the Prometheus URL and user, the Loki URL and user,
   and the Alertmanager URL and user. Create two access policy tokens:
   - `boxes`: `metrics:write`, `logs:write`. It goes on the boxes.
   - `rules`: `rules:read`, `rules:write`, `alerts:read`, `alerts:write`. It
     stays on your laptop, for `push.sh`.
   Also create a third token, `bot`, with `metrics:read` (for observe-bot),
   and in Grafana (Administration > Service accounts) a service account with
   the Editor role and a token, for the dashboards.
2. **Sentry** (sentry.io, free Developer plan). Create one Go project,
   `adhunters-go`; all Go services report there, told apart by the `service`
   tag. Note its DSN and your organization's slug. In Settings > Custom
   Integrations, create an internal integration with Issue & Event: Read and
   note its token, for observe-bot.
3. **Better Stack** (betterstack.com, free). In Uptime, create one heartbeat,
   "Grafana alerting", expected every 1 minute with a 5-minute grace, alerting
   you by call and email. Note its URL.
4. **Telegram**. Create a bot with @BotFather and note its token. Create the
   group "AdHunters alerts", add the bot, send one message in the group, then
   open `https://api.telegram.org/bot<token>/getUpdates` and note the
   group's `chat.id` (a negative number).
5. **Hetzner Object Storage**: a bucket `adhunters-backups` and an access
   key for it, for pgBackRest.
6. **mimirtool** and `jq` on your laptop (grafana/mimir releases), for
   `push.sh push`.

## Putting it in place

On each box, after `setup.sh` has run once:

- `/etc/adhunters/alloy.env`: the Grafana Cloud URLs, users and the `boxes`
  token (see `alloy/alloy.env.example`). Then `systemctl restart alloy`. On
  the data box, `POSTGRES_MONITOR_URL` is filled in by `setup.sh`.
- `/etc/adhunters/observe.env`: `SENTRY_DSN`. Then restart the services
  (one capture instance at a time). Empty leaves Sentry off.
- Data box: `/etc/adhunters/observe-bot.env` (Telegram, the `bot` token,
  Sentry's organization and integration token), then
  `systemctl restart observe-bot`. `observe-bot digest` prints today's
  digest without sending it, to try the settings.
- Data box: the bucket's keys in `/etc/pgbackrest/pgbackrest.conf`, then run
  `setup.sh` again. It switches WAL archiving on (only then, since a failing
  archive fills the disk), creates the stanza, checks it and starts the first
  full backup.

- Data box: `/etc/adhunters/credits.conf` (written once by `setup.sh` from
  `credits.conf.example`) and the keys it names in `observe-bot.env`. See
  Credits below.

On your laptop, once and after every change to `rules/` or `alertmanager/`:

```
cp platform/observe/observe.env.example platform/observe/observe.env   # fill it in
platform/observe/push.sh push
```

Push the rules once collection runs on the new boxes: `CaptureStopped` pages
while no box scrapes, which is true until then.

## Credits

Every paid service the suite runs on can stop our work when it runs out, so
each one is watched in one of four ways:

1. **A balance check**, where the service has a balance API. observe-bot reads
   it every 15 minutes (`[credit NAME]` in `credits.conf`). `CreditLow`
   (chat) fires under the section's `warn` level. `CreditRunningOut` (page)
   fires when the last 6 hours say it is gone within a day. The digest lists
   what is left.
2. **An estimate**, where there is a prepaid balance but no API to read it
   (`[estimate NAME]`). The owner puts in the balance from the billing page
   and when they read it; observe-bot subtracts what our services report
   spending since (`srv.Spent("provider", usd)` in `kit/ops`, the counter
   `adhunters_spend_usd_total`), every 15 minutes, and exports the result as
   the same `adhunters_credit_remaining`, so `CreditLow` and
   `CreditRunningOut` work on it unchanged. The digest marks it with "~".
   Where the count stands is kept in observe-bot's state directory
   (`estimate-NAME.json`), so a restart neither counts twice nor skips.
   Putting in a new balance or time starts the count again from it.
3. **A renewal reminder**, where there is no balance to read but a plan that
   lapses (`[renewal NAME]`). `RenewalDue` (chat) fires `remind` days before.
4. **The refusal itself**. A service that is refused for lack of credit
   calls `srv.OutOfCredit("provider")` (`kit/ops`), and `OutOfCredit` pages on
   the first one. This is the last line for services with no balance API. It
   is also why each provider's own auto-recharge or low-balance email should
   be on.

What we pay for, and how each is watched (checked 29 Sep 2026 from this repo,
adhunters-collector, adhunters-v4 and auto-creative):

| Service | Used by | Runs out as | Watched by |
|---|---|---|---|
| IPRoyal residential | Raposa (`res-1`) | GB of traffic | balance check `iproyal` (`GET resi-api.iproyal.com/v1/me`, `available_traffic`) |
| IPRoyal datacenter and ISP lines | capture, Raposa (`dc-us-*`, `isp-*`) | monthly subscriptions on auto-renew (one renews on the 5th, the others on the 14th) | renewals `proxies-datacenter` and `proxies-isp`; capture's own alerts when lines are refused |
| OpenAI | Create (images and headlines, the only provider: the owner's decision of 29 Sep), today's Spy (embeddings) | prepaid credit (19 USD on 29 Sep) | estimate `openai`, then `OutOfCredit` |
| OpenCode Zen | today's Spy (the model) | prepaid credit | no balance API yet (an open request upstream): auto-reload, then `OutOfCredit` |
| Anthropic | old collector (vertical tagging), Intel briefs later | prepaid credit | no balance API: auto-reload, then `OutOfCredit` |
| Hetzner (servers, Object Storage) | everything | a monthly invoice | nothing to run out; the card on file |
| Grafana Cloud, Sentry, Better Stack | observability | free-tier quotas | not yet: each warns by email near its limit |

auto-creative also has code for fal, Together AI and Gemini; Create will not
use them, so they are not watched. fal has a balance API
(`GET api.fal.ai/v1/account/billing?expand=credits`,
`credits.current_balance`) should that change.

The IPRoyal check was written from its documentation and has not been tried
with a real key. The first `observe-bot credits` after filling
in a key shows whether the path and unit are right; a wrong `field` prints the
keys the answer has instead. The Create and Spy code that calls the AI
services is not in this repo yet, so `Spent` and `OutOfCredit` start
counting when it moves here. Until then the `openai` estimate stays at the
balance entered: correct it by hand from OpenAI's billing page. An estimate
is only as good as what is reported, so every call that costs money reports
its cost once, where the vendor's answer gives it (OpenAI's usage tokens
times the price).

A data box set up before estimates existed has a `credits.conf` without the
`[estimate openai]` section, because setup.sh never overwrites it: copy the
section from `credits.conf.example`.

To add a service: a `[credit]` section when it has a balance API, an
`[estimate]` when it is prepaid with no API, a `[renewal]` when it has a plan
date, then `observe-bot credits` to
try it and `systemctl restart observe-bot`.

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

- Deploy markers and silencing alerts during a deploy. They come with the
  deploy script.
- Outside checks of the apps in Better Stack: no app is public yet.
- The monthly restore test on the standby box.
- Silent-failure checks in SQL (sightings against usual per publisher, books
  that must balance): `tracks-loader status -books` exists; exporting it as
  metrics is next.
