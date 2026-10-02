# SeriesNearLimit

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/boxes.yaml`.

## What it means

The boxes have sent over 9,000 series to Grafana Cloud for 30 minutes, counted as the samples they send a minute (every scrape runs every 60 s). The free plan holds 10,000 and drops what goes over, without a bill: new metrics, and the alerts and dashboards that need them, stop arriving. `platform/observe/README.md`, Series, says what we send and why.

## Check

1. Which box: "Series sent, by box" on Grafana's "AdHunters · Boxes".
2. What grew, in Grafana's Explore: `count by (job) ({__name__=~".+"})`, then `topk(20, count by (__name__) ({__name__=~".+"}))`.
3. Usually a label that took many values (an id, a URL, free text), or a new exporter sending everything it can.

## Fix

- A label with many values: change the metric so the label holds a short fixed set.
- An exporter sending what nothing reads: fewer collectors or a keep-list in `platform/observe/alloy/`, deployed with `setup.sh`.
- Every series is needed: a paid Grafana Cloud plan. The owner decides.

## After

It clears once the boxes send under 9,000 series.
