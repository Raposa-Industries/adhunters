# WalkerFailing

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/collection.yaml`.

## What it means

More than 20% of tracks-walker's walks failed in the last hour, for 30 minutes. Usually 5 to 10% fail: landing pages that refuse us or are gone. More than that is our side: proxy lines being blocked, timeouts, or a bug.

## Check

1. Which outcome? `sum by (outcome) (rate(tracks_walker_walks_total[1h]))`: `http_error` is the page answering 4xx or 5xx; `error` is no answer (timeouts, too many redirects, refused).
2. Which line? `platform/retire/collection-check.sh` has "Walker per line" and "Walk errors": one line far below the others is that line being blocked.
3. Which sites? The same section: one site failing hundreds of times is that site, not the walker.

## Fix

- A blocked line: replace it in `/etc/adhunters/tracks-capture/proxies.env` (capture uses the same file), then restart tracks-walker and capture one instance at a time.
- One site: nothing to do; it fails honestly in the walk records.
- A bug (a new error after a deploy): roll back to `.prev` and fix by PR.

## After

It clears once failures are back under 20%. Failed walks are kept as raw walk files and retried with a backoff; nothing to replay.
