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
| `/launch/` | The accounts; with only one, its tree. |
| `/launch/taboola/<account>` | The tree: every group with its campaigns (a pair's two together, marked "par"). Filters on the left: account, search, state, device, groups. Tick campaigns (a pair's other half comes along unless unticked) to **move** them to another group, **copy**, **pause**, or change bid and caps. Moves waiting on their copy are listed on top. **Novo grupo** makes a paused group. |
| `/launch/taboola/<account>/g/<group>` | One group's campaigns. `g/-` is campaigns without a group. |
| `/launch/taboola/<account>/g/<group>/c/<campaign>` | One campaign: settings, ads with their review state, the pair's other half, its History, and the same actions (rename here only). |
| `…/c/<campaign>?do=…&from=intel:<id>` | The same page with a suggested change filled in on top: `do=pause-ads&ads=<ids,…>`, `pause-campaign`, `set-daily-cap&cap=<usd>`, `set-bid&cpc=<usd>`. Only this campaign, not its pair. Nothing is sent until the person presses the button; History records `from` (only `intel:…` or `desk:…`) as who asked. |
| `/launch/new` | **Novo par**: where (account, name, an existing group or a new one from a group preset), settings (from a campaign preset or typed; save them as a preset), ads (pictures uploaded or taken from the library by set, English headlines, buttons, Sortido or every combination, AI label; a library creative's AI label is the one saved with it), then review and **Criar o par pausado**. Each step of the send shows as it happens and the page may be left; the same send twice is one send. Without a network connected, or by choice, **Subir à mão** gives Realize's bulk sheet and the ZIP. |
| `/launch/presets` | Group and campaign presets: make, edit, delete; how many pairs used each. |
| `/launch/history` | History, filtered by kind or text. |
| `/launch/new?set=<id>` | Novo par with one library set's creatives and headlines already in (Create links here after saving a set). |
| `/launch/drafts` | Drafts; opening one continues it in Novo par. |
| `/launch/requests` | **Pedidos**: changes Desk asked for, waiting ones first. |
| `/launch/requests/<id>` | One request: what it asks, the campaigns as they are now, **Confirmar e enviar** or **Recusar**. Confirming makes the change as the person, with the request's origin as who asked. |

⌘K finds any group or campaign by name or id in every connected account.

## How the writes work

- **New pair.** The group (when new), then the desktop campaign (Taboola
  `DESK`) and the mobile one (`PHON`; tablets are not targeted), each with
  all the ads. A picture is uploaded to Taboola once however many ads and
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
| `internal/store`, `migrations` | The `launch` schema: presets, pairs, History, moves, drafts, the ads made (`item`), requests; and `launch_api`. |
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
| `GET {net}/{account}/tree` | Groups, campaigns, pairs, waiting moves. |
| `GET {net}/{account}/campaigns/{id}` | Campaign, ads, pair, twin, History. |
| `POST {net}/{account}/groups` | A paused group `{name, budget, budget_model, objective}`. |
| `POST {net}/{account}/move` `duplicate` `pause` `change` | `{campaigns, to_group, originals, change}`; one result per campaign. |
| `POST moves/{id}/cancel` | Stops waiting on a move. |
| `POST pairs` then `GET jobs/{id}` | A new pair (`actions.PairRequest` plus `key`); the job lists each step. |
| `GET/POST presets`, `PUT/DELETE presets/{id}` | Presets. |
| `GET history?network=&account=&campaign=&limit=` | History and waiting moves. |
| `GET/POST drafts`, `GET/PUT/DELETE drafts/{id}` | Drafts. |
| `POST {net}/{account}/campaigns/{id}/pause-ads` | `{ads}`: pauses those ads of one campaign. |
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
| `campaign_move_v1` | Moves: `old_campaign_id, new_campaign_id` (numbers), `account, moved_at`, the text ids, target group, state. |
| `item_v1` | Every ad Launch made (new pairs and copies): network, account, campaign and item ids (text and number), our `ad_id`. |
| `pair_v1`, `preset_v1`, `change_v1` | Pairs, presets, History. |

Accounts are not in `launch_api`: they are the network login's, read live
(`GET accounts/{net}`).

## Settings and running it

`deploy/launch-web.env.example` lists them; `deploy/launch-web.service` is
the unit. `LAUNCH_LIBRARY_URL` is the library's API (default
`http://127.0.0.1:8093`; `off` for none). Not in `platform/servers/setup.sh` yet
(it also needs the `launch_api_read` role made before the first start, like
`raposa_api_read`), and not deployed: the
prototype (create-web at hunt-teste.fyi) keeps working until Launch
replaces it. The lent ZoltaGroup login is never set on both at once.

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
