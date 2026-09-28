# 0008 · Raposa: one visit per claim, files in object storage

**Decided:** 28 Sep 2026, by Claude while building the new Raposa, following
the design page (v3). Open to change by the owner.

- **One visit per claim.** A worker claims an investigation for one visit,
  writes that visit and the investigation's progress in one transaction, and
  lets go. Any worker on any box picks it up next. The collector carried an
  investigation from baseline to verdict in one call of up to two hours, so
  a deploy cut short every investigation in flight. Now a stop loses at most
  the visit in flight, which is not written and runs again.
- **Visits keep HTML only; the keeper keeps the page whole, once.** A visit
  stores the page's HTML and text. The keeper then opens each new version of
  a deep investigation's pages once and stores its files and video. The
  collector kept every file of every visit, as bytes in Postgres
  (spy.raposa_asset) and base64 in the runner's answers.
- **Files in object storage.** Kept files go to a files store (object storage
  in production, a folder in tests) under their MD5, never into Postgres.
  Postgres keeps which page has which file.
- **Burned lines are per site.** A line is burned for the site (its
  registrable domain) that showed it the white page while other lines on the
  same rung saw the dark page. Only a line burned for many sites is skipped
  everywhere. The collector judged burns per operator, but operators are
  Spy's to decide and Raposa does not read them; the site is what Raposa
  sees itself.
- **Two browser visits and one keep at once**, held on both sides (engine and
  runner), and the runner restarts itself after 200 jobs. More Chromium than
  that got the old runner killed for memory four times in an hour on 27 Sep.

**Why:** the owner runs investigations daily and wants deploys that stop
nothing. Files belong in object storage, not in the database the whole
platform shares, and the browser runner has to fit beside capture on one
box.
