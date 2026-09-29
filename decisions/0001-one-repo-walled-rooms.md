# 0001 · One repository, a walled room per service

**Decided:** 28 Sep 2026, by Marcos. The code-sharing rule was replaced on
29 Sep 2026 by [0013](0013-shared-code.md): services may share code that
several of them need, through a shared module.

Each service is its own Go module and binary, owns one Postgres schema, and
deploys alone. ~~Services share only `kit` (plumbing) and `contract` (published
views).~~ CI fails when a module imports another service's module.

**Why:** agents work on one part without knowing the rest, and one program
running seven jobs meant every change restarted collection.
