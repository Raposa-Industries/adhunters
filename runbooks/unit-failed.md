# UnitFailed

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/`.

## What it means

A systemd unit on this box is in the failed state: it stopped and systemd gave up restarting it.

## Check

1. `systemctl status <unit>` and `journalctl -u <unit> -n 100`.
2. `systemctl list-units --failed` for others.

## Fix

- Fix the cause the journal names, then `systemctl reset-failed <unit> && systemctl start <unit>`.
- A settings file with a mistake: fix `/etc/adhunters/<unit>.env`.

## After

It clears when the unit is no longer failed.
