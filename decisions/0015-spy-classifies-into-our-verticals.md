# 0015 · Spy classifies creatives into our own verticals

**Decided:** 30 Sep 2026. The owner fixed the vertical list on 29 Sep
(`spy/verticals/verticals.yaml`) and asked on 30 Sep for Spy to be built on
the new stack; Claude built the classifier in `spy/classify`. Open to change
by the owner.

- **One list.** A creative's vertical is an id of `verticals.yaml` (and its
  category), or nothing. No classifier may answer with anything else.
- **The collector's two passes, ported.** Keyword rules read the headlines
  and descriptions of the creative's newest ads, their brands, and the
  titles of the landing pages Raposa reached from it (`raposa_api`), with
  the collector's points and confidence, our categories in place of its
  broad verticals. Below 0.6 the creative is unsure, and a small model
  trained each day on the sure ones answers when it is at least 0.8 sure
  and surer than the rules. No paid API.
- **Every number cut by vertical uses it.** Direction's market, Size,
  ranges, lifespan curves and the read model read `spy.creative_vertical`,
  which is now a view over Spy's own answer.
- **Migration steps.** Expand and switch happen together in
  `spy/migrations/sql/0007_classify.sql`: the collector's labels move to
  `spy.creative_vertical_old` (import-old still refreshes them, to compare)
  and the name becomes the view. Nothing reads `spy_api` in service yet,
  since spy-numbers is not deployed, so no reader sees the switch. The
  contract step, dropping `creative_vertical_old` and `creative_vertical_v1`,
  waits until the old collector is gone.

**Why:** the old classifier's labels were long made-up names and broad
markets ("health"), and could not be filtered by the list the team uses.
Moving the classifier out of the collector also lets Spy improve it without
restarting collection.

**Not yet:** the collector's rules also read landing page bodies and funnel
steps from its walker, which Tracks does not have yet. When a walker lands,
its pages join the page part.
