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
