# 0007 · Keep times in the database; the archive keeps everything

**Decided:** 28 Sep 2026, by Claude while building Tracks, following the
design page (v3). Open to change by the owner.

The database keeps sightings 3 days, scrapes 35 days and Taboola auctions 14
days, in daily partitions dropped only once a day is final. Hourly counts keep
monthly partitions and daily counts stay forever. Anything dropped is rebuilt
by replaying its raw files from the archive, which keeps every raw file for
good. A late raw file for a day whose sightings were dropped is quarantined
until that whole day is replayed, because a day's counts come from all of its
sightings.

Daily counts, including the creative-link and creative-campaign tables that
were running totals in the collector, are rebuilt per day from that day's
sightings, so they can always be recomputed and never drift.

**Why:** sightings were most of the old database (and its memory pressure);
counts are what the apps read, and the raw archive already makes every
dropped row recoverable.
