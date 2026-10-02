# MemoryLow

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

This box has had under 10% of its memory available for 15 minutes. The kernel may start killing processes.

## Check

1. `systemd-cgtop -m` or `ps aux --sort=-rss | head`: who holds it.
2. On the worker box the usual one is Raposa's browser (capped at 6 GB). On the data box, Postgres (`shared_buffers` is 4 GB) and the loader during a replay.

## Fix

- A process leaking: restart it; open an issue with the owner.
- Expected load that no longer fits: resize the box (decision 0002 allows it).

## After

It clears above 10% available.
