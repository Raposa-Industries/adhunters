# 0013 · Apps stay apart; code several of them need is shared

**Decided:** 29 Sep 2026, by Marcos. Replaces the code rule in
[0001](0001-one-repo-walled-rooms.md), which came from a misunderstanding.

The goal is unchanged: keep each app as separate as we can, so a change or
failure in one never affects another. Each service is still its own Go module
and binary, owns one Postgres schema, reads others only through their
`<svc>_api` views, and deploys alone.

What changes: services and apps **can share code** that several of them need.
Shared code lives in its own module (`kit` for plumbing, `contract` for the
views, `shared` for domain code such as an ad network client), never inside
one service's folder. A service still never imports another service's module:
when a second service needs code that lives in one, the code moves to
`shared` and both import it, rather than being copied.
`scripts/check-walls.sh` enforces this in CI.

**Why:** copying code between services (Create's Taboola client, ported from
Intel's) doubles every fix and every lesson learned. Importing straight from
another service would let that service's internal changes break the importer;
a shared module keeps that change visible and deliberate.
