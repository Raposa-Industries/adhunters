# ErrorsLogged

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

One service logged the same error line 3 or more times in an hour. Sentry announces a kind of error once, when it first appears, so a failure that repeats for hours is only visible here (and in the 08:00 digest's "Errors logged"). The alert names the service and the log message.

## Check

1. `journalctl -u <unit> --since -1h | grep '"level":"ERROR"'` on the box the alert names: the full error text and its fields.
2. Sentry, filtered to the service: each different cause of that message is its own issue, with one event an hour (the count here is the real one).
3. Is it one cause or several? `sum by (msg) (increase(adhunters_log_errors_total{service="<service>"}[6h]))` over time shows when it started; match that to a deploy or a restart.

## Fix

- A Postgres restart or a short outage of something it calls: it stops by itself; nothing to do (this alert is muted for an hour after **PostgresRestarted**).
- A bug: fix by PR in the service's folder.
- An error that is really "nothing to do" (no data yet, a lock someone else holds): it should not be logged at error level; change it to info or warn by PR.

## After

It clears once the service has logged that message fewer than 3 times in an hour.
