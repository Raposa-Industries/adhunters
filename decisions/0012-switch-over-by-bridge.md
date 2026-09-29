# 0012 · Switch-over by a bridge; history comes in as counts

**Proposed:** 29 Sep 2026, by Claude, building the switch-over. It holds once
Marcos merges it, and each step on the servers waits for his word.

Tracks takes over scraping at a UTC midnight. Until the new Spy launches,
`tracks-bridge` writes Tracks' scrapes into the old collector's database with
the collector's own writer, and the collector keeps running with only its
sweeper off (`SWEEPER_CONCURRENCY=0`); its walker reads a new
`spy.walk_queue` the bridge fills. The proxy lines are split between the two
boxes, never shared. The plan and the commands are in
`platform/SWITCH-OVER.md`.

The collector's history comes into Tracks as counts, not sightings:
`tracks-loader import-old` copies the lookups and every hourly and daily count
before the switch-over, and marks those hours **imported**
(`tracks.hour_state.imported_at`, a new nullable column: nothing dropped,
renamed or retyped). This narrows [0007](0007-keep-times.md) for those days:
their counts can no longer be rebuilt by replay, because the archive has no
raw files for them. The loader and replay refuse imported hours rather than
overwrite them with nothing. The collector's raw history (its sightings and gz
files) is exported to the archive before prodbox is ever switched off.

**Why:** today's Spy reads the collector's tables and read model; feeding
them keeps it working with no change to Spy, and going back is one restart.
Copying counts is what the new Spy needs (Direction's usual weeks, any range),
and it is a few GB against the old sightings' 26 GB. Midnight keeps every day
whole on one side.
