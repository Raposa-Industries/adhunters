# 0025 · Walk pages keep each URL once

**Decided:** 2 Oct 2026, by Claude, under the owner's ask to compact the
data "without losing its accuracy" (2 Oct 2026, the same ask as decision
0023).

Each page of a walk moves from `tracks.walk_page`, which held its two URLs
in full, to `tracks.walk_step`, which points at `tracks.page_url`, where each
URL is kept once (by its md5). Every other value stays as it was, and
`tracks_api.walk_page_v1` keeps the same columns, rows and values, so
nothing that reads it changes; that is why it keeps its version.

It goes expand, switch, contract:

1. **Expand** (`0012_walk_urls_once.sql`): `page_url` and `walk_step`, every
   saved walk page copied into them, and a trigger that copies whatever a
   tracks-walker still on the old code writes to `walk_page`. A tracks-walker
   on the new code writes only `walk_step`, and does not start before the
   data box has this migration.
2. **Switch** (`0013_walk_page_reads_steps.sql`): `walk_page_v1` reads
   `walk_step` and `page_url`.
3. **Contract**: `tracks-loader walks drop` compares every `walk_page` row
   with its copy, value by value, the URLs as text, and drops `walk_page`
   and the trigger only when all of them match. A person runs it, after
   `tracks-loader walks check`. A later migration may then drop the empty
   `walk_page` that a new database still creates (`DROP TABLE IF EXISTS`).

**Why:** walk pages were the second biggest growth after hourly counts
(about 200 MB a day, 2 Oct 2026). The URLs were two thirds of each row,
and the same links come back walk after walk: 1.08 million walk pages had
about 170,000 distinct URLs. On a copy the size of the real table, 1 GB
became 0.24 GB and the copy took a minute. Keeping each URL once loses
nothing. The design's other idea, one row per ad and day after 90 days,
would lose single walks from the database, and is not needed at this size.
Raw walk files stay in the archive for good, so every walk can still be
rebuilt by `tracks-walker replay`.
