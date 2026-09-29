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
| `cmd/redtrack-probe` | Developer tool: reads an account end to end and saves every raw answer, plus `summary.md`. Also sends single raw calls, including writes. Not a service. |

Nothing here runs on a server yet, so there is no `/healthz` and no schema.
When Intel collects for real, it saves each raw answer before parsing, as
every outside source does, and the reports become re-runnable over any range.

## Running the probe

```sh
export REDTRACK_API_KEY=…            # from your RedTrack account settings
go run ./cmd/redtrack-probe -days 7 probe          # read only
go run ./cmd/redtrack-probe burst 30               # where 429 starts
go run ./cmd/redtrack-probe get /report group=campaign,sub1 date_from=2026-09-01 date_to=2026-09-28
go run ./cmd/redtrack-probe post /sources source.json   # writes: only on an account whose owner said so
```

Output goes to `redtrack-probe-<time>/` (`-out` to change). Cloud sessions
need `api.redtrack.io` in the environment's allowed domains.

## RedTrack: what the API gives

Taken from RedTrack's API reference and the `mcp-redtrack` client;
**not yet confirmed on a live account** (the probe does that).

| Endpoint | Gives |
|---|---|
| `GET /report` | Numbers grouped by one or more groups over whole days: impressions, clicks, conversions, cost, revenue, profit, ROI, CR, EPC, CPC. Groups: campaign, offer, source, landing, network, geo, device, `date`, `hour`, `sub1`…`sub20`, `rt_*`. Filters: `campaign_id`, `source_id`, `offer_id`, `sub1`…. 1000 rows per page. `timezone` overrides the account's. |
| `GET /conversions` | One row per conversion: click id, type, status, payout, the click's sub slots, geo and device. 10 000 per page. |
| `GET /tracks` | One row per click, same shape. 10 000 per page. |
| `GET /campaigns`, `/campaigns/{id}` | Tracker campaigns and their full setup; `total_stat=true` adds numbers. Status 1 active, 2 paused, 3 deleted. |
| `GET /sources`, `/sources/{id}` | Traffic sources, with the sub slot each macro fills. |
| `GET /offers`, `/networks`, `/landings` | Offers, affiliate networks, landing pages. |
| `GET /me/settings` | Time zone, currency, conversion types. |
| `POST`/`PUT` on the same paths | Create and change campaigns, offers, sources and so on. |

Rate limits are not published. The client waits 1 s between requests and
honours `Retry-After` on a 429; `burst` measures the real limit.

## Joining RedTrack with Taboola

- **Keys.** Taboola fills its macros in the tracking code on every click;
  RedTrack's traffic source puts each in a sub slot. The join is
  `{campaign_id}` to Taboola's campaign, `{campaign_item_id}` to its item and
  `{site_id}` to the publisher. Which slot holds which is read from
  `GET /sources/{id}`, never assumed. A report grouped by
  `campaign,subN,subM` then lines up row for row with Taboola's report by
  campaign, item and site.
- **Days.** Both sides count whole days in their account's time zone. Ask
  RedTrack with `timezone` set to the Taboola account's, or the days will
  not match.
- **Cost.** Taboola's own spend is the truth. RedTrack's cost comes either
  from `{cpc}` in each link or from its Taboola integration, so it can drift;
  compare the two and report the gap rather than overwrite either.
- **Clicks.** The two counts differ (clicks lost in the redirect, bot
  filtering). Expected; worth watching, not fixing.
- **Conversions and revenue** exist only in RedTrack.
