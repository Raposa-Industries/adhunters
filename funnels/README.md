# funnels

AdHunters Funnels: our landing sites, and what visitors do on them. It hosts
the pages, follows each journey from the tracker click to the last step, and
plays our VSLs in our own player, built to do what Vturb does. RedTrack stays
the source of money (cost, conversions, payouts); Microsoft Clarity keeps the
recordings and heatmaps. Study: the funnel tracking page (29 Sep 2026).

| Part | What it does | Status |
|---|---|---|
| `funnels-edge` | What visitors reach: the hosted landing sites, the page script at `/ah.js`, and the collector at `/e`, which writes every beacon as received to the spool. No database. | deployed |
| `funnels-loader` | Archives the spool's raw files, loads them into events, closes each hour into journeys and counts, and counts the open hours as drafts. Replays any range. | deployed |
| `web/ah.js` | The page script: the beacon and the VSL player, one file, plus Clarity when the site has it. | deployed |
| `funnels-web` | The pages under `/funnels/`, in the Frame: sites and their landing pages, each page's steps and drop-off split by campaign, videos and their retention curves, one journey by click id, the hosted sites and their versions. | deployed |
| `examples/vsl-demo` | A sample landing site (VSL page, offer, thank-you) marked up for every step, ready to publish on the first landing domain. | built |

```
landing page ──beacon──▶ funnels-edge /e ──▶ spool (one file per minute, zstd)
                                                 │ funnels-loader
                                                 ▼
                                   archive (s3://adhunters-raw/funnels)
                                                 │ parse (again at will)
                                                 ▼
                             funnels.event ─ hour closes ──▶ journeys, counts ─▶ funnels_api
                                   │                            │
                                   └ every 5 min ─▶ drafts ─────┴─▶ funnels-web (/funnels/)
```

## The page script

The edge adds it to every HTML page it serves, at the end of `<head>`, with
a style that hides `data-show-at` elements until the player reveals them. A
page that already loads `/ah.js` is left alone. On pages we don't host (the
team's current ones), one line does the same:

```html
<script async src="https://lp.example.com/ah.js" data-site="lp.example.com"
        data-endpoint="https://lp.example.com/e" data-clarity="abcd1234"></script>
```

It keeps one **journey** per tracker click id in a first-party cookie
(`ah_j`, one day): a landing URL with a new `clickid` starts a new journey;
the next pages of the funnel carry on the same one without it. It reads
`clickid` and `sub1`…`sub10`, `utm_*` from the URL, and sends beacons with
`navigator.sendBeacon` every 5 s while the page is in view, on every click
it counts, and when the page is hidden or left.

The page author marks the page; no code:

| Markup | Means |
|---|---|
| `<meta name="ah-lp" content="bp-02">` | The landing page's name. Without it, its path. |
| `data-ah-step="offer"` | A step, reached when half of it is in view. |
| `data-ah-step-on="click"` | ... or only when it is clicked. |
| `data-ah-click="buy"` | A click worth counting (step `click:buy`). |
| `data-ah-form="lead"` | A form; its first input counts as a start (step `form:lead`). |
| `href="…{clickid}…"` | Filled with the tracker click id, e.g. RedTrack's `https://…rttrk.com/click?clickid={clickid}`. `{journey}` works too. |
| `data-ah-video="bp-02"` | The VSL player (below). |
| `data-show-at="14:00"` | Hidden until the player has played 14:00; shown at once to a visitor who got there before. `data-show-for="bp-02"` ties it to one video. |

Steps the loader adds by itself: `view` (the page loaded), `scroll25` to
`scroll100`, `stay10s` (10 s with that page in view), and the video's
`play:<video>`, `pitch:<video>` and `end:<video>`.

## The VSL player

```html
<div data-ah-video="bp-02"
     data-src="https://vz-xxxx.b-cdn.net/<video id>/playlist.m3u8"
     data-poster="https://vz-xxxx.b-cdn.net/<video id>/thumbnail.jpg"
     data-pitch="14:00" data-curve="2"></div>
```

- **Autoplay, muted**, behind "Your video has already started / Click to
  listen" (`data-unmute-text`, `data-unmute-cta`). The click unmutes and
  starts from 0:00. When the browser refuses even muted autoplay, a "Click to
  watch" box starts it with sound.
- **Smart progress bar**: no scrub bar; the bar runs ahead early and slows
  later (`1 − (1 − t/length)^curve`, curve 2 by default: a quarter of the
  video shows as 44%). Clicking the video pauses and resumes it.
- **The pitch**: `data-pitch` is when the offer starts; reaching it is a step,
  and `data-show-at` elements appear as the video passes their time.
- **Resume**: a visitor who comes back past 30 s is asked "Continue watching"
  or "Start over".
