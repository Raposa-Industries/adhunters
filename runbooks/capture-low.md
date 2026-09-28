# CaptureLow

Page: Telegram, with sound, every 5 minutes until it clears. Rule: `platform/observe/rules/`.

## What it means

Successful scrapes have been under 70% of the same time last week for 10 minutes. Collection runs, but a large part of it is failing or missing.

## Check

1. Which instance dropped? `sum by (box, instance) (rate(tracks_capture_scrapes_total{outcome="ok"}[10m]))` against the same query with `offset 1w`.
2. Which outcomes grew? `sum by (network, outcome) (rate(tracks_capture_scrapes_total[10m]))`. Growth in `http_403` or `http_429` means the network or the proxies push back; `error` means timeouts or connection failures.
3. Was there a deploy in the last hour? A capture build that scrapes less is the first suspect.

## Fix

- One instance down or slow: see [capture-instance-quiet](capture-instance-quiet.md).
- A new build: roll it back (`cp /opt/adhunters/bin/tracks-capture.prev /opt/adhunters/bin/tracks-capture`, restart `@a`, then `@b`).
- Proxies pushing back: see [capture-errors](capture-errors.md).
- Fewer targets on purpose (a publisher removed from `targets.yaml`): silence the alert in Grafana for a week, until last week looks like this one.

## After

It clears when the rate is back above 70% of last week.
