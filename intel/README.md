# Intel

AdHunters Intel will judge our own campaigns: what they cost, what they
earn, and which ads, publishers and placements are worth more money. The app
is not built yet (build order: Tracks, then Raposa, then the other apps).
For now this folder holds the clients that read our performance data, so the
app has something tested to stand on. Words are in
[GLOSSARY.md](../GLOSSARY.md#intel).

## What is here

| Path | Does |
|---|---|
| `redtrack/` | Thin client for the RedTrack API: spacing and retries, paging, the key kept out of every answer, error and log line, rows kept as raw JSON fields. |
| `taboola/` | Read-only client for Taboola's Backstage API: token handling, retries on 429 and 5xx (honouring `Retry-After`), and the reads (account, campaigns, items, reports). Answers come back raw so they are saved before they are read. |
| `cmd/intel-taboola` | Developer tool: `probe` calls every Taboola read once for one account and writes the raw answers plus `summary.md`. Not a service. |
| `taboola/act` | The Taboola write client, for intel-act and its tests only. A guard checked before every request: one advertiser account, only campaigns and items it created (kept in a state file), new campaigns paused with a fixed bid and a total budget, and a money ceiling on everything it ever turns on. |
| `cmd/intel-taboola-writetest` | Developer tool: runs the approved write tests (T1 to T12 of `research/taboola-api/write-test-plan.md`) through `taboola/act`, saving every request and answer raw. |
| `cmd/redtrack-probe` | Developer tool: reads an account end to end and saves every raw answer, plus `summary.md`. Also sends single raw calls, including writes. Not a service. |

Nothing here runs on a server yet, so there is no `/healthz` and no schema.
When Intel collects for real, it saves each raw answer before parsing, as
every outside source does, and the reports become re-runnable over any range.

## Running the RedTrack probe

```sh
export REDTRACK_API_KEY=…            # from your RedTrack account settings
go run ./cmd/redtrack-probe -days 7 probe          # read only
go run ./cmd/redtrack-probe burst 30 /report       # where 429 starts on one path
go run ./cmd/redtrack-probe get /report group=campaign,sub1 date_from=2026-09-01 date_to=2026-09-28
go run ./cmd/redtrack-probe post /sources source.json   # writes: only on an account whose owner said so
```

Output goes to `redtrack-probe-<time>/` (`-out` to change). Cloud sessions
need `api.redtrack.io` in the environment's allowed domains.

## Taboola: reading and writing kept apart

The read client (`taboola`) refuses every request except GETs under
`/backstage/api/1.0/` and the token POST, before it leaves the process.
Writing lives only in `taboola/act`, behind its guard. On the lent account it
runs only what the user approved on 2026-09-29: tests that leave running
campaigns alone and spend $20 at most, in total.

    go run ./intel/cmd/intel-taboola-writetest paused     -account X-sc -url URL -out DIR   # T1 to T11, $0
    go run ./intel/cmd/intel-taboola-writetest live-start -account X-sc -url URL -tracking 'sub1={campaign_id}&…' -out DIR   # T12, created paused
    go run ./intel/cmd/intel-taboola-writetest live-on    -account X-sc -out DIR            # once approved
    go run ./intel/cmd/intel-taboola-writetest live-cut   -account X-sc -out DIR
    go run ./intel/cmd/intel-taboola-writetest live-end   -account X-sc -out DIR            # pause, reports, delete

Use one `-out` for all of them: its `state.json` is how the guard knows what
is ours and what was turned on. Put Taboola's macros (`{campaign_id}`,
`{campaign_item_id}`, `{site_id}`…) in `-tracking`, the campaign's tracking
code, never in `-url`: Taboola escapes the braces in an item URL, so they
would reach RedTrack unfilled (seen 2026-09-29).

## Running the Taboola probe

    export TABOOLA_CLIENT_ID=… TABOOLA_CLIENT_SECRET=…
    go run ./intel/cmd/intel-taboola probe -out /tmp/taboola-probe [-account acme-sc] [-days 30] [-items 5]

