# Spy

AdHunters Spy finds the ads that are doing well, so we can make our own
like them. This folder holds its derived numbers, the classifier that puts
each creative in one of our verticals, auction prices, and the app's pages
(`spy-web`, in the shared Frame). Every number is sightings per check, built from Tracks'
hourly counts: [METRICS.md](METRICS.md) defines each one, says how precise
it is for any range, and shows how we checked it does not mislead.

The numbers moved here from adhunters-collector (`internal/spy`, its spy
schema 018 to 034, at `e20148c`), which runs them inside the collector
today. The judgements were rebuilt from the Spy metrics handbook; the read
model and Direction were ported.

## What runs

| Binary | Does | Listens |
|---|---|---|
| `spy-numbers run` | Every minute, the last 24 hours (rebuilt only when Tracks closes an hour). Every 5 minutes, the new landing page walks, the classifier, the read model, then Size and Direction; every 15, operator grouping. Every hour, auction prices. | ops on `OPS_ADDR` (9122) |
| `spy-web` | Spy's pages and their JSON under `/spy/`, behind create-web's sign-in. Reads only published views. | `SPY_WEB_ADDR` (127.0.0.1:8097), ops on 9116 |

```
spy-numbers migrate                applies the migrations (the unit runs it before each start)
spy-numbers run
spy-numbers refresh [-rebuild] [-pages-since T]
                                   every job once; -rebuild redoes Direction's daily part first,
                                   -pages-since reads the landing page walks from T again
spy-numbers import-old             copies the groupings, pages, Direction history and prices
                                   from the collector (OLD_DATABASE_URL, read only)
spy-numbers status
spy-numbers check                  the walker, the classifier and the grouping on live data, read only
```

`/healthz` fails when the read model or Direction has not succeeded for 20
minutes. Each job is also a kit task (`adhunters_task_*`), promising the
last 24 hours and prices every 90 minutes, grouping every 45 and the others every 20, so the TaskLate
alert covers them. `/metrics` has `spy_numbers_runs_total{job,outcome}`,
`spy_numbers_seconds{job}`, `spy_numbers_rows{job}`,
`spy_numbers_last_success_timestamp_seconds{job}` and
`spy_recent_window_end_timestamp_seconds`. The unit and example settings
are in [deploy/](deploy/); `platform/servers/setup.sh data` installs both on
the data box.

Any other range needs nothing running: the range functions compute it when
asked, from Tracks' counts.

## The numbers

| Part | What | Kept in | Refreshed |
|---|---|---|---|
| Ranges | Presence, share of voice, momentum with its likely range and word, lifespan, launch hit rate, for creatives, operators, verticals and publishers over any start and end | computed when asked | never stored |
| Last 24 hours | The ranges over the last 24 closed hours, stored so lists open fast. They end at the first hour not closed in the last 6 days, so holes in the collector's history (hours it never scraped) never hold them back | `creative_recent`, `operator_recent`, `recent_window` | when Tracks closes an hour |
| Read model | Totals per creative, operator and publisher (today, yesterday, 3, 7 and 30 days), who runs it, its vertical, new, running, junk | `creative_stats`, `operator_stats`, `publisher_stats`, `creative_day` | every 5 minutes |
| Size | Share and rank in its vertical, over 24 hours and 7 days; scaled | `size_stats` | hourly |
| Direction | Rising, steady, fading or stopped right now, against the same hours of the usual weeks, with a sentence for people | `direction_stats`, `direction_event` | every 5 minutes |
| Verticals | Each creative's category and vertical from our list, how sure, and why | `creative_class`, `class_model` | every 5 minutes (the model daily) |
| Auction prices | Per day, ad, publisher and device: auctions, clearing price, bid and cap (Taboola), bid and second price (NewsBreak) | `price_day` | hourly, yesterday and today |
| Sparklines | Each creative's presence per day | computed when asked | never stored |

### Direction

The last 2 hours (the open hour included) against the same hours and
weekday of the last 3 weeks, on the publishers and devices where the
subject usually runs, for ads, creatives, operators and verticals. The gap
is measured in the subject's own noise (`spy.dispersion`, measured daily),
and entering rising or fading must also pass Benjamini–Hochberg across the
run; leaving uses the old thresholds, so words do not flicker. Every change
is written to `direction_event` for alerts.

The collector's `direction_fill_usual` took about 4 minutes per slot hour
on a CX43 (tracks/measure/hourclose-direction). It now goes through
analysed temporary tables; [measure/](measure/results-20260928.md) has
the timings at Tracks' volume (Direction every 5 minutes: 5 s; a creative
list over a custom range: 10 to 15 s, so the app must run it with a
longer timeout and keep the answer).

### Groupings

