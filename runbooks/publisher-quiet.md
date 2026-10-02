# PublisherQuiet

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

One publisher and device has answered with ads 60% less than the same hours last week, for 2 hours. It may have changed its ad setup, blocked us, or dropped a network.

## Check

1. What does it answer now? `sum by (outcome) (rate(tracks_capture_scrapes_total{publisher="<publisher>"}[1h]))`: `empty` means it answers without ads, HTTP errors mean it refuses.
2. Open the publisher's page in a normal browser: are the ads still there?
3. Read one of its recent raw files to see the answer as received.

## Fix

- Ads moved to another placement or network: update `targets.yaml` (the publisher's slot groups), then restart `tracks-capture@a`, wait 30 s, and restart `@b`.
- The publisher dropped the network: disable it in `targets.yaml`.
- Our lines are blocked there only: note it; a change of lines for that publisher is a code change.

## After

It clears when the rate is back above 40% of last week, or a week after the change.
