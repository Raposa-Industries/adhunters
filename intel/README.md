# Intel

AdHunters Intel judges our own campaigns: what they cost, what they earn,
which ads are better or worse than their campaign, and what looks wrong
right now. It only reads and suggests. Every change it proposes is made in
Launch: a suggestion's button opens Launch with the change filled in, and a
person confirms there. Intel holds no key that can write to Taboola or
RedTrack. Words are in [GLOSSARY.md](../GLOSSARY.md#intel).

## The app

Three binaries, each stopping cleanly on SIGTERM and serving `/healthz` and
`/metrics` on `OPS_ADDR`. Units and example settings are in `deploy/`;
`platform/servers/setup.sh` installs all three on the data box, makes the
`intel` login (with `launch_api_read`) and writes its password into their
settings once.

| Binary | Does | Ops port |
|---|---|---|
| `intel-collect run` | Reads every Taboola login (its own in `intel-collect.env`, and those added on Launch's Contas page, see below) and RedTrack account on a schedule and keeps each answer as received: first in its spool on disk, then in `intel.answer` ([decision 0016](../decisions/0016-intel-answers-in-postgres.md)). It parses nothing. `intel-collect once JOB` runs one job. | 9113 |
| `intel-numbers run` | Every 2 minutes: loads new answers into tables, links moved campaigns, works out results, keeps alerts and suggestions (each sent once to the ops group "AdHunters operation" on Telegram, `OPS_TELEGRAM_CHAT_ID`, or "AdHunters alerts" while that is empty; suggestions with their Launch link). `reload -from D -to D` parses a range of answers again; `status` prints counts. Never talks to Taboola or RedTrack. | 9114 |
| `intel-web` | The pages under `/intel/`, in the Frame (`shared/frame`), on `INTEL_WEB_ADDR` (127.0.0.1:8096) behind Cloudflare Access. Its one write is "not now" on a suggestion. | 9115 |

**Logins added on Contas** (decision 0028). With `LAUNCH_LOGIN_KEY_BASE64`
in `intel-collect.env` (the same value as in `launch-web.env`),
intel-collect reads `launch_api.taboola_login_v1` at start and every 5
minutes, opens each login's sealed secret and proxy, and reads the accounts
chosen for it on Contas like its own logins, named `contas-<id>` in
`intel.answer`. Every request for them, the token included, goes through
the login's proxy and never direct; a login with no proxy, or whose secret
or proxy does not open with the key, is not read and the log says why. An
account one of its own logins already reads is read once, by its own. When a
login is added, removed or changed, intel-collect ends cleanly and systemd
starts it again with the new set.

An account of its own env login that has a proxy on Contas (the "···" menu,
`launch_api.taboola_account_proxy_v1`) is read only through that proxy, by
a client of its own named `<login>-<account>`, the token included; the env
login leaves it out. When its proxy does not open (no key, another key) the
account is not read at all and the log says why; it never goes direct.
What Launch published is kept in `/var/lib/intel-collect/launch.json`
(still sealed, `INTEL_LAUNCH_KEPT`), so a start with the database away
still knows which accounts never go direct. To give it the key, from your
computer:

```
ssh admin@adhunters-data sudo bash -s <<'EOF'
set -e
f=/etc/adhunters/intel-collect.env
v=$(sed -n 's/^LAUNCH_LOGIN_KEY_BASE64=//p' /etc/adhunters/launch-web.env)
[ -n "$v" ] || v=$(base64 -w0 /var/lib/launch-web/login.key)
sed -i '/^LAUNCH_LOGIN_KEY_BASE64=/d' "$f"
[ -z "$(tail -c1 "$f")" ] || echo >>"$f"
echo "LAUNCH_LOGIN_KEY_BASE64=$v" >>"$f"
systemctl restart intel-collect
EOF
```

What intel-collect reads, per Taboola login (Intel keeps to 40 standard and 8
realtime requests a minute of the login's 84 and 10, leaving the rest to
Launch):

| Job | Every | Reads |
|---|---|---|
| realtime | 5 min | The last hour of 5-minute buckets per campaign (what runaway and the gaps are judged on). |
| status | 5 min | Each account's campaign groups and campaign list, so a change of delivery status is seen within minutes. |
| settings | hour | Campaigns, then each campaign's items. |
| reports | hour | Yesterday and today by campaign, by campaign and site, and by item (one call a day: the item report has no day split). |
| month | day | The month so far the same way (and the month before until the 5th). |
| history | hour | Campaign changes of yesterday and today, including ones made in Taboola's own dashboard. Kept raw; not parsed yet. |
| accounts | hour | Which advertiser accounts the login may read, and their time zones. |

And per RedTrack account, grouped by `sub1` (Taboola campaign) with `sub4`
(item), `sub8` (site) or the hour, in each Taboola account's time zone: the
yesterday and today every hour, 7 days once a day, and conversions of yesterday and
today every 15 minutes. Only GETs leave the process (`collect.ReadOnly`).

How the numbers are judged:

- **Days** are the Taboola account's; RedTrack is asked in the same zone.
- **Sales** come from RedTrack. A campaign RedTrack never saw falls back to
  Taboola's count and shows no revenue.
- **Ads** are compared with their campaign on profit per 1,000 impressions,
  with a likely range (`range_level`, 90%). Under 10 sales an ad's profit is
  estimated from its own CTR and landing page click rate, and its campaign's
  sale rate; its rates borrow `prior_strength` clicks of weight from the
  campaign so that small ads are not judged on luck.
- **Alerts:** runaway, tracking gap, landing page gap, postback gap and
  rejected item, each open while it holds and sent once.
- **Delivery status:** every change of a campaign's status (Taboola's
  "Delivery Status": running, paused, pending approval, rejected,
  depleted…), in every account of every login, goes to the ops group,
  grouped into one message per round. Intel adds two statuses of its own:
  - **deleted**, once the campaign has been missing from every campaign list
    for 15 minutes (Taboola's list sometimes leaves out a campaign that is
    still there). An answer without a list in it is a load error, never an
    empty list;
  - **campaign group deleted**, once its group has been missing from the
    group list for 15 minutes. Realize shows "Campaign Group Was Deleted",
    while the campaign list keeps the old status.

  A campaign that appears in an account Intel already reads is sent as new.
  Changes older than `status_alert_max_age_hours` (6) when found are recorded
  but not sent, so a reload or a first run sends nothing old. Both of Intel's
  statuses are final in Taboola, so a change away from either is recorded but
  not sent.
- **Suggestions:** pause ads that would not reach their spend without a sale
  1 time in 20 at the account's usual cost per sale (and spent $10 or more);
  pause the campaign when that is every running ad; halve the daily cap of a
  runaway. "Not now" hides one for a day. Each new one goes once to
  the ops group with an "Open in Launch" link (`INTEL_BASE_URL` plus
  the Launch path), grouped into one message per round; ones older than
  `suggestion_alert_max_age_hours` (6) stay on the pages only.
- Every threshold is a row in `intel.setting`, changed without a deploy.

Moved campaigns: Taboola cannot change a campaign's group, so Launch moves
one by copying it, and the copy has a new id. Launch publishes each move in
`launch_api.campaign_move_v1` (read through the `launch_api_read` role); intel-numbers copies them into
`intel.campaign_link` and the campaign page shows the whole line. Until
Launch publishes that view, nothing is linked.

Intel publishes `intel_api` views for the other apps
(`contract/sql/intel/`): campaign and ad results, open suggestions, alerts
and campaign lines. `intel_api_read` can read every view there, including
ones a later migration adds (default privileges, migration 0007).

Money is never converted: every amount is in the Taboola account's
currency. All four ZoltaGroup accounts (and the network) are USD (checked
2026-10-01), the same as RedTrack's revenue and what Launch shows.

Tests need Postgres: `PG_TEST_URL=postgres://… go test ./...` (each test
makes and drops its own database).

## Other things here

| Path | Does |
|---|---|
| `redtrack/` | Thin client for the RedTrack API: spacing and retries, paging, the key kept out of every answer, error and log line, rows kept as raw JSON fields. Every call, retries included, is counted on `/metrics` by status as provider `redtrack` (`kit/ops` `Transport`). |
| `taboola/` | Read-only client for Taboola's Backstage API, on `shared/taboola` (token handling, retries on 429 and 5xx honouring `Retry-After`): a transport that refuses anything but reads, and the reads (account, campaigns, items, reports). Answers come back raw so they are saved before they are read. |
| `cmd/intel-taboola` | Developer tool: `probe` calls every Taboola read once for one account and writes the raw answers plus `summary.md`. Not a service. |
| `taboola/act` | The Taboola write client for the write tests only (Launch owns every Taboola write), on `shared/taboola` (it repeats nothing but a request answered 401, once, with a new token). A guard checked before every request: one advertiser account, only campaigns and items it created (kept in a state file), new campaigns paused with a fixed bid and a total budget, and a money ceiling on everything it ever turns on. |
| `cmd/intel-taboola-writetest` | Developer tool: runs the approved write tests (T1 to T12 of `research/taboola-api/write-test-plan.md`) through `taboola/act`, saving every request and answer raw. |
| `cmd/redtrack-probe` | Developer tool: reads an account end to end and saves every raw answer, plus `summary.md`. Also sends single raw calls, including writes. Not a service. |

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
    go run ./intel/cmd/intel-taboola-writetest live-start -account X-sc -url URL -tracking 'sub1={campaign_id}&…' -brand NAME -image PHOTO [-ai] -titles 'one|two' -out DIR   # T12, created paused
    go run ./intel/cmd/intel-taboola-writetest live-on    -account X-sc -out DIR            # once approved
    go run ./intel/cmd/intel-taboola-writetest live-cut   -account X-sc -out DIR
    go run ./intel/cmd/intel-taboola-writetest live-pause -account X-sc -out DIR            # hold T12: pause, keep everything
    go run ./intel/cmd/intel-taboola-writetest live-resume -account X-sc -out DIR           # turn it back on as it was
    go run ./intel/cmd/intel-taboola-writetest live-bid   -account X-sc -out DIR -cpc 0.3   # change its bid (guard: 0.50 at most)
    go run ./intel/cmd/intel-taboola-writetest live-end   -account X-sc -out DIR            # pause, reports, delete
    go run ./intel/cmd/intel-taboola-writetest purge      -account X-sc -out DIR [-groups a,b] # items and groups left by our deleted campaigns

Use one `-out` for all of them: its `state.json` is how the guard knows what
is ours and what was turned on. Put Taboola's macros (`{campaign_id}`,
`{campaign_item_id}`, `{site_id}`…) in `-tracking`, the campaign's tracking
code, never in `-url`: Taboola escapes the braces in an item URL, so they
would reach RedTrack unfilled (seen 2026-09-29). `live-start` serves real
ads, so it refuses to start without a real brand, a photo the owner has
rights to and two headlines that match the landing page (`-ai` labels an
AI-made photo, as Taboola asks): placeholders could
be rejected and count against the account. Deleting a campaign leaves its
items in Taboola's review queue, so cleanup deletes the items first
(seen 2026-09-29). Taboola also makes an "AutoGen - <campaign name>" campaign
group for every new campaign and keeps it afterwards, so cleanup and `purge`
delete those groups last; `-groups a,b` names groups of campaigns made before
the client recorded them. The client deletes only AutoGen groups named with
our prefix that no campaign in the account still uses. A campaign counts as
deleted only when the DELETE answers TERMINATED or reading it back gives 404;
one still readable is a cleanup failure (on 2026-09-29 a DELETE left T12 in
place and it was recorded as deleted).

Images must go up with an image content type; Taboola refuses
`application/octet-stream`.

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

- **Keys.** Taboola fills its macros in the campaign's tracking code on every
  click (not in item URLs, where it escapes the braces);
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