Until Spy's own grouping is switched on, operators and which operator each
account belongs to come from the collector: `spy-numbers import-old` copies
them over (repeatable: each run replaces the last), matching accounts by
network and external id and creatives by creative key; rows Tracks has not
seen yet are skipped and counted in `import_mark`. It refuses to copy an
empty grouping. Operator ids are kept, so OP123 means the same operator
everywhere, and so are names typed by hand (`name_is_manual`) and each
operator's seller.

So nothing the collector gathered is lost when its database goes, the same
command also copies (migration 0013, `importold/collector.go`):

| What | Into | How |
|---|---|---|
| Sites, clues, site clues, sellers | `site`, `clue`, `site_clue`, `seller`, `site_seller` | Merged by domain, kind and value, platform and account; first and last seen widen. Its certificate and name-server clues cannot be found again from Tracks' walks. |
| Which accounts' clicks reached which sites | `account_site` | Accounts Tracks has not seen are skipped. |
| Hand grouping fixes | `grouping_fix` (`old_id`, `made_by`) | Replaced on each run; Spy's own stay. A join whose operator is gone is skipped. |
| Agencies | `agency` (`name`, `imported`) | An imported agency's name root never joins accounts. |
| Direction changes | `direction_event` (`old_id`) | Only before Spy's own began; ads and creatives get Tracks' ids. Vertical events name the collector's verticals and stay in the archive. |
| Taboola auctions and NewsBreak prices | `price_day` (`imported`) | See Auction prices. |

### Landing pages and operator grouping

Tracks walks each running ad's link to its landing page and one next step
(`tracks-walker`, published as `tracks_api.walk_page_v1` and
`page_version_v1`). Every 5 minutes `spy.refresh_pages` reads the walks
since its last run into:

| Table | What |
|---|---|
| `site` | A landing page's registrable domain (`www.go.acme.co.uk` is `acme.co.uk`). On a hosting domain (`myshopify.com`, `vercel.app`, …) the whole host is the site; a platform (`clickbank.net`, `amazon.com`, …) is never one. Both lists are rows of `shared_domain`. |
| `clue`, `site_clue` | What a site's pages carry that names who runs them: pixel ids (Meta, Google Ads, Analytics, Tag Manager, TikTok, Pinterest, Snapchat, Clarity, NewsBreak), emails and company names. Strong clues are one business's; free mail, platform addresses and company names are only hints (`spy.clue_of`, the collector's rules). |
| `seller`, `site_seller` | The merchant account on a checkout platform ("ClickBank slimpro") behind a site's landing page or its next step. |
| `account_site` | Which account's click reached which site. |
| `creative_page` | Each creative's newest landing page that answered, for the classifier. |

Everything is kept as first and last seen, so reading a walk twice changes
nothing; `refresh -pages-since` reads a range again after a Tracks replay or
a `shared_domain` change.

Every 15 minutes `spy.regroup_operators` groups sites and accounts by the
collector's rules: sites sharing a strong clue belong together (a clue on
more than `clue_max_sites`, 20, joins nothing); an account whose clicks
reach at most 2 of those groups joins them, one reaching 3 or more is its
own operator (arbitrage); accounts sharing a name root join, unless the
root reaches 3 or more groups or its accounts belong to 3 or more operators
today (an agency; the second test is migration 0012, for agencies whose
other clients the walker has not reached yet); accounts with the same email in
their name join; `grouping_fix` rows (join or split, by hand) win. A group
keeps the operator most of its members have now, so OP codes stay. Kind is
arbitrage, affiliate (a site selling through ClickBank, BuyGoods, MaxBounty,
Digistore24 or JVZoo) or direct; the name is the oldest running account,
the seller and the code.

It runs in shadow first. The answer goes to `grouping_group` and
`grouping_member`, and each run's `grouping_run` row counts the accounts
grouped as today, moved, newly grouped, and on no landing page yet;
`spy-numbers status` shows the last one. `spy-numbers check` shows what to
read before switching: the walker's last 24 hours, the classifier, how the
accounts with an operator now compare, and the proposed groups that differ
most, with what tied most of each group's accounts in. Operators change only when the setting `operators_from` is
`grouping`: then the grouping writes `operator`, `account_operator` and
`site.operator_id`, and import-old copies the verticals only. An account no
landing page has shown yet keeps the operator it has (migration 0011); one
leaves its operator only when the grouping places it elsewhere or a hand fix
splits it out. Switching is one row:

```
UPDATE spy.setting SET text_value = 'grouping' WHERE name = 'operators_from';
```

### Verticals

