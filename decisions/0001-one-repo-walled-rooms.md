# 0001 · One repository, a walled room per service

**Decided:** 28 Sep 2026, by Marcos.

Each service is its own Go module and binary, owns one Postgres schema, and
deploys alone. Services share only `kit` (plumbing) and `contract` (published
views). CI fails when a module imports another service.

**Why:** agents work on one part without knowing the rest, and one program
running seven jobs meant every change restarted collection.