- **A/B arms**: `data-arms='[{"id":"a","src":"…","pitch":"14:00"},{"id":"b","src":"…","pitch":"12:30"}]'`.
  The arm is picked from the journey, so a visitor always gets the same one,
  and every video event carries it.
- **HLS**: Safari and iPhone play `.m3u8` themselves; elsewhere the player
  loads hls.js 1.5.20 from jsDelivr on demand. An `.mp4` or `.webm` plays as it is.
- **Watched seconds** count only while the sound is on and the video plays:
  each second once, a seek ahead leaves a gap. They go out with each beat, so
  a closed tab still counts up to its last second.

Videos live on **Bunny Stream** (it encodes to HLS and serves from its CDN;
we use its files, not its player). Hetzner Object Storage plus a CDN is the
fallback if the bill grows; the player doesn't change.

## Microsoft Clarity

`funnels-edge site -site lp.example.com -clarity <project id>` turns Clarity
on for a site: the page script loads Clarity's tag and tags each recording
with `journey`, `clickid`, `sub1`, `sub4`, `sub8` and `lp`, so a recording can
be found from a funnel number (Clarity › Filters › Custom tags) and back.
Clarity keeps the recordings; nothing of them comes here.

## Hosting landing sites

```
funnels-edge publish  -site lp.example.com ./folder      # a new version, served at once
funnels-edge versions -site lp.example.com               # * marks the one served
funnels-edge serve    -site lp.example.com -version 20260930T200000Z   # roll back
funnels-edge site     -site lp.example.com -clarity abcd1234
```

Run them as the `funnels` user on the data box
(`sudo -u funnels /opt/adhunters/bin/funnels-edge …`). Each site is a folder
under `/var/lib/funnels/sites/<host>/`: every published version is kept, and
`current` links to the one served, switched in one rename. A folder needs an
`index.html`; hidden files and links are refused. `/offer` serves
`offer.html`, a folder serves its `index.html`, `www.` serves the bare host,
and names starting with `_` or `.` are never served. HTML is `no-cache`, the
rest is cached 5 minutes.

A site's domain reaches the edge through the Cloudflare tunnel on the data
box, as a public hostname pointing at `http://127.0.0.1:8098`, with no
Cloudflare Access (landing pages are public). See platform/OPERATIONS.md.

## A sample site