[shared/verticals/verticals.yaml](../shared/verticals/verticals.yaml) is our own fixed list
of verticals: 59 of them in 16 categories, so a list can be filtered by
category first, then vertical. Home & Garden, Phones & Apps and Celebrity &
Viral were added on 2026-10-01, from the most seen running ads that had no
vertical. A classifier may only answer with one of its
ids, or with nothing; it never makes up a name. Each vertical has a short
name (20 characters at most), what it covers, the near neighbours that go
elsewhere, and the keywords and hints the old rules used. The
`verticals` package loads it, and its test keeps ids and names unique, each
keyword in one vertical only, every "goes elsewhere" pointing at a real
vertical, and the must-haves (Blood Pressure, Memory Loss, Weight Loss,
Tinnitus, Diabetes, Neuropathy, Prostate Health, Joint Pain, Vision)
present.

### The classifier

`spy/classify` puts each creative in one vertical of the list (decision
0017), in two passes:

- **Rules.** The list's keywords (2 points) and hints (1 point) over the
  newest ads' headlines and descriptions (weight 3), their brands (2), the
  walked landing page's title, h1 and h2 and description with the titles of
  the pages Raposa visited (2), and the first 3,000 characters of the
  landing page's text (1). The category with the most
  points wins, then its best vertical; a catch-all only when no specific
  vertical scored. The answer carries its confidence (0 to 1; under 0.6 it
  is unsure) and the words that decided it.
- **Model.** A softmax over words, trained daily on the rules' sure answers
  (at least 50, at most 600 per vertical, catch-alls left out), with the
  keywords masked half the time so it learns the rest of the text. It
  answers only where the rules are unsure and it is at least 80% sure.
  It is also retrained early once the sure answers reach 1.5 times what the
  newest model learned from (and at least 200 more), so a model trained
  while the first reads were filling in does not stand for a day. When it
  declines, its top guesses are still kept in `model_top`, and
  `spy-numbers check` lists the most seen running ads with no vertical
  beside them. The last 7 models are kept.

A creative is read again when a newer ad, a changed landing page or newer
Raposa evidence arrives,
or when verticals.yaml changes (its hash is stored with each answer). The
collector's labels are kept in `creative_vertical_old`;
`spy.creative_vertical` is now a view over `creative_class` with vertical
ids, so the read model and ranges use our list.

### Auction prices

Tracks keeps every Taboola auction a check saw (`tracks_api.auction_v1`,
new in this change) and NewsBreak's bid and second price on each sighting.
`spy.refresh_prices` sums them per day, ad, publisher and device with
their quartiles, redoing yesterday and today each run (14 days on the
first). The ad page shows the average and the typical value (the middle of
the daily medians, weighted by auctions). The unit of Taboola's prices is
not confirmed yet.

`import-old` adds the collector's prices (`importold/prices.go`), marked
`imported` and replaced on each run: its Taboola auction log
(`public.adhunters_rtb_auction_log`) and the NewsBreak prices on the
sightings it still keeps (one day at a time). The log names no ad: an
auction reaches one through its campaign item (the Tracks link with that
item id, the creative clicked through it most, and that creative's most
seen ad that day on that publisher and device, else its most seen ad), so
prices per creative are exact and a creative with several headlines puts
them on its leading one. An auction whose item Tracks has no link for
goes to its campaign's most seen creative; the import prints how many
went each way, and why the rest were skipped (no creative, no publisher,
or an unknown device). Domains are compared without `www.`. The log kept no RTB flag; a winning seat that
names RTB or a competing seat counts as RTB. Where Spy already has a row
from Tracks for the same day, ad, publisher and device, Tracks' row stands,
and `refresh_prices` replaces a copied row when Tracks has one.

## The app

`spy-web` serves, in the shared Frame (`shared/frame`), with a Portuguese UI:

| Page | Path | Shows |
|---|---|---|
| Anúncios | `/spy/` | Every creative in the range as a card with its sparkline; filters by text, category, vertical, status, device, network, publisher, tracker, affiliate network, days active; sorts by presence, momentum, share of voice and more |
| Anúncio | `/spy/ads/{creative}` | Numbers, direction, presence per day (the last 30 days, or each day of a chosen range, up to 120), hour of day (São Paulo) over the range's last 7 days, publishers, auction prices, its ads, links, campaigns, Raposa's investigations with the button to ask for one, and **Criar variações**, a link to `/create/?from=spy&creative={creative}` that opens Create on a new session with this ad's image and headline |
| Operadores | `/spy/operators/` and `/{id}` | Operators by presence, momentum, launches and hit rate; one operator's creatives, accounts, brands and publishers |
| Publishers | `/spy/publishers/` and `/{id}` | Checks and sightings per publisher; its top operators and creatives |
| Mercado | `/spy/pulse/` | Each vertical's presence and momentum, how many are rising, fading and stopped now, and the latest changes of direction |

