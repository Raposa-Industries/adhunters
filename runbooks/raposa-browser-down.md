# RaposaBrowserDown

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/`.

## What it means

Raposa's keeper has found the browser runner down for 15 minutes. Browser rungs and keeps wait; plain fetch visits still run.

## Check

1. `systemctl status raposa-browser` and `journalctl -u raposa-browser --since -30min` on the worker box.
2. **OutOfMemoryKill** alongside: the runner hit its 6 GB cap.

## Fix

- `systemctl restart raposa-browser`. The keeper retries the pages it could not keep.
- Chromium missing after an update: see `raposa/browser/README.md` for installing it again.

## After

It clears when the keeper reaches the runner again.
