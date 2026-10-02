# UnitFailed

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

A systemd unit on this box is in the failed state: it stopped and systemd gave up restarting it. Every unit of ours is watched, and so are one-off jobs started with `systemd-run` (`run-u1234.service`): a one-off that failed did not finish its work.

## Check

1. `systemctl status <unit>` and `journalctl -u <unit> -n 100`.
2. `systemctl list-units --failed` for others.

## Fix

- Fix the cause the journal names, then `systemctl reset-failed <unit> && systemctl start <unit>`.
- A settings file with a mistake: fix `/etc/adhunters/<unit>.env`.
- A one-off job (`run-u…`): `systemctl status <unit>` shows the command it ran. Run it again once the cause is fixed, or decide it is not needed; then `systemctl reset-failed <unit>` clears it. A failed one-off stays failed, and this alert stays, until then.

## After

It clears when the unit is no longer failed.
