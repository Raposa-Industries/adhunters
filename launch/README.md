# AdHunters Launch

Puts ads on the networks, and is the one app that changes anything there
(AGENTS.md: Launch owns every write; Intel and Desk only read and suggest:
their one-tap links open Launch with the change filled in; Desk's requests
are taken and kept, but have no screen since Pedidos was removed, see
Pages). Taboola today; NewsBreak
later as a second adapter under `internal/network/`. The pages are in
Portuguese, headlines always in English. It sits in the Frame
(`shared/frame`) and uses the ad code it shares with create-web
(`shared/adsweb`).

**New campaigns go up running.** Since the owner's word of 2026-10-02
(`TABOOLA_CREATE_ACTIVE=1`), the groups, campaigns and ads a person makes
here go up running and spend once Taboola approves them; the button says
**Criar e ligar**. Without it they are made paused and run only when a
person turns them on in Taboola's dashboard. Copies and moves always arrive
paused, and nothing here turns an existing campaign on.
Taboola's rules only warn; the person decides. The AI label is the person's
choice, with a warning when pictures that look AI-made are marked "no".

## Pages

All under `/launch/`; a link to any of them opens it. The tabs are
Campanhas · Contas (Draw Designer IMPLEMENT d587e1b829, "only Campanhas and
Contas left"). The Pedidos, Pedido, Histórico, Presets and Rascunhos
screens were removed then; their API calls and every row they showed are
kept (see "Removed screens").

| Path | Shows |
|---|---|
| `/launch/campaigns` (also `/launch/`) | **Campanhas**: one table, like Realize's, with each group, its campaigns and their ads as rows that open in place (**Abrir tudo** opens every group, and every campaign's ads when there are 25 campaigns or fewer; **Fechar tudo**). Each row has its state, budget and the period's numbers; on top, the totals (Linhas or Grupos, Gasto, Cliques, CTR, Vendas, CPA, Receita, Lucro, ROI), and **Colunas** picks the number columns (kept per browser). Search, account (**Todas as contas** or one), **Grupo de campanha** and **Campanha** (tick some to see only those, the campaigns following the ticked groups; each list shows a few and "+N" for the rest), state, device and period are on the left. A campaign whose group was deleted sits under "Grupo apagado · id", and one with no group under "Sem grupo". Tick campaigns to **pause**, change budget and bid, **copy**, **move** them to another group or add ads (a pair's other half comes along unless unticked), or tick ads to pause them. Clicking a campaign opens it on the right without leaving the table: its account and group, state, pair, **Pausar** (with "com o par"), **Mudar**, **Adicionar anúncios**, **Duplicar**, **Mudar de grupo**, its numbers, and tabs for its settings, its ads and its History; Esc closes it and ↑ ↓ go to the next campaign. Numbers are Intel's (`intel_api.campaign_result_v1`, `ad_result_v1`), never asked of Taboola by Launch; without Intel they show "—". **+ Novo** makes a group, a campaign or ads (N then G, C or A), starting from the account and group shown. |
| the address | `?account=<id or all>&group=<id>&open=<campaign id>&w=<today, yesterday, 7d, 30d>`, so a link or a reload shows the same table with the same campaign open. |
| `/launch/groups`, `/launch/ads` | The tables before batch 126: they open Campanhas with the same address. |
| `/launch/taboola/<account>[/g/<group>]` | Old addresses: open Campanhas narrowed to that account and group. `g/-` is campaigns without a group. |
| `/launch/taboola/<account>/g/<group>/c/<campaign>` | One campaign: Campanhas with that campaign open on the right. |
| `…/c/<campaign>?do=…&from=intel:<id>` | The same, with a suggested change filled in on top: `do=pause-ads&ads=<ids,…>`, `pause-campaign`, `set-daily-cap&cap=<usd>`, `set-bid&cpc=<usd>`. Only this campaign, not its pair. Nothing is sent until the person presses the button; History records `from` (only `intel:…` or `desk:…`, like `desk:step:12`) as who asked. |
| `/launch/new?make=…` | The steps behind **+ Novo**, like Realize's "+ New": one item at a time, each on its own short page (Draw Designer IMPLEMENT 43e7b65f44). On the left the steps (each with what it holds so far; a click goes back to one). In the middle one step at a time, "Passo N · …" in one card, then **Voltar**, **Cancelar** and **Próximo: …**, which checks the step first. On the right **Prévia na tabela** (on Novos anúncios' first step, the library instead): the rows Campanhas will show, the group with the campaigns already in it and the new ones marked "novo", each with its bid and budgets, the new ads, and the first ads as cards. Groups, campaigns and ads go up running or paused as the server says (`TABOOLA_CREATE_ACTIVE`), and the preview says which. |
| `make=campaign` (the default; `&account=…&group=…` picks the group) | **Nova campanha** (IMPLEMENT d587e1b829: only what a person changes in Taboola, the rest Taboola's defaults or the team's below): 1 · Campanha: **Grupo**, a tree of each account (opens and closes; its id and how many groups) and its groups ("3 campanhas · Rodando"), with "+ Grupo novo GRP<nn>" in each (made with the campaigns: objective Online Purchases, budget per campaign, no end date); **Dispositivo**, three cards: Mobile (phones and tablets), Desktop (computers) or Os dois (one campaign of each, a pair); **Começa** Hoje (no start date sent: Taboola starts it today) or Amanhã (tomorrow's date), never an end date; **Orçamento diário** per campaign (US$ 500 by default; no total of its own, the server's ceiling still applies); **Países** (chips: the United States by default, also Canada, United Kingdom, Australia, New Zealand, Ireland; the excluded cities go only with the United States); **Marca**; **Nomes**, one chip per campaign with the team's name, which follows the group (GRP01-CMP03-Desk-pp-bl): a click lets one be typed; **Mais configurações** (closed, showing the tracking code): presets, tracking code, bid (Maximize conversions by default, CPC fixo or CPC Smart), the excluded US cities and ad delivery. 2 · Revisar e criar: the campaigns go up without ads; each step of the send shows as it happens, the same send twice is one send, and **Adicionar anúncios** then opens Novos anúncios with the new campaigns picked. Without a network connected nothing can be made here. |
| `make=group` | **Novo grupo**: 1 · Grupo: **Conta**, one radio row per account (name, id, how many groups); **Nome**, filled with the account's next number (GRP03); "Os nomes descem assim" shows the group's, its first campaign's and first ad's names; **Mais configurações** (closed): presets, objective and budget (per campaign by default, or the group's per month or in total). Every group runs for ever, with no end date. 2 · Revisar e **Criar grupo**, or **Criar e adicionar campanha**, which goes straight on to a campaign in it. |
| `make=ads` (`&to=<ids>` picks campaigns) | **Novos anúncios**: 1 · Anúncios: **Campanhas** as chips (**+ Escolher** opens every account's groups and campaigns, with search; a group's box picks all its campaigns) and **Página de destino** (required); **Combinações**, a matrix: pictures are columns A, B, C… (each with × to take it out; **+ img** uploads one from the computer), headlines rows (each with an optional description, which goes on its ads; **+ headline da biblioteca**, or one typed in English); each ticked cell is one ad, AD01, AD02… in reading order, made in every chosen campaign; a click on a row's headline or a column's letter ticks the whole row or column (or unticks it); "8 marcadas × 2 campanhas = 16 anúncios"; **Botão**, one for all these ads: Saiba mais (Learn More), Leia mais (Read More), Compre agora (Shop Now), Ver oferta (Get Offer), Inscreva-se (Sign Up), Sem botão, or **Mais** with the rest of Taboola's buttons; and the names the ads get. On the right, **Biblioteca do Create**: search, a folder (a library set) and the folder's pictures and headlines; a click on a picture makes it a column (it then shows its letter) and on a headline a row (it then shows a check), a second click takes it out; under a picture, "no Launch: N anúncios", the ads Launch made with it (`launch.item`); headlines over 60 letters stay out. 2 · Revisar e adicionar: one row per ad (its picture's letter, headline, description, button and names), **Tirar** unticks it; the AI label (Sim or Não, the person's choice, warned when pictures look AI-made); repeated ads and headline warnings only warn. The same ads go into each campaign picked, through its own account (Realize's "assign creatives"). Without a network connected, **Subir à mão** gives Realize's bulk sheet and the ZIP. |
| `/launch/new?set=<id>` | Novos anúncios with one library set open in the library and its pictures and headlines already in the matrix as columns and rows, nothing ticked yet (Create links here after saving a set). |
| `/launch/accounts` | **Contas**: one row per Taboola account Launch uses (**Conta**, **ID** in mono with how its requests reach Taboola under it: "proxy host:porta", or "direto" for an account of the server's own login without one, **No Launch** "2 grupos · 5 campanhas" or "nenhum grupo" from each account's tree, **Adicionada** dd/mm, "—" for the server's own), then "N contas". **+ Novo** opens **Nova conta**: **Client ID**, **User ID**, **Client secret** (a password field) and **Proxy** (`http://usuario:senha@host:porta`, http, https or socks5), all required. **Conectar** asks Taboola through that proxy for a token and the login's account list (reads only: nothing is made or changed on Taboola or RedTrack); a green box says "Conectou: N contas vão entrar" with their names, and **Adicionar N contas** adds every advertiser account the login sees (the network account never; ones already in Launch are struck through and not counted). They then appear in the Conta picker, ⌘K, Novo grupo and every table, and every call about one goes to its own login through its proxy, with the same guards and ceilings. Each row's **···** menu: for an added login's account, **Proxy** (change the login's proxy, for all its accounts; checked through the new one first; it cannot be emptied), **Detalhes do acesso** (client ID's first and last four characters, user ID, which can be filled in or changed, proxy, accounts), **Escolher as contas deste acesso**, **Tirar esta conta do Launch** (its login's last account removes the login) and **Remover o acesso**; for an account of the server's own login, only **Proxy** (set, change or take away; without one it goes direct). The secret and the proxy are sealed before they are saved and no page, answer or log line shows them again. An account two logins share belongs to the first (the server's own first). |
| `/launch/requests`, `/launch/requests/<id>`, `/launch/history`, `/launch/presets`, `/launch/drafts` | Removed screens: they open Campanhas, like `/launch/groups` and `/launch/ads`. |

⌘K finds any group or campaign by name or id in every connected account.

### Removed screens

IMPLEMENT d587e1b829 removed Pedidos, Pedido, Histórico, Presets and
Rascunhos (`web/pages/route.js`, `GONE`). Nothing stored was dropped and the
API is unchanged, but:

- **Desk's requests** (`launch_api.new_request_v1`, the `launch.*` asks in
  `contract/actions/launch.json`) are still taken and kept, and nobody can
  confirm or refuse them: they wait. Desk's links to them
  (`/launch/requests/<id>`, the asks' `link`) open Campanhas.
- **History** shows only in a campaign's Histórico tab, not all at once.
- **Presets** are still saved and used in the new-item steps (**Salvar como
  preset**, **Usar um preset…**), but none can be changed or deleted.
- **Drafts** already saved open only by address (`/launch/new?draft=<id>`).

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
- **Budget:** US$ 500 a day and no total (lifetime) limit, the owner's
  word of 2026-10-02 (it was $20 and $20 since 2026-10-01). The Taboola
  client enforces the ceilings whatever the page sends
  (`TABOOLA_MAX_DAILY_CAP` and `TABOOLA_MAX_SPEND_LIMIT`, 20 when unset;
  the box sets 500 and 0). With a spend limit above 0, a new campaign
  without a total gets it, one above is refused, a copy is brought down to
  it, and no change raises it. With 0, a campaign gets no total unless one
  is typed, and a typed total is at most 30 daily caps.
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
- **Names** (the team's names, IMPLEMENT d587e1b829): groups GRP<nn>,
  counted per account; campaigns GRP<nn>-CMP<nn>-<Desk|Mobile>-pp-bl,
  counted per group; ads GRP<nn>-CMP<nn>-AD<nn>-<Desk|Mobile>-pp-bl, in the
  matrix's order. A group named otherwise lends its own name
  (`Memory Loss US-CMP01-Desk-pp-bl`). The names from before (groups 01,
  02…; campaigns CMP<n>-<account number>-<Desktop|Mobile>-pp-bl) still count
  when numbering. Nothing outside Launch reads these names (checked
  2026-10-02: Intel, Desk, Spy, Tracks, Raposa and contract only copy a
  campaign's name). Taboola's items have no name field, so an ad's name
  is only Launch's: in History ("Adicionou 8 anúncios
  (GRP01-CMP01-AD01-Desk-pp-bl a AD08)"), in the steps and in the bulk
  sheet's Ad Name.
- **Landing page:** any address; Funnels is not wired in.

## How the writes work

- **Never turned on.** No Launch action, Intel `?do=` link or Desk request
  can start an existing campaign or ad (the kinds are pause, pause ads,
  change, copy and move), and the Taboola client refuses any body with
  `is_active` other than false before it is sent. The one exception is a new
  campaign, group or ad a person makes here with `TABOOLA_CREATE_ACTIVE=1`
  (the owner, 2026-10-02: a person pressing create is the go).
- **Paused, checked.** Every copy Taboola makes, and every new campaign
  without `TABOOLA_CREATE_ACTIVE`, must come back with `is_active` false;
  one that comes back running, or without saying, is paused at once and
  that pause is checked. A copy above the
  ceilings (its source's daily cap or fixed CPC) is brought down to them.
- **New campaign.** The group (when new; without a group budget it is
  `spending_limit_model` NONE, accepted on the paused test), then the
  desktop campaign (Taboola `DESK`) and/or the mobile one (`PHON` and
  `TBLT`, phones and tablets, as the team's mobile campaigns are; since
  2026-10-02, before it was `PHON` only), each with all the ads. Both
  devices make a pair; the mobile one has the desktop's settings unless the
  request's `mobile` gives its own (the page sends only CPC or target CPA,
  daily budget and start date there). The Taboola client checks each
  campaign against the ceilings on its own.
- **Groups run for ever.** Backstage's campaign group has an `end_date`
  whose default, `9999-12-31`, means no end (API reference §3,
  CampaignGroup; `start_date` defaults to today). Launch never sends either,
  so every group it makes has no end date and runs until someone pauses it;
  the pages say "para sempre" on the group form, the preview and group
  presets. Sending `9999-12-31` explicitly needs a change to
  `shared/taboola/write` that is not made yet. A picture is uploaded to Taboola once however many ads and
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
  campaigns leaves them there, still running or spending if they were on
  (2026-10-01: campaign 50547320 still listed and readable, its group
  answering 404, Realize saying "Campaign Group Was Deleted", the API's
  `campaigns/base` saying `CAMPAIGN_GROUP_TERMINATED`). Launch offers no
  delete; if it ever does, a group with campaigns is refused. Such a
  campaign shows as "Grupo apagado", under a stand-in group of that name
  with no actions, so it can still be found, paused or changed.
- **Pause, change.** Bid, daily cap, total limit, name (one campaign at a
  time). Some ads of one campaign can be paused too (Intel's `pause-ads`).
  Turning on is never here.
- **Requests.** Desk calls `launch_api.new_request_v1` (below); the
  request waits until a person confirms or refuses it. It is decided once:
  a second press gets "já foi decidido". Since Pedidos was removed
  (IMPLEMENT d587e1b829) no page confirms or refuses them: they are kept
  and wait, and Desk's links to `/launch/requests/<id>` open Campanhas.
- **Proxies.** Every request to Taboola for an account of a login added on
  Contas goes through that login's proxy, and for an account of the
  server's own login through its proxy when it has one (else direct): the
  token, reads, writes and image uploads alike, each account with its own
  client (`internal/logins`, `shared/taboola/write` `Settings.HTTP`). The
  proxy is fixed in the transport, so the environment's proxy settings never
  apply. When it fails the request fails with a pt-BR line naming its
  host:port ("nada foi enviado direto à Taboola"); it never goes direct
  instead. An added login without a usable proxy (one added before proxies,
  or a key that changed) has every request refused until one is set on
  Contas.
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
| `internal/store`, `migrations` | The `launch` schema: presets, pairs, History, moves, drafts, the ads made (`item`), requests, logins and proxies; and `launch_api`. Reads Intel's numbers from `intel_api` (the `launch` login is granted `intel_api_read`). |
| `internal/library` | Reads the library (`library/`) on localhost. |
| `internal/images` | Pictures kept by hash. |
| `web` | The pages (`web/pages`), plain ES modules on the Frame; `newpair.js` and `newpair.css` are the + Novo steps, `matrix.js` the matrix and the team's names. |

## API

Under `/launch/api/`. Errors are `{"error": "<pt-BR line>"}`: 400 refused
before the network, 404 not here, 409 name taken, 502 the network refused
or failed, 503 not connected.

| Call | Does |
|---|---|
| `GET status` | The person, each network (connected, why not), the ceilings. |
| `GET search?q=` | Groups and campaigns for ⌘K. |
| `GET accounts/{net}` | Every login's chosen accounts. |
| `GET {net}/{account}/tree` | Groups (with their objective), campaigns, pairs, waiting moves. The lists are kept 30 s and dropped by any write through Launch. |
| `GET {net}/{account}/ads?campaigns=1,2` | `{ads: {campaign: [ad]}, errors: {campaign: why}}`, at most 25 campaigns, kept like the tree. |
| `GET numbers?window=7d&accounts=a,b` | Intel's numbers for the window by campaign and by ad: `{available, campaigns, ads, refreshed_at}`; `available` is false without Intel's views. |
| `GET {net}/{account}/campaigns/{id}` | Campaign, ads, pair, twin, History. |
| `POST {net}/{account}/groups` | A group `{name, budget, budget_model, objective}`, running or paused like campaigns (`TABOOLA_CREATE_ACTIVE`). |
| `POST {net}/{account}/move` `duplicate` `pause` `change` | `{campaigns, to_group, originals, change}`; one result per campaign. |
| `POST moves/{id}/cancel` | Stops waiting on a move. |
| `POST pairs` then `GET jobs/{id}` | A new campaign or pair (`actions.PairRequest` plus `key`; `devices` both, mobile or desktop; no `name` for the team's names; `desktop_name` or `mobile_name` names that campaign only; `ads` exactly as made, one entry per ad, or none (Nova campanha sends none: the campaigns go up empty); `desktop` or `mobile`, whole settings that replace `settings` for that device only); the job lists each step. |
| `GET/POST presets`, `PUT/DELETE presets/{id}` | Presets (the new-item steps use and save them; no screen lists them since IMPLEMENT d587e1b829). |
| `GET history?network=&account=&campaign=&limit=` | History and waiting moves (a campaign's Histórico tab). |
| `GET/POST drafts`, `GET/PUT/DELETE drafts/{id}` | Drafts (kept; no screen lists them since IMPLEMENT d587e1b829). |
| `POST {net}/{account}/campaigns/{id}/pause-ads` | `{ads}`: pauses those ads of one campaign. |
| `GET {net}/{account}/next[?group=<id>]` | The names the next group and campaign get: `{group, group_id, prefix, campaign, desktop, mobile}`; `group` is the account's next new group, the rest are for a campaign in `?group=` (400 when the account has no such group) or, without it, in that new group. |
| `POST {net}/{account}/add-ads` | `{campaigns, new_ads}`: the same ads (each with its own title, description and button) in each campaign, paused or running like campaigns (`TABOOLA_CREATE_ACTIVE`); History names them AD01… in that campaign. |
| `GET used?creatives=<fp>,…` | How many ads Launch made with each picture, by its fingerprint (the first 10 hex characters of its SHA-256, as in our ad id): `{used: {fp: n}}`. |
| `GET library/status` `verticals` `sets` `creatives` `headlines` | The library's lists, passed on (filters: `vertical`, `set`, `angle`, `origin`, `ai_label`, `q`, `limit`, `before`). |
| `GET library/set?id=`, `GET library/thumb?id=` | One set with its creatives and headlines; a creative's thumbnail. |
| `POST library/use?id=` | Copies a library creative's picture into Launch's pictures: `{image, creative}`. |
| `GET requests`, `GET requests/{id}` | Requests (kept; no screen shows them since IMPLEMENT d587e1b829). |
| `POST requests/{id}/confirm`, `POST requests/{id}/refuse` | Decides one, as the signed-in person. |
| `POST images` (multipart `image`), `GET images/{sha}` | Pictures. |

| `GET logins` | The logins for Contas: `{id, server, network, name, client_id` (first and last four characters only)`, user_id, proxy` (host:port)`, accounts: [{id, name, proxy, problem}], added_by, added_at, problem}`; an account's `proxy` "" is direct. Each login's accounts are asked of Taboola through their proxy. Never a secret or a whole proxy. |
| `POST logins/check` | `{client_id, client_secret, proxy}`: asks Taboola through the proxy which accounts the login sees (a token and allowed-accounts, reads only): `{accounts: [{id, name, network}]}`. |
| `POST logins` | `{name, client_id, user_id, client_secret, proxy, accounts}`: checks again through the proxy, seals the secret and the proxy, saves, and starts using the accounts. 400 when the proxy is not a valid http, https or socks5 address, Taboola refuses the login, an account is not the login's or is its network account, or the login is already there. |
| `GET logins/{id}/allowed`, `PUT logins/{id}`, `DELETE logins/{id}` | What an added login sees now (through its proxy); `{name, accounts}` to rename it and choose again; stop using it (nothing changes on Taboola). |
| `PUT logins/{id}/proxy` | `{proxy}`: an added login's new proxy, checked through it first; never empty. |
| `PUT logins/{id}/user-id` | `{user_id}`: an added login's Taboola user ID. Nothing is asked of Taboola. |
| `PUT accounts/{net}/{account}/proxy` | `{proxy}`: the proxy of an account of the server's own login, checked through it first; `""` takes it away (direct again). |

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

Accounts are not in `launch_api`: they are the network logins', read live
(`GET accounts/{net}`).

## Logins added on Contas

`launch.login` (`migrations/sql/0003_login.sql`) holds each added login: name,
client ID, the chosen accounts, who added it and when, and its secret sealed
with AES-256-GCM (`internal/logins`), tied to its network and client ID. The
key is 32 bytes, never in the database, so a database copy alone cannot
open a secret. Launch reads it from `LAUNCH_LOGIN_KEY_BASE64` (the key in
base64, in `/etc/adhunters/launch-web.env`); without that setting it uses the
file `LAUNCH_LOGIN_KEY`, by default `login.key` in the data folder
(`/var/lib/launch-web/login.key` on the data box), which it makes at its
first start, owner-only. With the setting it neither reads nor makes the file;
if a file holds another key, the log says so and the setting wins.

Nothing backs up `/var/lib`, so the owner keeps the key in their password
manager, like every other key (decision 0026). To move the key file into the
setting and copy the key to the clipboard without showing it, run this from
your computer, then paste the key into the password manager. No restart is
needed: the key is the same.

```
ssh admin@adhunters-data sudo bash -s <<'EOF' | wl-copy
set -e
f=/etc/adhunters/launch-web.env k=/var/lib/launch-web/login.key
v=$(base64 -w0 "$k")
sed -i '/^LAUNCH_LOGIN_KEY_BASE64=/d' "$f"
[ -z "$(tail -c1 "$f")" ] || echo >>"$f"
echo "LAUNCH_LOGIN_KEY_BASE64=$v" >>"$f"
if sed -n 's/^LAUNCH_LOGIN_KEY_BASE64=//p' "$f" | base64 -d | cmp -s - "$k"; then echo "== saved in $f, the same key as $k; it is in your clipboard" >&2; else echo "== the setting does not match $k" >&2; exit 1; fi
printf %s "$v"
EOF
```

On a rebuilt box, put the line `LAUNCH_LOGIN_KEY_BASE64=` with the key from
the password manager back in `launch-web.env` and restart launch-web. If the
key is lost, the added logins show "a chave do servidor mudou" and have to be
added again, and accounts with a proxy refuse every request until it is set
again; the server's own login's accounts without a proxy are not affected.

Since IMPLEMENT d587e1b829 (`migrations/sql/0004_proxy.sql`, add-only) each
added login also holds its proxy, sealed with the same key and tied to the
login apart from its secret, and its Taboola **user ID** (`user_id`, plain;
'' for logins added before). No Taboola call needs the user ID: the token
(`client_credentials`) takes only the client ID and secret, and every other
call the token, so Launch only keeps and shows it. The server's own login's
accounts may each have a proxy in `launch.account_proxy`, sealed the same
way and tied to the account. Logins added before proxies have none, so
their accounts are refused until a person sets one on Contas (**···** >
**Proxy**). Nothing new is needed in `launch-web.env`. Intel does not read these
logins yet: their accounts have no numbers in Launch until it does.

## Settings and running it

`deploy/launch-web.env.example` lists them; `deploy/launch-web.service` is
the unit. `LAUNCH_LIBRARY_URL` is the library's API (default
`http://127.0.0.1:8093`; `off` for none). `platform/servers/setup.sh data`
installs it on the data box with its login and the `launch_api_read` role
(platform/OPERATIONS.md, "Launch's page"). https://hunt-teste.fyi/ opens it
(create-web sends every request on to `/launch/`); the old launcher page
is retired. Since 2026-10-01 the ZoltaGroup login is the team's live one
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
node --test launch/web/test/*.test.js   # from the repo root: the matrix, the team's names, the mobile's own settings, routes, Contas' rows
```

The API tests run every write against the fake network on a throwaway
database; the adapter tests run against a fake Backstage.

## Not yet

- Adding pictures to the library from Launch (they are only Launch's now).
- The library's Originais/Geradas filter: the library does not tell an
  original picture from a generated one (its origin is create, upload or
  drive; the AI label is the person's), so the panel shows all.
- The team's blocked-words list (Create's today) in the headline warnings.
- Desk asking for new ads (it can link `/launch/new?set=<id>`).
- Who may confirm a raise of bid or caps (any signed-in person today).
- NewsBreak.
