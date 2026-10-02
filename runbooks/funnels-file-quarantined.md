# FunnelsFileQuarantined

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/funnels.yaml`.

## What it means

A Funnels raw file failed to load 3 times, or can never load as it is (its bytes in the archive changed), and was set aside. Its beacons are missing from the journeys and counts until it is fixed and replayed. Hours whose journeys it holds close without it.

## Check

1. `funnels-loader status` counts the quarantined files.
2. `SELECT key, attempts, last_error FROM funnels.raw_file WHERE quarantined_at IS NOT NULL;` says why.

## Fix

- A loader bug: fix it by PR, deploy, then `funnels-loader replay -from <file's hour> -to <hour+1h>`. A replay also takes the files of its range out of quarantine.
- Changed bytes: the spool keeps files 48 hours after archiving (`/var/lib/funnels/spool`, with a `.archived` marker). Ask the owner before replacing the archive copy.

## After

It clears when no file is quarantined. Replayed hours close again an hour after the load.
