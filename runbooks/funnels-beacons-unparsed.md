# FunnelsBeaconsUnparsed

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/funnels.yaml`.

## What it means

More than 5% of the beacons loaded in 30 minutes did not parse. They are kept in the archive and counted in `funnels.raw_file.bad_beacons`, but their events are missing from the journeys.

## Check

1. Which files: `SELECT key, bad_beacons, rows FROM funnels.raw_file WHERE bad_beacons > 0 ORDER BY minute DESC LIMIT 20;`
2. Read one from the archive (or the spool, 48 hours) and look at the `body` of a bad line: an old or changed page script (`v` not 1), a page on another domain sending something else to `/e`, or cut bodies (`truncated`).

## Fix

- A changed page script: fix the loader's parser or the script by PR, deploy, then `funnels-loader replay` over the hours affected.
- Someone else posting to `/e`: nothing to fix in the loader; the lines stay counted as bad.

## After

It clears when the share falls under 5%.
