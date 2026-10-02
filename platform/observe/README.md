# Observability

Knowing the platform works without watching it: metrics, logs and errors go
to hosted services outside Hetzner, alerts go to the Telegram group
"AdHunters alerts" (decision 0004), what the team acts on goes to the ops
group "AdHunters operation", and a heartbeat proves the alerting
itself works (decision 0009).

| Piece | Where | What it does |
|---|---|---|
| Grafana Alloy | every box, `alloy/` here, installed by `platform/servers/setup.sh` | Reads every service's `/metrics`, the host and, on the data box, Postgres and the Cloudflare tunnel every 60 s; ships the journal of our units. Buffers on disk while Grafana Cloud is unreachable. Sends only what is worth watching (see Series). |
| Grafana Cloud | hosted (free tier) | Stores metrics and logs, runs the alert rules (`rules/`) and the Alertmanager (`alertmanager/`). |
| Sentry | hosted (free tier), `kit/errs` | Every log line at error level and every panic becomes a Sentry event, grouped by service, message and the error's shape (its text with numbers, quoted text, URLs and ids masked), tied to the build. A process sends at most one event an hour per issue, 30 an hour in all, so a stuck error cannot use up the free plan's events; the real count is `adhunters_log_errors_total`. |
| Telegram | the "AdHunters alerts" group | Pages (with sound, every 5 minutes until cleared), chat alerts (silent, at any hour), the digest and new Sentry issues: everything about the platform itself. Each alert links its runbook in `runbooks/`. |
| Telegram | the ops group "AdHunters operation" | What the team acts on: Intel's alerts, delivery status changes and suggestions, and Taboola policy changes. Set by `OPS_TELEGRAM_CHAT_ID` in `observe-bot.env` and `intel-numbers.env`; while it is empty they go to "AdHunters alerts". |
| Better Stack | hosted (free tier) | Receives the always-firing `Heartbeat` every minute and calls the owner when it stops. Later: outside checks of the apps. |
| `observe-bot` | data box, `cmd/observe-bot` here | Sends the 08:00 digest (the last 24 hours in numbers, with the alerts that fired, the errors logged and the failed task runs; three lines on a quiet day) and posts each new Sentry issue to Telegram, silently, as it first appears (it asks Sentry every minute, tries a slow or failing answer three times, and logs a failed poll as a warning rather than reporting it to Sentry; `adhunters_task_runs_total{task="sentry_relay",result="error"}` counts them and `TaskLate` fires after 10 minutes without getting through). Reads what is left on each prepaid service every 15 minutes and knows when subscriptions renew (see Credits below). Crawls Taboola's policy pages every 6 hours and posts what changed (see Taboola policy watch below). |
| Dashboards | `dashboards/`, uploaded by `push.sh` | "AdHunters · Services" (what fires now, errors by service and message with their log lines, tasks, processes, spend), "AdHunters · Web" (the Cloudflare tunnel, each app's requests through create-web, calls to outside services, landing sites), "AdHunters · Postgres" (connections, load, the queries that take the most time, space by schema and table, each app's pool), "AdHunters · Apps" (Raposa, Spy, Funnels, Desk), "AdHunters · Collection" (capture, shipper, loader, walker) and "AdHunters · Boxes" (hosts, network, processes and pressure, Postgres, backups, task freshness, what Alloy sends). |
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
2. **Sentry** (sentry.io, free Developer plan). Create one Go project
   (ours, made 30 Sep 2026 in the EU region: organization
   `marcos-capistrano`, project `go`); all Go services report there, told
   apart by the `service` tag. Note its DSN and your organization's slug. In Settings > Custom
   Integrations, create an internal integration with Issue & Event: Read and
   note its token, for observe-bot. An organization in the EU data region
   answers on `https://de.sentry.io`, so observe-bot's `SENTRY_URL` is that
   instead of `https://sentry.io`.
3. **Better Stack** (betterstack.com, free). In Uptime, create one heartbeat,
   "Grafana alerting", expected every 1 minute with a 5-minute grace, alerting
   you by call and email. Note its URL.
4. **Telegram**. Create a bot with @BotFather and note its token. Create the
   group "AdHunters alerts", add the bot, send one message in the group, then
   open `https://api.telegram.org/bot<token>/getUpdates` and note the
   group's `chat.id` (a negative number). Do the same for the ops group
   "AdHunters operation", with the same bot, for `OPS_TELEGRAM_CHAT_ID`.
5. **Hetzner Object Storage**: a bucket `adhunters-backups` and an access
   key for it, for pgBackRest.
