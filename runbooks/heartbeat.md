# Heartbeat

Heartbeat: Better Stack only. Rule: `platform/observe/rules/`.

## What it means

This alert always fires. Grafana Cloud's Alertmanager sends it to Better Stack every minute. It never reaches Telegram.

If it **stops** arriving, Better Stack calls and emails the owner: the alerting itself is broken, so no other alert can be trusted until it is back.

## Check

1. Grafana Cloud status page (status.grafana.com): an outage on their side.
2. In Grafana, Alerting: is the rule group `heartbeat` still loaded? `./platform/observe/push.sh push` loads it again.
3. Alertmanager: is the `betterstack-heartbeat` receiver failing? Its URL may have changed.

## Fix

- Push the rules and config again (`platform/observe/push.sh push`).
- A new Better Stack heartbeat URL: update `observe.env` and push.

## After

Better Stack closes its incident when the heartbeat arrives again.
