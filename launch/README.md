# AdHunters Launch

Puts ads on the networks, and is the one app that changes anything there
(AGENTS.md: Launch owns every write; Intel and Desk only read and suggest:
their one-tap links open Launch with the change filled in, and Desk's
requests wait in Launch for a person to confirm). Taboola today; NewsBreak
later as a second adapter under `internal/network/`. The pages are in
Portuguese, headlines always in English. It sits in the Frame
(`shared/frame`) and uses the ad code it shares with create-web
(`shared/adsweb`).

**Everything is made paused.** Groups, campaigns and ads are created
paused and run only when a person turns them on in Taboola's own dashboard.
Taboola's rules only warn; the person decides. The AI label is the person's
choice, with a warning when pictures that look AI-made are marked "no".

## Pages

All under `/launch/`; a link to any of them opens it.

| Path | Shows |
|---|---|
| `/launch/groups` | **Grupos de campanha**, like Realize's: a table of groups with their campaigns, budget and the period's numbers (summed from their campaigns). Clicking a group opens Campanhas narrowed to it. |
| `/launch/campaigns` (also `/launch/`) | **Campanhas**: a table with each campaign's group, device, state, bid, daily budget and numbers. Clicking a campaign opens Anúncios narrowed to it. Tick campaigns (a pair's other half comes along unless unticked) to see or add ads, **pause**, change budget and bid, **copy**, or **move** them to another group. Moves waiting on their copy are listed on top. |
| `/launch/ads` | **Anúncios**: each ad with its picture, campaign, state, review, Intel's word and numbers; tick ads to pause them. Without a group or campaign it reads the 25 campaigns that spent most and says so. |
| the three tables | Share the address `?account=<id or all>&group=<id>&campaign=<id>&w=<today, yesterday, 7d, 30d>`, so a link or a reload shows the same table. At the top, like Realize, a breadcrumb of pickers (Conta, Grupo de campanha, Campanha) with search: picking a group opens its campaigns, a campaign its ads, and "Todos" takes that narrowing off. The period sits by the title; search, state and device are on the left. A group or campaign the table is narrowed to is the title and a chip; its × (or **Ver todas**) goes back to all, and the top tabs always open the whole level. Columns sort by a click; totals are on top and in the last row; 50 rows a page; each row has its actions on the right (**+ Campanha**, **Editar**, **+ Anúncios**, the ad's page). Numbers are Intel's (`intel_api.campaign_result_v1`, `ad_result_v1`), never asked of Taboola by Launch; without Intel they show "—". **Novo ▾** on every table makes a group, a campaign or ads, starting from the group or campaign the table is narrowed to. |
| `/launch/taboola/<account>[/g/<group>]` | Old addresses: open Campanhas narrowed to that account and group. `g/-` is campaigns without a group. |
| `/launch/taboola/<account>/g/<group>/c/<campaign>` | One campaign: settings, ads with their review state, the pair's other half, its History, and the same actions (rename here only). |
| `…/c/<campaign>?do=…&from=intel:<id>` | The same page with a suggested change filled in on top: `do=pause-ads&ads=<ids,…>`, `pause-campaign`, `set-daily-cap&cap=<usd>`, `set-bid&cpc=<usd>`. Only this campaign, not its pair. Nothing is sent until the person presses the button; History records `from` (only `intel:…` or `desk:…`, like `desk:step:12`) as who asked. |
| `/launch/new?make=…` | The steps behind **Novo ▾**, like Realize's "+ New": the steps on the left, one at a time (**Próximo** checks the step first; the step list jumps back), and on the right a preview of everything that will be made: account, group, each campaign with its name and settings, and the first ads as cards. Everything is made paused. |
| `make=campaign` (the default; `&group=` picks the group) | **Nova campanha**: 1 · Grupo (account; an existing group or a new one, named with the account's next number, 01, 02…, from a group preset or typed: objective, budget per campaign (default) or the group's per month or in total). 2 · Campanha (devices: both, which makes a desktop and a mobile campaign alike, mobile or desktop; named CMP<n>-<account number>-<Mobile or Desktop>-pp-bl unless a name is typed; settings from a campaign preset or the team's defaults below). 3 · Anúncios (pictures uploaded or from the library, English headlines, buttons, Sortido or every combination, AI label). 4 · Revisar e **Criar pausado**. Each step of the send shows as it happens; the same send twice is one send. Without a network connected, **Subir à mão** gives Realize's bulk sheet and the ZIP. |
| `make=group` | **Novo grupo de campanha**: **Criar grupo**, or **Criar e adicionar campanha**, which goes straight on to a campaign in it. |
| `make=ads` (`&to=<ids>` picks campaigns) | **Novos anúncios**: pick campaigns of one account, then the same ads, paused, go into each (Realize's "assign creatives"). |
| `/launch/new?set=<id>` | Nova campanha with one library set's creatives and headlines already in (Create links here after saving a set). |
| `/launch/drafts` | Drafts; opening one continues it in Nova campanha. |
| `/launch/requests` | **Pedidos**: changes Desk asked for, waiting ones first. |
| `/launch/requests/<id>` | One request: what it asks, the campaigns as they are now, **Confirmar e enviar** or **Recusar**. Confirming makes the change as the person, with the request's origin as who asked. |

⌘K finds any group or campaign by name or id in every connected account.

## The team's defaults

A new campaign starts from these (2026-09-30), and every one can be changed
or saved in a preset:

- **Where:** United States, minus Atlanta GA, Bend OR, Biloxi MS, Idaho
  Falls ID, Indianapolis IN, New York NY, Rochester NY, Shreveport LA and
  Tallahassee FL, sent as their ids in Taboola's US city dictionary (3,
  2132, 2038, 995, 140, 57, 125, 189, 847; read 2026-09-30, see
  research/taboola-api/lookups-2026-09-30 in the project files). The
  `city_targeting` EXCLUDE shape (string ids) was accepted and kept on the
  paused test of 2026-09-30 (research/taboola-api/launch-test-2026-09-30).
- **Budget:** US$ 500 a day, no spending limit. The ceiling
  (`TABOOLA_MAX_DAILY_CAP`) is 500 in the example settings, and 500 when
  the line is missing.
- **Bid:** Maximize conversions (CPA), a target CPA optional; Taboola sets
  each bid (`bid_strategy` MAX_CONVERSIONS, no `cpc`, kept as
  OPTIMIZED_CONVERSIONS on the paused test; TARGET_CPA with `target_cpa` is
  not tried yet). It needs a conversion objective, so the objective starts
  at Online Purchases;
  Maximize conversions with Website traffic or Brand awareness is refused.
- **Ad delivery:** prioritize top-performing ads (`traffic_allocation_mode`
  OPTIMIZED).
- **Tracking code:** `src={site}&utm_medium={thumbnail}&…&ref_id={click_id}`
  (`web/pages/presets.js`, `TEAM`).
- **Names:** groups 01, 02… and campaigns CMP<n>-<account>-<device>-pp-bl,
  each counted per account from the highest already there. The account's
  number is the first number in its id (zoltagroup-1-sc is 1), or its place
  in the login's accounts.
- **Landing page:** any address; Funnels is not wired in.

## How the writes work

- **Never turned on.** The owner's rule (2026-10-01): no campaign or ad runs
  without their go. No Launch action, Intel `?do=` link or Desk request can
  start one (the kinds are pause, pause ads, change, copy and move), and the
  Taboola client refuses any body with `is_active` other than false before
  it is sent.
- **Paused, checked.** Every campaign Taboola makes (new or a copy) must
  come back with `is_active` false; one that comes back running, or without
  saying, is paused at once and that pause is checked. A copy above the
  ceilings (its source's daily cap or fixed CPC) is brought down to them.
- **New campaign.** The group (when new; without a group budget it is
  `spending_limit_model` NONE, accepted on the paused test), then the
  desktop campaign (Taboola `DESK`) and/or the mobile one (`PHON`; tablets
  are not targeted), each with all the ads. Both devices make a pair. A picture is uploaded to Taboola once however many ads and
  campaigns use it. When one campaign fails the other is still made; the
  result says what exists. The pair is recorded (`launch.pair`) with the
  preset it came from.
- **Move.** Taboola fixes a campaign's group when it is made (proven
  2026-09-30), so a move is a copy into the new group, with a new id, and the
  ads copied along in the same call (new ids, paused, back through review;
  proven 2026-09-30, any ad that comes back running is paused). The original is paused
  when a person starts the copy (checked every `LAUNCH_WATCH_EVERY`), or at
  once, or never, as chosen. Waiting moves can be cancelled.
- **Copy.** The same, in the same group, named "(cópia)". Copying both
  halves of a pair makes a pair.
- **No group delete.** Deleting a group in Taboola that still holds
  campaigns leaves them there but out of the API's reach (404; seen
  2026-09-30). Launch offers no delete; if it ever does, a group with
  campaigns is refused.
- **Pause, change.** Bid, daily cap, total limit, name (one campaign at a
  time). Some ads of one campaign can be paused too (Intel's `pause-ads`).
  Turning on is never here.
- **Requests.** Desk calls `launch_api.new_request_v1` (below); the
  request waits until a person confirms or refuses it on its page. It is
  decided once: a second press gets "já foi decidido".
- Every change goes to History with who did it, who asked (`?from=` on the
  API: `intel:…`, `desk:…`), before and after, and the result.

The signed-in person is Cloudflare Access's
`Cf-Access-Authenticated-User-Email` header. Pictures people bring are kept
by their SHA-256 under `<data>/images/`, and every Taboola request and answer
under `<data>/kept/<UTC day>/` before it is read (`kit/keep`).

## Code

| Folder | Holds |
|---|---|
| `cmd/launch-web` | The binary: pages, API, Frame and ad code; the move watcher; `/healthz` and `/metrics` on `OPS_ADDR`; stops on SIGTERM. |
| `internal/network` | What Launch asks of a network, in words for every network, and `fake` (in memory, for tests and the demo). |
| `internal/network/taboola` | The Taboola adapter over `shared/taboola/write`, which holds the guards (allowed accounts, never a network account, ceilings, only-own on a lent login). |
| `internal/actions` | Every write, its checks and its History. |
| `internal/api` | `/launch/api/`, JSON for the pages. |
| `internal/store`, `migrations` | The `launch` schema: presets, pairs, History, moves, drafts, the ads made (`item`), requests; and `launch_api`. Reads Intel's numbers from `intel_api` (the `launch` login is granted `intel_api_read`). |
| `internal/library` | Reads the library (`library/`) on localhost. |
| `internal/images` | Pictures kept by hash. |
| `web` | The pages (`web/pages`), plain ES modules on the Frame. |

## API

Under `/launch/api/`. Errors are `{"error": "<pt-BR line>"}`: 400 refused
before the network, 404 not here, 409 name taken, 502 the network refused
or failed, 503 not connected.

| Call | Does |
|---|---|
| `GET status` | The person, each network (connected, why not), the ceilings. |
| `GET search?q=` | Groups and campaigns for ⌘K. |
| `GET accounts/{net}` | The login's accounts. |
| `GET {net}/{account}/tree` | Groups, campaigns, pairs, waiting moves. The lists are kept 30 s and dropped by any write through Launch. |
| `GET {net}/{account}/ads?campaigns=1,2` | `{ads: {campaign: [ad]}, errors: {campaign: why}}`, at most 25 campaigns, kept like the tree. |
| `GET numbers?window=7d&accounts=a,b` | Intel's numbers for the window by campaign and by ad: `{available, campaigns, ads, refreshed_at}`; `available` is false without Intel's views. |
| `GET {net}/{account}/campaigns/{id}` | Campaign, ads, pair, twin, History. |
| `POST {net}/{account}/groups` | A paused group `{name, budget, budget_model, objective}`. |
| `POST {net}/{account}/move` `duplicate` `pause` `change` | `{campaigns, to_group, originals, change}`; one result per campaign. |
| `POST moves/{id}/cancel` | Stops waiting on a move. |
| `POST pairs` then `GET jobs/{id}` | A new campaign or pair (`actions.PairRequest` plus `key`; `devices` both, mobile or desktop; no `name` for the team's names); the job lists each step. |
| `GET/POST presets`, `PUT/DELETE presets/{id}` | Presets. |
| `GET history?network=&account=&campaign=&limit=` | History and waiting moves. |
| `GET/POST drafts`, `GET/PUT/DELETE drafts/{id}` | Drafts. |
| `POST {net}/{account}/campaigns/{id}/pause-ads` | `{ads}`: pauses those ads of one campaign. |
| `GET {net}/{account}/next` | The names the next group and campaign get: `{group, campaign, account_number, desktop, mobile}`. |
| `POST {net}/{account}/add-ads` | `{campaigns, new_ads}`: the same ads, paused, in each campaign. |
| `GET library/status` `verticals` `sets` `creatives` `headlines` | The library's lists, passed on (filters: `vertical`, `set`, `angle`, `origin`, `ai_label`, `q`, `limit`, `before`). |
| `GET library/set?id=`, `GET library/thumb?id=` | One set with its creatives and headlines; a creative's thumbnail. |
| `POST library/use?id=` | Copies a library creative's picture into Launch's pictures: `{image, creative}`. |
| `GET requests`, `GET requests/{id}` | Requests. |
| `POST requests/{id}/confirm`, `POST requests/{id}/refuse` | Decides one, as the signed-in person. |
| `POST images` (multipart `image`), `GET images/{sha}` | Pictures. |

Every write takes `?from=intel:<id>` or `?from=desk:<id>` for History's
"who asked"; anything else there is ignored.

## launch_api

What other services may read, and Desk's one call
(`migrations/sql/0002_launch_api.sql`, each statement word for word in
`contract/sql/launch/`). Readers get it through the `launch_api_read` role.

| Name | Holds |
|---|---|
| `request_v1` | Requests: `id, kind, input, requested_by, origin, state` (`waiting`, `confirmed`, `sent`, `refused`, `failed`), `confirmed_by, result, made_at, decided_at`. |
| `new_request_v1(p_kind, p_input, p_requested_by, p_origin)` | Asks for a change, returns the request id. `p_kind`: `pause`, `pause_ads`, `change`, `duplicate`, `move`. `p_input`: `{network, account, campaigns: [ids], ads, change: {cpc, daily_cap, spending_limit, name}, to_group, originals}`. The same `p_origin` (`desk:42`) returns the same request. |
| `campaign_move_v1` | Finished moves: `old_campaign_id, new_campaign_id` (numbers), `account, moved_at` (when it finished), the text ids, target group. |
| `item_v1` | Every ad Launch made (new pairs and copies): network, account, campaign and item ids (text and number), our `ad_id`. |
| `pair_v1`, `preset_v1`, `change_v1` | Pairs, presets, History. |

Accounts are not in `launch_api`: they are the network login's, read live
(`GET accounts/{net}`).

## Settings and running it

`deploy/launch-web.env.example` lists them; `deploy/launch-web.service` is
the unit. `LAUNCH_LIBRARY_URL` is the library's API (default
`http://127.0.0.1:8093`; `off` for none). `platform/servers/setup.sh data`
installs it on the data box with its login and the `launch_api_read` role
(platform/OPERATIONS.md, "Launch's page"). Not deployed yet: the
prototype (create-web at hunt-teste.fyi) keeps working until Launch
replaces it. Since 2026-10-01 the ZoltaGroup login is the team's live one
(owner) and both apps hold it; Launch runs without `TABOOLA_ONLY_OWN` so the
team can manage the campaigns already there.

See the pages with a fake Taboola (a few groups, campaigns and a pair), a
fake library (two sets) and two Desk requests, on a throwaway database:

```
cd launch
PG_TEST_URL=postgres://… LAUNCH_DEMO=127.0.0.1:8094 go test ./cmd/launch-web -run TestDemo -timeout 0
```

then open http://127.0.0.1:8094/launch/.

## Tests

```
cd launch && PG_TEST_URL=postgres://… go test -race ./...
```

The API tests run every write against the fake network on a throwaway
database; the adapter tests run against a fake Backstage.

## Not yet

- Adding pictures to the library from Launch (they are only Launch's now).
- The team's blocked-words list (Create's today) in the headline warnings.
- Desk asking for a new pair (it can link `/launch/new?set=<id>`).
- Who may confirm a raise of bid or caps (any signed-in person today).
- NewsBreak.
