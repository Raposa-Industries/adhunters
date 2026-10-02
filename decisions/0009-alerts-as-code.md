# 0009 · Alerts as code, errors from the logs

**Decided:** 28 Sep 2026, by Claude, following the design page's
observability section and decisions 0004 and 0005. Open to the owner.

- Alert rules and the Alertmanager config live in the repository
  (`platform/observe/`), are tested in CI, and are pushed to Grafana Cloud
  with `push.sh`. Nothing about alerting is clicked together in a UI.
- Three tiers: page (Telegram with sound, repeated every 5 minutes), chat
  (Telegram, silent, daytime) and heartbeat (Better Stack only). Every alert
  has a runbook in `runbooks/`, and CI refuses an alert without one.
- The watcher is watched: an always-firing heartbeat goes to Better Stack,
  which calls when it stops.
- Services report errors to Sentry through their logs: every error-level
  line and every panic, grouped by service and message. No service code calls
  Sentry; `kit/logx` and `kit/run` do it when `SENTRY_DSN` is set.
- Scrapes run every 60 s, the free tier's resolution; every alert is written
  for it.

**Why:** alerts reviewed in PRs stay in step with the code they watch, and
an alert without a next step trains people to ignore alerts.

**Changed:** 2 Oct 2026, by Claude, after the owner asked why a night of
failures (spy-numbers jobs, the walker, Raposa queueing nothing, a Postgres
restart) reached neither the owner nor Claude.

- Chat alerts go out at any hour, still silent. Held until 08:00, one that
  cleared before then was never sent.
- Sentry groups by service, message and the error's shape (its text with
  numbers, quoted text, URLs and ids masked), so the same step failing a new
  way is a new issue. Grouped by message alone, three different spy-numbers
  failures were one old issue and none was announced.
- Each process sends at most one event an hour per issue (and 30 an hour in
  all), so one stuck error cannot spend the free plan's monthly events, after
  which Sentry would drop new issues too. The full count is the metric
  `adhunters_log_errors_total` (kit/logx), which **ErrorsLogged** and the
  digest read.
- A task that keeps failing between successes fires **TaskFailing**;
  **TaskLate** alone never saw it.
- Alloy watches every unit of ours, one-off `systemd-run` jobs included, and
  ships their journals; `push.sh check` fails when setup.sh runs a unit it
  does not watch.
- Claude reads what fired and failed once a day (the daily check), so the
  owner is not the only one who notices.
