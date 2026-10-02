# BridgeBehind, BridgeFileQuarantined

Chat: Telegram "AdHunters alerts", silent, at any hour. Rules: `platform/observe/rules/`.

Only while `tracks-bridge` runs, between the switch-over and the new Spy
([platform/SWITCH-OVER.md](../platform/SWITCH-OVER.md)).

## What it means

Today's Spy (app.adhunters.pro) reads the collector's database on prodbox,
and after the switch-over only `tracks-bridge` writes new scrapes there.
**BridgeBehind**: the oldest raw file not yet written there is more than 15
minutes old, so Spy's lists and numbers are that late. **BridgeFileQuarantined**:
a file failed 3 times (or can never be written) and was set aside, so Spy is
missing its scrapes. Nothing is lost either way: the raw files stay in the
archive and Tracks has loaded them.

## Check

1. On the data box: `sudo bash -c 'set -a; . /etc/adhunters/tracks-loader.env; . /etc/adhunters/tracks-bridge.env; /opt/adhunters/bin/tracks-bridge status'`
   (pending files, the oldest minute, quarantined files).
2. `journalctl -u tracks-bridge --since -30min`: the same file failing, or
   the collector's database refusing (`collector_database` in `/healthz`).
3. Is the loader behind too? Then the shipper or the archive is: see
   [loader-lag](loader-lag.md) and [shipper-behind](shipper-behind.md).
4. Is prodbox up and reachable over Tailscale from the data box?
   `timeout 3 bash -c '</dev/tcp/100.95.67.124/5432' && echo open`

## Fix

- prodbox or its Postgres is down: nothing to do on this side; the bridge
  catches up by itself when it is back (it writes the oldest file first).
- A quarantined file: read its `last_error` (`SELECT * FROM tracks.bridge_file
  WHERE quarantined_at IS NOT NULL`), fix the cause, then write it again:
  `tracks-bridge redo -from <its minute> -to <a minute later>`. Scrapes
  already in the collector's database are skipped, so a redo never counts
  twice.
- The bridge is stuck: `sudo systemctl restart tracks-bridge`. A file cut off
  by the stop is written again and its stored scrapes are skipped.
- Spy cannot wait: go back to the collector scraping (the rollback in
  [platform/SWITCH-OVER.md](../platform/SWITCH-OVER.md)).

## After

BridgeBehind clears once the bridge is under 15 minutes behind;
BridgeFileQuarantined once no file is quarantined (a redo clears the row).
