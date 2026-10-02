# ServiceDown

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

A service's `/metrics` has not answered for 5 minutes. The process is stopped, crashing, or hung.

## Check

1. `systemctl status <unit>` on the box named in the alert.
2. `journalctl -u <unit> -n 100`: why it stopped. A unit whose settings hold `FILL_ME` is never started on purpose.
3. **UnitFailed** or **ServiceRestarting** alongside says it is crashing.

## Fix

- Stopped: `systemctl start <unit>`.
- Crashing after a deploy: roll back to `/opt/adhunters/bin/<binary>.prev` and restart.
- Hung: `systemctl restart <unit>` (it gets 45 s to stop cleanly).

## After

It clears when `/metrics` answers again.
