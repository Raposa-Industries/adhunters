# 0022 · Spy hands an ad to Create

**Decided:** 1 Oct 2026, with the coordinator's default for the roadmap's
Stage 2 (research an ad in Spy, then make similar creatives).

A Spy ad's "Criar variações" button is a plain link,
`/create/?from=spy&creative=<id>`, where `<id>` is the creative's id in
`tracks_api.creative_v1` (the id Spy's own pages use). Spy sends nothing
else.

Create does the rest, reading only published views: the picture's
`image_url` from `tracks_api.creative_v1`, the newest headline from
`tracks_api.ad_v1`, the brand from `tracks_api.brand_v1`, and the vertical
from `spy_api.creative_class_v1`. It opens a session in that vertical, named
after the ad, adds the ad's picture and headline as items with origin `spy`
(`library_ref` `spy:creative:<id>`), and picks both, ready for a prompt.
When Spy has no vertical for it, the person picks one first. Opening the
same ad again in the same vertical returns that session.

The picture is downloaded once, by Create's server, and kept in Create's
own store like an upload (raw first). Nothing reaches the library until a
person saves, as for any item.

**Why:** a link keeps Spy free of Create's API. Create's login is granted
`tracks_api_read` and `spy_api_read` (platform/servers/setup.sh) for this.
Without `spy_api` it still works, and the person picks the vertical.

The picture is fetched from public addresses only: its address comes from a
scraped page, so Create's server refuses loopback, private and Tailscale
addresses.