`examples/vsl-demo` is a three-page funnel that uses every mark above: the
VSL player (a public test stream until Bunny's is in), a button revealed at
the pitch with the click id in its link, a lead form, and an offer page that
carries on the journey without a click id. Publish it on a new landing domain
to check the whole path before a real page goes up:

```
cp -r ~/adhunters/funnels/examples/vsl-demo /tmp/ && chmod -R a+rX /tmp/vsl-demo
sudo -u funnels /opt/adhunters/bin/funnels-edge publish -site lp.example.com /tmp/vsl-demo
```

then open `https://lp.example.com/?clickid=test1&sub1=demo` and look for the
journey under hunt-teste.fyi/funnels/ (the open hour shows within 5 minutes).
For a real page, swap the player's `data-src`, `data-poster` and `data-pitch`
for the Bunny files, and the buy link for the RedTrack offer click URL.

## Raw files

One JSON line per beacon, written as received, in
`spool/events/<yyyy>/<mm>/<dd>/<hh>/edge-<instance>-<hhmm>.ndjson`, sealed
with zstd when the minute ends (shared/spool, as capture does). Fields: `id`
(ULID), `at` (the edge's clock), `instance`, `version`, `host`, `origin`,
`referer`, `ua`, `country` (Cloudflare's), `ip_hash`, `net`, and `body` (or
`body_b64` when it is not UTF-8; `truncated` past 64 KB).

The visitor's address is never written: only a keyed hash of it
(`FUNNELS_IP_KEY`; the same address gives the same hash while the key stays)
and its network (`/24` or `/48`), for the bot check. Nothing else personal is
collected: the script never reads form fields.

The edge takes the address and country from Cloudflare's headers only with
`-trust-cloudflare`, set on the data box because the tunnel is the only way
in. The collector answers any origin, so pages on other domains can send to
it.

## funnels-loader

```
funnels-loader migrate
funnels-loader run    -archive s3://adhunters-raw/funnels [-spool /var/lib/funnels/spool] [-close-after 1h]
                      [-draft-every 5m] [-keep-events 2160h] [-networks-every 24h]
funnels-loader replay -from 2026-09-30T00:00:00Z -to 2026-10-01T00:00:00Z
funnels-loader status
funnels-loader networks fetch|list
funnels-loader networks import -archive … -source hetzner ./hetzner.txt
```

Every 10 s it:

1. **Archives** each sealed spool file: uploads it under the same key, checks
   it, records it in `funnels.raw_file`, and leaves a `.archived` marker.
   Archived files leave the spool after 48 hours.
2. **Loads** pending files, oldest first: checks the sha256, parses each
   beacon into `funnels.event` (the file's earlier events are replaced, so
   loading twice equals once), and marks dirty the hour each touched journey
   started in. A beacon that doesn't parse stays in the archive and is
   counted in `bad_beacons`. A file that fails 3 times, or whose bytes
   changed, is quarantined.
3. **Closes** each dirty hour an hour after it ends (`-close-after`), once
   every file that can hold its journeys is loaded: the journeys that started
   in it are rebuilt from all their events, whenever those arrived, then its
   counts. A late event makes its journey's hour dirty and it closes again.
4. **Drafts** each hour that has never closed (the one under way, and those
   waiting for `-close-after`), at most every `-draft-every`: the same
   journeys and counts, written to the `funnels_draft` schema. The pages show
   them marked partial; `funnels_api` never does. An hour's drafts go when it
   closes.

Once an hour it drops events older than `-keep-events` (90 days), never from
an hour still to close: the journeys and counts stay, and the archive keeps
every beacon. To recount an old range, replay it with a day more on each side,
so the journeys crossing its edges have all their events.

`replay` marks a range's files pending again (quarantined ones too); the
running loader does the work. `status` exits non-zero while a file is
quarantined.

A journey is flagged `bot_suspect`, never dropped, when the browser says it is
automated, its user agent is missing or a bot's, it had no input and under
1 s in view, or its network (the /24 or /48) lies inside a **data-center
network**. Counts leave flagged journeys out, except the `bots` columns. The
loader fetches AWS's and Google Cloud's published lists once a day; each is
kept in the archive as received (`networks/<source>/…`) before it is read,
and replaces that source's list in `funnels.dc_network`. Other lists (Hetzner,
OVH, DigitalOcean) go in by hand: `networks import` takes any text with one
network per line and keeps it the same way. A journey is checked when its
hour is counted, so a new list applies from then on; replay a range to apply
it there.

## funnels_api

The views other services read (`contract/sql/funnels/`), counts from closed
hours only:

| View | One row per |
|---|---|
| `journey_v1` | journey: where it began and stopped, time in view, scroll, the video's watched seconds and pitch, bot flag. Join to RedTrack's conversions by `clickid`. |
| `journey_step_v1` | step a journey reached, in order. |
| `journey_hourly_v1` | hour, site, first landing page, sub1/sub4/sub8, device, country. |
| `step_hourly_v1` | hour, landing page, step, sub1/sub4/sub8, device: reached, and stopped there (drop-off). |
| `video_hourly_v1` | hour, video, arm, sub1/sub4/sub8, device: loads, autoplays, plays, watched seconds, reached the pitch. |
| `video_second_hourly_v1` | hour, video, arm, device, second: plays that heard it (the retention curve). |
| `closed_hour_v1` | hour whose counts are current. |

sub1, sub4 and sub8 are Taboola's campaign, item and site id as RedTrack's
Taboola preset fills them; they reach a journey when the landing URL carries
them. Otherwise the join goes through `clickid` and RedTrack.

## funnels-web

The pages under `/funnels/` on the team's address, behind Cloudflare Access
(platform/OPERATIONS.md), in the Frame:

| Page | Shows |
|---|---|
| Visão geral (`/funnels/`) | Each landing site's journeys, how many came with a click id, interacted, time in view and bots; every video's plays. |
| A site (`/funnels/s/<site>`) | Where journeys came in, and each landing page's steps: how many reached each, the share of the page's views, how many stopped there. |
| A landing page (`/funnels/s/<site>/lp?name=<lp>`) | Its steps, and the same split by Taboola campaign (sub1) for its deepest steps. |
| Vídeos, a video (`/funnels/v/<video>`) | Loads, autoplays, plays, seconds heard per play, the pitch; the retention curve per A/B arm; the same per device. |
| Jornadas (`/funnels/journeys?q=`) | One journey by its tracker click id (as RedTrack shows it): pages, steps with times, videos, why it looks like a bot. |
| Hospedagem (`/funnels/hosting`) | The hosted sites, the version each serves, the older ones, Clarity on or off. |

Every page takes a window (today, yesterday, 7 or 30 days, UTC days), a device
and a campaign. Hours still open come from the drafts and the page says so.
`funnels-web` reads Funnels' tables through its own login (`funnels_web`, role
`funnels_web_read`) and writes nothing; publishing stays on the command line.
It listens on `127.0.0.1:8099`; ops on `127.0.0.1:9123`.

## Working here

```
go test ./...                          # PG_TEST_URL=postgres://… for the database tests
node --test web/test/*.test.js
```

Deploy files are in `deploy/`; `platform/servers/setup.sh` installs the three
binaries on the data box, makes the `funnels` and `funnels_web` logins and
the address key, and copies the object storage keys from Tracks' loader.

Next: RedTrack's conversions beside the journeys, once Intel reads them.
