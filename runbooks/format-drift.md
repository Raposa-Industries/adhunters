# FormatDrift

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/`.

## What it means

More than 2% of one network's answers no longer parse (loaded as `unparsed`). The network probably changed its answer format. Every answer is still in the archive, so nothing is lost; the numbers undercount until the parser is fixed.

## Check

1. When did it start? `sum by (network, outcome) (rate(tracks_loader_scrapes_total[30m]))` over the last day.
2. Read a raw file from after the start and compare it with `tracks/parse/testdata/`.
3. One publisher or all? The parser's log lines name the file and the record.

## Fix

- Fix the parser in `tracks/parse/` with a test built from a real record of the new format, by PR.
- After the deploy, replay from the start of the drift: `tracks-loader replay -from <start> -to <now>`.

## After

It clears once new answers parse again. The replay brings back the hours in between.
