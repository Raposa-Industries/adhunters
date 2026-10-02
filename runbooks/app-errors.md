# AppErrors

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/web.yaml`.

## What it means

Over 20% of the requests to one app through create-web, and at least 5, got a server error (5xx) in the last 15 minutes, and that has lasted 5 minutes. People using the app see errors. The alert names the app as create-web routes it: `spy` is spy-web, `raposa` raposa-web (worker), `launch` launch-web, `create` create, `intel` intel-web, `funnels` funnels-web, `desk` desk-web, and `home` and `signin` are create-web itself.

## Check

1. Which status: "Server errors by app" on Grafana's "AdHunters · Web". 502 is create-web finding the app not answering, and **ServiceDown** says so too; 500 is the app failing.
2. A 500: the app's error lines (`journalctl -u <unit> -n 100`), **ErrorsLogged** and Sentry.
3. Did it start with a deploy? "Versions running" on "AdHunters · Services".

## Fix

- Not answering: [service-down](service-down.md).
- Failing since a deploy: roll back to `/opt/adhunters/bin/<binary>.prev` and restart.
- A bug: Sentry's issue says where; the thread that owns the app fixes it.

## After

It clears when under 20% of the last 15 minutes' requests to the app fail.
