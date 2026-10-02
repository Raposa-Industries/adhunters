# RaposaIdle

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/raposa.yaml`.

## What it means

raposa-engine on the worker box has taken no investigation step for more than 3 hours. Automatic quick investigations alone usually come several an hour, so either nothing is being queued or what is queued does not run.

## Check

1. Queued at all? `increase(raposa_auto_queued_total[6h])`: 0 means the automatic queue finds nothing. `platform/retire/collection-check.sh` shows the queue step by step ("the automatic quick queue now"); a column that drops to 0 is the step that stops it.
2. Waiting but not running? Its "Raposa: waiting and running now" section; `journalctl -u raposa-engine --since -3h` for "claim failed".
3. Automatic runs switched off in Raposa's settings, and nobody asked for an investigation: then quiet is expected.

## Fix

- Nothing queued while ads run: a fix in `raposa/` (the quick queue's query), by PR.
- Claims failing: the database or the engine's login; see the error.
- Automatic runs off on purpose: silence this alert in Grafana (Alerting, Silences) for as long as they stay off.

## After

It clears on the engine's next step.
