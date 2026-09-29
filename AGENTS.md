# Agent notes (whole repo)

- Work inside one service's folder at a time. If a change needs two services,
  it goes through `contract/` (a new view version) and says so in the PR.
- Keep services apart so one can't affect another, but share code several of
  them need: it goes in a shared module (`kit`, `contract`, `shared`), never
  copied between services. Never import another service's Go module; move the
  code to `shared` instead (decision 0013).
- Never read another service's tables. Read its `<svc>_api` views.
- A migration that drops, renames or retypes anything needs a decision file
  and goes expand, switch, contract. Build indexes `CONCURRENTLY`.
- Anything from outside (feeds, reports, beacons) is saved raw before parsing,
  and parsing must be re-runnable over any time range.
- Every binary stops cleanly on SIGTERM within 30 s (`kit/run`) and serves
  `/healthz` and `/metrics` (`kit/ops`).
- Never lose collected data. Never change production without the owner's word.
- A PR that changes behaviour updates its folder's README in the same PR.
- Use the words in GLOSSARY.md; add a word there before using a new one.
