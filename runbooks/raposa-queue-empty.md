# RaposaQueueEmpty

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/raposa.yaml`.

## What it means

Raposa's automatic quick queue (`raposa.queue_quick`, every 5 minutes) has queued nothing for more than 6 hours while raposa-engine runs. It usually queues several an hour: a quick investigation for each new ad whose landing page tracks-walker could not get. A queue that selects nothing raises no error, so this is the only sign. It mutes **RaposaIdle**, which it explains.

## Check

1. `platform/retire/collection-check.sh`, section "the automatic quick queue now": it runs the queue's steps one by one. The first column at 0 is the step that drops everything (on 2 Oct 2026, `live_creatives`: the view it joined had no Taboola ads).
2. The quick settings under it: `quick_networks`, `quick_walk_failures`, `quick_queue_depth`. A setting changed to something that matches nothing looks the same.
3. Automatic runs switched off on purpose: then quiet is expected.

## Fix

- A step that matches nothing because of the data it reads (a view, a network name): a fix in `raposa/` by PR.
- A setting: the owner changes it.
- Off on purpose: silence this alert in Grafana (Alerting, Silences) for as long as automatic runs stay off.

## After

It clears 30 minutes after the queue queues again. The ads it missed are new for 7 days (`quick_new_days`), so the queue picks them up by itself.
