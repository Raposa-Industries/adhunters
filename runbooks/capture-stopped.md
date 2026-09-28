# CaptureStopped

Page: Telegram, with sound, every 5 minutes until it clears. Rule: `platform/observe/rules/`.

## What it means

No box has made a successful scrape for 3 minutes. Nothing new is being collected. Raw files already in the spool or archive are safe.

## Check

1. Is it one box or all? In Grafana, `sum by (box, instance, outcome) (rate(tracks_capture_scrapes_total[5m]))`. If there are no series at all, the capture processes are down; if there are series but no `ok`, they run and every scrape fails.
2. If the worker box is gone too, **BoxGone** is firing: follow [box-gone](box-gone.md) first.
3. On the box (over Tailscale): `systemctl status 'tracks-capture@*'` and `journalctl -u 'tracks-capture@*' --since -15min`.
4. All scrapes failing with `http_403` or `error`: the proxy lines are refused or down. Check the proxy provider's dashboard and `tracks_capture_lines_cooling_down`.

## Fix

- Processes down: `systemctl restart tracks-capture@a`, wait 30 s, then `@b` (never both at once). If they crash at start, the journal says why; a bad `targets.yaml` or `proxies.env` is the usual cause, and `.prev` of the binary is the rollback (`/opt/adhunters/bin/tracks-capture.prev`).
- Proxies refused: fix at the provider (credit, credentials). Capture reads its lines at start: restart `@a`, wait 30 s, then `@b`.
- The worker box is dead: raise the standby box to all 8 workers (set `WORKERS=8` in `/etc/adhunters/tracks-capture@standby.env`, then `systemctl restart tracks-capture@standby`).

## After

It clears by itself once scrapes succeed again. Nothing to replay: what was not scraped was never received.
