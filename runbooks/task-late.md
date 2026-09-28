# TaskLate

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/`.

## What it means

A periodic task (a refresh, a pull, a close) has not succeeded for longer than it promises. The table it writes is stale; readers see old numbers.

## Check

1. The alert names the service and task. `adhunters_task_runs_total{task="<task>"}`: is it running and failing, or not running at all?
2. `journalctl -u <service> --since -2h`: the task's error lines (they are also in Sentry).
3. `adhunters_task_last_duration_seconds`: a task that got too slow for its schedule.

## Fix

- Failing: the error decides. A database problem points to [postgres-down](postgres-down.md); a code error is a fix by PR.
- Not running: the service is down or stuck; restart it.

## After

It clears on the task's next success.