- The credentials come from Taboola (Backstage API client id and secret). They
  live in environment variables, never in files in the repo or in chat. The
  token is never written.
- The host `backstage.taboola.com` must be reachable. Cloud sessions need it
  on the environment's allowed domains.
- Without `-account` the probe reads the credentials' own account. A
  network account lists no campaigns itself, so campaigns and items are also
  read from every advertiser account the credentials may read.
- Reports cover the last `-days` days in the account's time zone. The splits
  read: day, campaign, campaign by day, site, campaign by site by day,
  country, platform, hour of day, campaign by hour (the last 2 days only:
  Backstage allows 48 hours for it), and item.
- Backstage allows 84 requests a minute per client (`Ratelimit-Policy`).
  Its reports refresh about hourly. Campaign by site by day is big (43 MB
  for 30 days of one busy network account).
- A failed read is recorded in the summary and the probe carries on.

Tests: `go test ./...` (against a stand-in server; no network).

## Before building more here

What the first read test found, and what is still unknown, is in the
project's shared files (`research/taboola-api/findings.md`). Anything in
Intel beyond this client and probe waits for the Intel replan the owner
asked for on 2026-09-29, which reads those findings first. Two facts that
shape it: Taboola has everything before the click and the real cost,
RedTrack everything after it; joining them needs the campaign, item and
site ids on both sides.

## RedTrack: what the API gives

Taken from RedTrack's API reference and the `mcp-redtrack` client, then
checked on 2026-09-29 against a fresh, empty sandbox account: every path
below answers, but with no traffic yet, row fields are still unconfirmed.
Raw answers: `research/redtrack/` in the project files.

| Endpoint | Gives |
|---|---|
| `GET /report` | A bare JSON array. Numbers grouped by one or more groups over whole days: impressions, clicks, conversions, cost, revenue, profit, ROI, CR, EPC, CPC. Groups: campaign, offer, source, landing, network, geo, device, `date`, `hour_of_day` (not `hour`), `day_of_week`, `sub1`…`sub20`, `rt_*`; combinations such as `campaign,date,hour_of_day` work. An unknown group answers HTTP 500 "Problem with loading report", not 400. Filters: `campaign_id`, `source_id`, `offer_id`, `sub1`…. 1000 rows per page. `timezone` overrides the account's. |
| `GET /conversions` | `{"items": [...], "total": n}`; `date_from` and `date_to` are required. One row per conversion: click id, type, status, payout, the click's sub slots, geo and device. 10 000 per page. |
| `GET /tracks` | One row per click, same envelope and required dates. 10 000 per page. |
| `GET /campaigns`, `/campaigns/{id}` | An array, or `null` when there are none (`/campaigns/v2` sends `{"items": null, "total": {}}`). Tracker campaigns and their full setup; `total_stat=true` adds numbers. Status 1 active, 2 paused, 3 deleted. |
| `GET /sources`, `/sources/{id}` | Traffic sources, with the sub slot each macro fills. |
| `GET /offers`, `/networks`, `/landings` | Offers, affiliate networks, landing pages. |
| `GET /me/settings` | Not the time zone: the web app's table layouts. Useful anyway, as `table_campaigns_report` lists every report column id (241: `clicks`, `unique_clicks`, `lp_views`, `lp_clicks`, `conversions`, `convtype1`…`20`, `revenue`, `revenuetype1`…`20`, `cost`, `profit`, `roi`, `cpc`, `cpa`, `epc`, `approved`, `pending`, `declined`…). No time zone endpoint was found, so reports should always pass `timezone`. |
| `GET /source_presets` | RedTrack's ready-made traffic sources (Taboola, NewsBreak, Outbrain…) with their macros; `page=2` for the rest. |
| `POST` on the same paths | Create. A source needs only `title`; copy a preset's fields and `preset_id` to get its macros. An offer needs `title`, `url` and `program_id` (the network's id). A campaign needs `title` and `source_id` and comes back with `trackback_url`, the link to paste into Taboola. |
| `PUT`, `DELETE` | `PUT /sources/{id}` answered 403 "action not allowed" with this key; `PUT /campaigns/{id}` is allowed. `DELETE /sources/{id}` works (204). |

