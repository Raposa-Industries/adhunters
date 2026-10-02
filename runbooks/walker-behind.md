# WalkerBehind

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/collection.yaml`.

## What it means

An ad that runs now has waited more than 6 hours past its turn to be walked again (the re-walk is every 6 hours), for 30 minutes. tracks-walker is not keeping up: changes on those landing pages go unseen until it gets there. `tracks_walker_backlog` says how many ads are overdue.

## Check

1. Walking at its usual rate? `sum(rate(tracks_walker_walks_total[1h]))` against the day before. A drop with **WalkerFailing** is lines or pages failing; a drop without it is slow walks (`tracks_walker_walk_seconds`).
2. More ads than usual? "Walker backlog" in `platform/retire/collection-check.sh`: live ads with a link, and how many are overdue.

## Fix

- Slow or failing: see [walker-failing](walker-failing.md).
- More live ads than the workers can walk: more workers (`WORKERS` in `/etc/adhunters/tracks-walker.env`, 2 at first) or more lines; the owner decides, since lines cost money.

## After

It clears once no ad is 6 hours past its turn.
