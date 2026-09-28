# Raposa browser runner

Headless Chromium behind two HTTP endpoints on `127.0.0.1:8086`, for
raposa-engine on the same box:

- `POST /visit` loads a link under one disguise and follows the dark funnel.
  The engine calls it for the rungs whose disguise says `engine = 'browser'`;
  the cheaper rungs never touch it.
- `POST /keep` keeps one version of one page whole, once (keep.js).

Ported from adhunters-collector e20148c (`browser/`). What changed:

- **The settle bug is fixed.** The old runner.js defined `settle` twice; the
  second (`settle(pending, ms)`) replaced the first (`settle(page)`), so every
  browser visit failed with "pending is not iterable". They are now
  `settlePage` and `settleBodies`.
- **A visit keeps no files.** It counts every response's bytes for the
  metered budget, and returns each step's HTML, text and redirects. The files
  are the keeper's job, once per version. `captureAssets`, `maxAssetBytes` and
  `RAPOSA_MAX_CAPTURE_BYTES` are gone.
- **Slots.** Two visits and one keep at once; anything more is answered 503
  at once and the engine comes back later. The engine holds the same limits.
- **Restarts itself.** After `RAPOSA_BROWSER_RESTART_AFTER` visits and keeps
  (200) it answers 503 to new work, lets what runs finish, and exits 0;
  systemd starts a fresh one.
- **Stops when the engine hangs up.** A visit or keep whose caller went away
  has its browser context closed, so nothing keeps loading for nobody.
- **The keeper names plain video files instead of downloading them**
  (`{"kind": "file", "download": true}`); the engine streams them to disk
  through the same line. Stitched playlists are still joined here.
- `/metrics` serves the runner's numbers; `/healthz` answers 503 without the
  Chromium build.

One visit is one browser context, never reused, so cookies do not leak from
one visit to the next. One Chromium is kept per line and closed after 5 idle
minutes. Lines with a password go through a local relay, so Chromium never
answers a proxy challenge (that switched the cache off and sent
`Cache-Control: no-cache` on every request, which one cloaker served the
white page to).

## Running it

```bash
npm ci --omit=dev
npx playwright-core install chromium   # or point RAPOSA_CHROME at a build
node runner.js
npm test                               # needs Chromium; skipped without it
```

Environment, all optional:

- `RAPOSA_CHROME`: the Chromium binary. Default: the build playwright-core
  1.63.0 installs.
- `RAPOSA_BROWSER_HOST`, `RAPOSA_BROWSER_PORT`: default `127.0.0.1`, `8086`.
- `RAPOSA_IP_URL`: where the exit address is read. Default `https://api.ipify.org`.
- `RAPOSA_NAV_TIMEOUT_MS`: one navigation. Default 45000.
- `RAPOSA_VISIT_TIMEOUT_MS`: one whole visit, browser launch and exit address
  included. Default 110000, well under the engine's 170 s wait, so a slow
  visit still answers with the bytes it spent.
- `RAPOSA_MAX_VISIT_BYTES`: what one visit may transfer before only the pages
  themselves still load. Default 67108864.
- `RAPOSA_BROWSER_RESTART_AFTER`: jobs before a restart. Default 200.

## A visit

Request: `url` (a live link past the baseline), `referer` (the publisher
page; a Taboola click address is served from memory so the landing sees
Taboola's referer without any click reaching Taboola), `proxy`
(`{server, username, password}` or null), `device` (`phone` or `desktop`),
`timezone`, `loadAssets` (false blocks images, video and fonts),
`humanDwell`, `dwellMs`, `maxSteps` (how far past the landing page to follow
calls to action; 0 loads the landing page only).

Answer: `ok`, `error`, `bytes`, `durationMs`, `exitIp`, and `steps`, each with
`stepNo`, `reachedBy` (`landing`, `redirect`, `cta`), `clickedText`,
`clickedUrl`, `url`, `status`, `title`, `html`, `text` and `hops`. A failed
visit still reports the bytes it spent and the steps it had.

The walk stops at a checkout (a card field, or a known checkout host) and
when no call to action is found. An anchor scores its repeat count, plus 3
when its text reads like a call to action and plus 2 for a tracker click
path; footer pages never count. Below a score of 2 it falls back to a button
whose text reads like a call to action.

## A keep

Request: `url`, `html` (the version a visit stored, served in place of the
page so the cloaker decides nothing), `referer`, `proxy`, `device`,
`timezone`, `dir` (absolute; the engine's keep folder on the same box) and
limits. Answer: `renderedHtml` (the page after its scripts ran), `files`
(written under `dir`), `videos` and `notes`. One page gets 10 minutes; past
that it is kept with what it has and says where it was cut.