Rate limits (measured 2026-09-29; RedTrack does not publish them): `/report`
and `/conversions` each have their own "20 a minute, 2 a second" allowance
over a calendar minute, shown in `X-Ratelimit-*` headers, but one request
often uses two of the 20, so about 10 fit a minute. Lists (`/campaigns`,
`/sources`) and `/me/settings` took 25-30 back-to-back requests with no
limit. A 429 carries `Retry-After` (up to the minute's end). So the client
spaces `/report`, `/conversions` and `/tracks` 6 s apart, everything else
0.2 s, waits for the reset when a minute's allowance reads 0, and retries a
429 after `Retry-After` up to 5 times apart from its 5xx retries. A full day
by campaign, item and site is one to a few pages, so this is no bottleneck.

## Joining RedTrack with Taboola

- **Keys.** Taboola fills its macros in the tracking code on every click;
  RedTrack's traffic source puts each in a sub slot. The join is
  `{campaign_id}` to Taboola's campaign, `{campaign_item_id}` to its item and
  `{site_id}` to the publisher. A report grouped by `campaign,sub1,sub4,sub8`
  then lines up row for row with Taboola's report by campaign, item and site.
- **The slots.** RedTrack has a Taboola preset (`GET /source_presets`, which
  pages 100 at a time; Taboola is `5b2781722c822c00013beb61`, NewsBreak
  `650995e8da57fc0001f4fa69`). A source created from it (checked 2026-09-29
  in the sandbox; raw answers in `research/redtrack/source-2026-09-29/`)
  gives this tracking link:

  | Slot | Taboola macro | RedTrack role |
  |---|---|---|
  | `sub1` | `{campaign_id}` | campaign id (`cid`) |
  | `utm_source` (sub2) | `Taboola` (fixed) | `rt_source` |
  | `utm_campaign` (sub3) | `{campaign_name}` | `rt_campaign` |
  | `sub4` | `{campaign_item_id}` | ad id (`aid`) |
  | `sub5` | `{site}` | publisher name (`pid`) |
  | `sub6` | `{title}` | headline |
  | `sub7` | `{platform}` | platform |
  | `sub8` | `{site_id}` | placement (`rt_placement`) |
  | `sub9` | `{thumbnail}` | image URL |
  | `sub10` | `{timestamp}` | click time |
  | `ref_id` | `{click_id}` | Taboola's click id, sent back in the conversion postback |

  The preset has no `{cpc}`. The team's real account may have edited its
  source, so read its `GET /sources/{id}` before trusting this table for it.
- **Cost.** Taboola's own spend is the truth. The preset's cost comes from
  RedTrack's Taboola API integration (`integration_types.cost_update`, at
  campaign and placement level; RedTrack's note says support must switch on
  the API rules), not from the link. Either way it can drift from Taboola's
  report; compare the two and report the gap rather than overwrite either.
- **Conversions back to Taboola.** The preset's postback is
  `https://trc.taboola.com/actions-handler/log/3/s2s-action?click-id={ref_id}&name=EVENT_NAME`.
- **NewsBreak** has a preset too: `__CAMPAIGN_ID__` (cid), `__FLIGHT_ID__`
  (ad group), `__CREATIVE_ID__` (ad), their names, `__OS__`, and
  `__CALLBACK_PARAM__` as `ref_id`; cost at campaign, ad group and ad level.
- **Days.** Both sides count whole days in their account's time zone. Ask
  RedTrack with `timezone` set to the Taboola account's, or the days will
  not match.
- **Clicks.** The two counts differ (clicks lost in the redirect, bot
  filtering). Expected; worth watching, not fixing.
- **Conversions and revenue** exist only in RedTrack.
