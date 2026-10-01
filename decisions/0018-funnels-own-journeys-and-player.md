# 0018 · Funnels: our own journey tracking and VSL player

**Decided:** 29 Sep 2026 by Marcos (own VSL player like Vturb), built 30 Sep 2026.

Funnels follows visitors itself: a page script on our landing pages sends
beacons to a collector on the same domain (`funnels-edge`), saved raw before
parsing (decision 0003), and keyed by the tracker click id so each journey
joins RedTrack's conversions and Taboola's campaign, item and site. Our VSLs
play in our own player, which does what Vturb's does (muted autoplay, smart
progress bar, buttons at the pitch, resume, A/B arms), with the files on
Bunny Stream. RedTrack stays the source of money numbers. Microsoft Clarity
keeps recordings and heatmaps; we don't build those.

The visitor's IP address is never stored: only a keyed hash and its /24 or
/48 network, for the bot check. Journeys that look automated are flagged,
never dropped.

**Why:** RedTrack and Voluum see only redirects and Vturb sees only its
video; none of them ties what happens inside a page to the ad that sent the
visitor, and Vturb's numbers stay in its dashboard. Recordings are a solved
problem that costs nothing with Clarity.

**Added 2026-10-01:** parsed events (`funnels.event`) are kept 90 days;
journeys and counts are kept for good, and the archive keeps every beacon, so
a replay brings any range's events back. Hours still open are counted every
few minutes into a separate `funnels_draft` schema that only Funnels' pages
read, so `funnels_api` stays closed hours only. Journeys from published
cloud networks (AWS, Google Cloud, lists imported by hand) are flagged as
bot suspects; each list is kept as received before it is read.
