# WalkerBehind

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/collection.yaml`.

## What it means

At the pace of its last 15 minutes, tracks-walker would need more than 2 hours to walk the ads waiting for it (`tracks_walker_backlog`), and that has lasted 30 minutes. On 2 Oct 2026 it needed about 30 minutes with 2 workers. It is not keeping up: changes on those landing pages go unseen until it gets there. A walker that walks nothing is [WalkerStopped](walker-stopped.md) instead.

`tracks_walker_oldest_overdue_seconds`, the longest any one ad has waited, is only information. Ads that run on and off come back already due, so it reads hours even when the walker keeps up.

## Check

1. Walking at its usual rate? `sum(rate(tracks_walker_walks_total[1h]))` against the day before. A drop with **WalkerFailing** is lines or pages failing; a drop without it is slow walks (`tracks_walker_walk_seconds`).
2. More ads waiting than usual? `tracks_walker_backlog` against the day before, and "Walker backlog" in `platform/retire/collection-check.sh`.

## Fix

- Slow or failing: see [walker-failing](walker-failing.md).
- More live ads than the workers can walk: more workers (`WORKERS` in `/etc/adhunters/tracks-walker.env`, 4 since 2 Oct 2026) or more lines; the owner decides, since lines cost money.

## After

It clears once the walker could walk what waits in under 2 hours.
