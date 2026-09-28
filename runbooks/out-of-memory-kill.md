# OutOfMemoryKill

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/`.

## What it means

The kernel killed a process on this box because memory ran out, or a unit hit its own `MemoryMax`.

## Check

1. `journalctl -k --since -30min | grep -i -E 'killed process|oom'`: which process.
2. `systemctl status <unit>`: systemd restarts our units by themselves.

## Fix

- Raposa's browser at its cap: expected now and then; it restarts. Often means one page is too heavy.
- Anything else: see [memory-low](memory-low.md).

## After

It clears 15 minutes after the last kill.
