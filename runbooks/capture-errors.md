# CaptureErrors

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/`.

## What it means

More than 30% of one network's scrapes have failed for 15 minutes (timeouts, HTTP errors, refused connections). Collection runs, with holes.

## Check

1. Which outcomes? `sum by (outcome) (rate(tracks_capture_scrapes_total{network="<network>"}[15m]))`.
2. Which publishers? Add `publisher` to the `by`. One publisher failing is that publisher; all of them is the network or the proxies.
3. `tracks_capture_lines_cooling_down`: many lines resting means the proxies are being refused.
4. The raw files hold every failed answer with its status and headers: `tracks-capture stats` on the spool, or read one file.

## Fix

- Proxies refused: check the provider (credit, bans). Replace lines in `proxies.env`, then restart `tracks-capture@a`, wait 30 s, and restart `@b`.
- The network changed its endpoint: that is a code change in `tracks/capture/feed/`, by reviewed PR.

## After

Failed scrapes are recorded as scrapes, so coverage numbers show the hole honestly. Nothing to replay.