6. **mimirtool** and `jq` on your laptop (grafana/mimir releases), for
   `push.sh push`.

## Putting it in place

On each box, after `setup.sh` has run once:

- `/etc/adhunters/alloy.env`: the Grafana Cloud URLs, users and the `boxes`
  token (see `alloy/alloy.env.example`). Then `systemctl restart alloy`. On
  the data box, `POSTGRES_MONITOR_URL` is filled in by `setup.sh`.
- `/etc/adhunters/observe.env`: `SENTRY_DSN`. Empty leaves Sentry off.
  `platform/servers/set-sentry-dsn.sh`, run from your computer, asks for it
  once (hidden), writes it on every box and restarts only what is already
  running, capture one instance at a time. On the data box,
  `observe-bot sentry-test` (with `observe.env` and `observe-bot.env`
  loaded) sends one test error, to see it arrive. Its message carries the
  time, so every run is a new Sentry issue and reaches the chat again.
- Data box: `/etc/adhunters/observe-bot.env` (Telegram and the ops group's
  chat id, the `bot` token,
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

`CaptureStopped` pages while no box scrapes, so push once at least one
capture instance runs on the new boxes (the worker's `tracks-capture@a` has
since 29 Sep 2026). While only test lines run, the capture chat alerts may
say collection is low; silence them in Grafana (Alerting, Silences) until
the switch-over.

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

## Taboola policy watch

Taboola rejects ads that break its advertiser policies, and those policies
change. observe-bot reads every policy page in the Realize help center's
[Policy & Content Review](https://realize.com/help/en/collections/11915686-policy-content-review)
collection every 6 hours: the collection, the sections it lists and every
article they list (never the links inside an article), plus two policy
articles filed elsewhere: [Declare AI-generated content in your ads](https://realize.com/help/en/articles/16002528-declare-ai-generated-content-in-your-ads)
and Campaign Branding Text. It posts each policy
change to the ops group "AdHunters operation" (`OPS_TELEGRAM_CHAT_ID`;
"AdHunters alerts" while that is empty), silently:

- **Taboola policy changed**: the article's link, then the lines removed (➖)
  and added (➕), each paragraph or list item one line.
- **New Taboola policy article** or **section**: its text.
- **Taboola policy article removed**: it is no longer listed.

The first crawl only sets the baseline and says how many articles it
watches. Every page is saved raw (gzip) in
`/var/lib/observe-bot/policy/raw/<UTC time>/` before it is read, about 1 MB a
crawl, kept for good; the last good crawl is `policy/current.json`.

Guards against false alarms: text that changes without the policy changing
(relative dates, authors, article counts, the feedback widget) is dropped;
a page that cannot be read, a page with no text (a bot check) or a crawl with
under half the articles of the last one fails the run instead of reporting
removals. Failed runs are the `policy_watch` task: `TaskLate` fires after
25 hours without a good crawl. A change whose message fails to send is sent
again next time.

```
sudo /opt/adhunters/bin/observe-bot policy          # crawl now and print the changes; sends and keeps nothing
sudo STATE_DIRECTORY=/var/lib/private/observe-bot /opt/adhunters/bin/observe-bot policy -list
sudo STATE_DIRECTORY=/var/lib/private/observe-bot /opt/adhunters/bin/observe-bot policy -from 20261001T000000Z -to 20261008T000000Z   # read two saved crawls again and compare
```

`POLICY_URLS` in `observe-bot.env` (space separated collection or article URLs) widens
or changes what is watched; `-policy-poll 0` on `observe-bot run` turns it
off. Tried on the real help center on 29 Sep 2026: 68 pages, 58 articles,
and two crawls minutes apart compared as no change.

## Changing alerts

`rules/*.yaml` are Prometheus rule files, one Grafana Cloud namespace per
file. Every alert needs a `tier` label (`page`, `chat` or `heartbeat`), a
`summary` and a `runbook_url` to a file in `runbooks/`, and every alert that
reaches Telegram a `resolved` line: what is true once it clears. The ✅
Cleared message shows that line instead of the summary, because Grafana
keeps an alert's annotations from its last firing evaluation, so the summary's
numbers and its "top it up" would read as a new alert. For the same reason a
`resolved` line uses only labels, never `$value`.
`push.sh check` validates the rules, runs `tests/rules_test.yaml`, checks the
runbooks and validates the Alertmanager config; CI runs it on every PR.

A periodic task in any Go service gets freshness alerting for free by
reporting through `kit/ops` `Tasks` (`Promise` once, `Done` after each run):
`TaskLate` fires when its last success is older than its promise, and
`TaskFailing` when it fails 3 times in 2 hours, even with successes in
between.

Every error line any service logs is counted by message (`kit/logx`,
`adhunters_log_errors_total{msg}`), and `ErrorsLogged` fires when one service
logs the same message 3 times in an hour. Sentry announces each kind of error
once, when it first appears; this is what keeps a repeating failure from
going quiet after that. An error that is really "nothing to do" belongs at
info or warn level, or it will fire.

Alloy watches every unit of ours (`UnitFailed`, `PostgresRestarted`) and ships
its journal. `push.sh check` fails when `platform/servers/setup.sh` runs a
unit that `alloy/common.alloy` does not cover.

`rules/web.yaml` watches what the team opens: `TunnelDown` when the data box's
cloudflared holds no connection to Cloudflare, and `AppErrors` when over 20%
of an app's answers through create-web (`adhunters_http_requests_total`) are
5xx.

Chat alerts are delivered at any hour, silently. Until 2 Oct 2026 they were
held from 22:00 to 08:00, and one that cleared before morning was never sent
(decision 0009).

## Series

Grafana Cloud's free plan holds 10,000 series (a series is one metric with
one set of label values) and drops what goes over; it bills nothing. On
2 Oct 2026 we sent 12,859: Postgres 6,936 (statistics for each of its 221
tables and a copy of every setting, which nothing read), the hosts 2,126,
Alloy's own 1,869 and our services 1,820. Since then each exporter sends
what a rule, a dashboard or a person reads, about 5,000 in all:

- Postgres (`alloy/postgres.alloy`): connections, locks, transactions,
  cache hits, deadlocks and temporary files per database, checkpoints,
  WAL, replication and archiving; the 20 queries that took the most time
  (`pg_stat_statements_*`, with their text in `pg_stat_statements_query_id`);
  and the size and rows of each schema and of the 20 biggest tables, a
  partitioned table with its partitions (`pg_schema_*`, `pg_table_*`, from
  `alloy/postgres-queries.yaml`). No statistics for every table.
- The hosts (`alloy/common.alloy`): only the collectors listed there, which
  add network errors and retransmits, sockets, the connection-tracking
  table, file handles, processes and threads, the clock, and each unit's
  restarts and threads to what the alerts read.
- Alloy and cloudflared: a short list each of the metrics worth keeping.
- Our services (`kit/ops`, `kit/pg`): besides their own metrics, each
  service with a database reports its pool (`adhunters_db_pool_*`), calls to
  Taboola, Telegram, OpenAI and the other headline providers are counted by
  provider and status code (`adhunters_outbound_*`), and create-web counts
  every request by app and status code (`adhunters_http_*`). About 500
  series together.

Before adding a metric, count its series: each label multiplies them, so a
label never holds an id, a URL or free text, only a short fixed set of
values. Where we stand, in Grafana's Explore: `count({__name__=~".+"})`, and
`count by (job) ({__name__=~".+"})` for who sends them. Each box's Alloy
reports what it sends as `prometheus_remote_write_wal_storage_active_series`;
`SeriesNearLimit` (chat) fires when the boxes send over 9,000 together.

## Daily check

Claude reads what fired and failed once a day, at 07:54 São Paulo, in the
project's "Alerts that fire" thread: Sentry through its connector. Claude
reports what needs the owner there, fixes what is its own to fix by PR, and
passes the rest to the thread that owns it. Grafana is the owner's to watch
(2 Oct 2026); alerts reach both through Telegram.

`daily-check.sh` prints, from Grafana Cloud, the alerts that fired, the error
lines and failed task runs by service, restarts, failed units and the
collection numbers of the last 24 hours. Claude runs it in the daily check
only when the session can reach Grafana Cloud, which takes:

- the stack's Prometheus host (`prometheus-prod-…grafana.net`) in the
  environment's allowed domains (Project settings, Network access);
- three environment variables in Project settings: `GRAFANA_QUERY_URL` and
  `GRAFANA_QUERY_USER` (the same values as in observe-bot.env on the data
  box) and `GRAFANA_QUERY_TOKEN`, a new access policy token with
  `metrics:read` only.

`./daily-check.sh` with the same three variables works from a laptop too.

## Not built yet

- Deploy markers and silencing alerts during a deploy. They come with the
  deploy script.
- Outside checks of the apps in Better Stack: no app is public yet.
- The monthly restore test on the standby box.
- Silent-failure checks in SQL (sightings against usual per publisher, books
  that must balance): `tracks-loader status -books` exists; exporting it as
  metrics is next.