⌘K searches ads, operators, publishers and verticals. Every page has the
range picker (24 hours, 7, 30, 90 days, or any dates). The last 24 hours
read what spy-numbers keeps ready; any other range is counted when asked
(10 to 15 s at Tracks' volume) in a transaction with a 90 s limit, one
query per address at a time, and kept 10 minutes (the last 24 hours, 2).

Tracks keeps hourly counts 35 days after their month ends, then moves them
to hour files in its archive (tracks/README.md, decision 0023); daily counts
stay, and ranges older than `hourly_days` already read whole days. For an
older range, the ad page asks Tracks for the archived days of its hour of
day back (`tracks_api.hourly_days_v1`), says so on the panel, and asks
`ads/{id}/hours` (never cached) every 15 s until they are in, a minute or
two later.

The JSON is under `/spy/api/` (`ads`, `ads/{id}`, `ads/{id}/hours`, `operators`,
`publishers`, `pulse`, `search`, `events`, `verticals`, `facets`);
`POST /spy/api/ads/{id}/investigate` asks Raposa through
`raposa_api.request_investigation_v1` and records who asked. It takes only
JSON from the same origin.

People reach it through create-web's sign-in (create/README.md), which
replaced Cloudflare Access on 2026-10-01, so `ACCESS_TEAM` and `ACCESS_AUD`
stay unset and spy-web listens only on localhost; who asked for an
investigation is then recorded empty. With them set (Access, no longer
used), every request needs a valid Access token (`shared/access`).

Setting it up: `platform/servers/setup.sh data` makes the `spy` login (owns
the spy schemas; `tracks_api_read`, `raposa_api_read`) and the `spy_web`
login (`spy_api_read`, `tracks_api_read`, `raposa_api_read`, which carries
EXECUTE on `raposa_api.request_investigation_v1`), writes
`/etc/adhunters/spy-numbers.env` and `spy-web.env`, and starts both units.
Without the Raposa grants the ad page
leaves Raposa out. Spy is marked ready in `shared/frame/assets/core.js`,
so other apps' menus link to it.

## What it reads and publishes

Reads Tracks only through `tracks_api` (the login needs `tracks_api_read`):
`ad_v1`, `creative_v1`, `account_v1`, `brand_v1`, `publisher_v1`,
`device_v1`, `link_v1`, `sighting_v1`, `ad_hourly_v1`,
`ad_account_brand_hourly_v1`, `scrape_coverage_v2`, `closed_hour_v1`,
`ad_daily_v1`, `ad_account_daily_v1`, `creative_link_daily_v1`,
`creative_campaign_daily_v1`, `auction_v1`, `walk_page_v1`,
`page_version_v1`, and (spy-web) `network_v1`, `campaign_v1` and
`hourly_days_v1`. `scrape_coverage_v2` adds the open hours' checks, which
Direction needs beside their sightings. From Raposa it reads
`raposa_api.evidence_v1` (page titles for the classifier, when the login
may) and, in spy-web, `investigation_v1`.

Publishes `spy_api` (granted to `spy_api_read`), with a copy of each
definition in [contract/sql/spy](../contract/sql/spy):

- Stored: `creative_stats_v1`, `operator_stats_v1`, `publisher_stats_v1`,
  `creative_recent_v1`, `operator_recent_v1`, `recent_window_v1`,
  `size_v1`, `direction_v1`, `direction_event_v1`, `operator_v1`,
  `account_operator_v1`, `creative_vertical_v1`, `creative_class_v1`,
  `price_day_v1`.
- Any range: `range_info_v1`, `creative_range_v1`, `operator_range_v1`,
  `vertical_range_v1`, `publisher_range_v1`, `creative_prices_v1`,
  `creative_series_v1`.

Settings are rows of `spy.setting`, read on every run: changing one needs
no deploy.

## Working here

Tests need a Postgres the test may create databases on:

```
PG_TEST_URL=postgres://postgres:test@localhost:5432/postgres?sslmode=disable go test ./...
```

`internal/testdb` stands in for the Tracks views as plain tables.
`numbers/checks_test.go` runs the handbook's checks on simulated counts
(about a minute; `-short` skips them). `numbers/volume_test.go` times every
job at Tracks' volume and runs only with `SPY_VOLUME=1`.

Every file in `contract/sql/spy` must appear word for word in a migration
(`migrations_test.go` checks). A published view or function that changes
gets a new version: a new contract file and a new migration.

### Not built yet

- Showing sites, clues and the proposed grouping in the app, and screens
  to edit groupings (`grouping_fix`) and correct a vertical.
- Shared certificates and name servers as clues (the collector had them
  from its own lookups; Tracks' walker does not look them up).
- Watches and alerts from the pages (Direction's events are ready for them).
- Direction's fading guard for one site (ranges have it; Direction sums its
  usual publishers).
- The nightly checks METRICS.md asks for (stopped accuracy, predictive
  value) once Tracks has weeks of history.
