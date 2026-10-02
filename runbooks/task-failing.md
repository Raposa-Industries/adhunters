# TaskFailing

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

A periodic task failed 3 or more times in 2 hours. It may still succeed in between, so it is not late (**TaskLate** does not fire), but each failure is work not done: a refresh that left part of a table stale, a pull that missed a window.

## Check

1. The alert names the service and the task. `journalctl -u <service> --since -2h | grep '"level":"ERROR"'`: the error of each failed run.
2. Sentry: each different cause is its own issue.
3. `adhunters_task_last_duration_seconds{task="<task>"}`: a task that times out has grown too slow for its statement timeout.

## Fix

- The error decides: a database error (overflow, timeout, deadlock) is a fix by PR in the service's folder; a timeout may need a faster query or a smaller batch.
- A failure that is really "nothing to do yet" should count as a success with 0 rows, by PR.

## After

It clears once the task has failed fewer than 3 times in the last 2 hours.
