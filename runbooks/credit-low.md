# CreditLow, CreditRunningOut

CreditLow is a chat alert (Telegram, silent, 08:00 to 22:00 São Paulo). CreditRunningOut pages. Rule: `platform/observe/rules/credits.yaml`.

## What it means

A prepaid service is running out. observe-bot reads its balance every 15 minutes from the service's own API, as `credits.conf` on the data box says. For a service with no API (an `[estimate]`, such as `openai`) it is an estimate: the balance last put in by hand, less what our services reported spending since.
- **CreditLow:** the balance is under the `warn` level for that service.
- **CreditRunningOut:** at the rate of the last 6 hours it is gone within a day, or it is gone already.

When it is gone, the work that needs it stops. For example, Raposa's residential line (`iproyal`) stops.

## Check

1. The alert names the service (`credit`) and what is left, in its unit.
2. On the data box, `sudo bash -c 'set -a; . /etc/adhunters/observe-bot.env; /opt/adhunters/bin/observe-bot credits'` reads every balance now.
3. For an estimate, compare with the service's billing page (OpenAI: platform.openai.com, Settings, Billing). `observe-bot credits` shows the balance it started from, when, and what was counted since.
4. What is spending it: for `iproyal`, Raposa's visits (`raposa_visits_total`) and its residential bytes. For an AI service, the app that calls it.

## Fix

- Top up at the service. Card payments and plan changes are the owner's.
- If something is spending faster than it should, such as a loop of retries or an investigation stuck on the residential line, stop that first. Changing a box waits for the owner's word.
- If an estimate is wrong, put the real balance and the time you read it (UTC) in its `balance` and `as_of`, then `systemctl restart observe-bot`. The count starts again from there. An estimate that drifts low while the real balance is fine means a service reports spending twice; one that drifts high means a paid call does not report it.
- If the level is wrong for how fast it is spent, change `warn` in `/etc/adhunters/credits.conf`, then `systemctl restart observe-bot`.

## After

CreditLow clears 15 minutes after the balance is back over `warn`, at the next read. CreditRunningOut clears as soon as the balance is over `warn` again.
