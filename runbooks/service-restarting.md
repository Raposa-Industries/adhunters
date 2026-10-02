# ServiceRestarting

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

A service restarted 3 or more times in 30 minutes. systemd brings it back each time, but work is being interrupted.

## Check

1. `journalctl -u <unit> --since -30min`: the last lines before each start.
2. Sentry: a panic reports there with its stack trace before the process exits.
3. **OutOfMemoryKill** alongside means the kernel is killing it.

## Fix

- A panic: fix by PR; meanwhile roll back to `.prev` if a deploy brought it.
- Memory: see [out-of-memory-kill](out-of-memory-kill.md).

## After

It clears after 30 quiet minutes.
