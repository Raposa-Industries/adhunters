# 0011 · Spy: every number is sightings per check

**Decided:** 28 Sep 2026. The owner asked for numbers that hold for any
start and end and do not mislead; the Spy metrics handbook (the research
thread) set the method, and Claude built it in `spy/`. Open to change by
the owner.

- **Sightings per check, from hourly counts.** Every Spy number divides
  sightings by the checks (scrapes) of the same publisher, device and hour.
  A raw count mostly measures how much we scraped.
- **Momentum is the Mantel–Haenszel rate ratio** against the subject's
  usual period, comparing each publisher and device only with itself and
  each hour of the week with the same hour. Its likely range is Greenland
  and Robins' variance widened by the subject's measured noise.
- **Every change carries a likely range and a plain word** (clear, likely,
  unclear, too little data). Rising and fading must pass Benjamini–Hochberg
  across the list. No number is shown when there is too little data.
- **Any range.** Every list is computed for any start and end when asked,
  read to the hour for the last 35 days and in whole UTC days before that.
  Only the last 24 hours are stored.
- **Retired:** the collector's Stage, momentum %, phone share, feed
  position, peak and drop from peak, and the handbook's rejected conviction
  index, volatility and half-life. They were raw counts or blends that move
  with check volume. Added: share of voice, lifespan against the vertical,
  and each operator's launch hit rate.
- **Derived numbers are Spy's**, in `spy-numbers`, reading `tracks_api`
  only. They leave the collector, so updating Spy no longer restarts
  collection.

**Why:** the team acts on these numbers (what to copy, whom to watch), and
a number that rises because we scraped more, or a word that flags noise,
costs money. The checks in spy/METRICS.md show the new numbers holding
when checks move and when nothing changes.
