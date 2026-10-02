# SeriesNearLimit

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/boxes.yaml`.

## What it means

Grafana Cloud has held over 9,000 of our series for 30 minutes, counted as Grafana counts them: every box's series and the alerts' own, a series read every 4 minutes the same as one read every minute. The free plan holds 10,000 and drops what goes over, without a bill: new metrics, and the alerts and dashboards that need them, stop arriving. `platform/observe/README.md`, Series, says what we send and why.

## Check

1. Which kind grew: "Series in Grafana Cloud, by kind" on Grafana's "AdHunters · Boxes" (hosts are `integrations/unix`, our services `adhunters`).
2. Which metric, in Grafana's Explore: `topk(20, count by (__name__) ({job="adhunters"}))`, with the kind that grew.
3. Usually a label that took many values (an id, a URL, free text), or a keep-list in `platform/observe/alloy/` that lets a whole family through.

## Fix

- A label with many values: change the metric so the label holds a short fixed set.
- A metric that shows no problem at a glance: take it off the keep-list in `platform/observe/alloy/`, deployed with `setup.sh`. `go test ./tests/` in `platform/observe` fails if an alert or a panel still reads it.
- Every series is needed: a paid Grafana Cloud plan. The owner decides.

## After

It clears once Grafana holds under 9,000 of our series.
