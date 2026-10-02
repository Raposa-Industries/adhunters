# AdHunters Create

Images and headlines for our ads, made with OpenAI from a brief, and saved
into the library that Launch makes ads from. Was auto-creative (images
only), which stays as it is. Two binaries live here:

- `create`: Create itself, rebuilt on auto-creative's steps (below,
  [decision 0015](../decisions/0015-create-briefs.md)).
- `create-web`: the team's sign-in, in front of every app on hunt-teste.fyi
  (below). It was the campaign launcher page until 2026-10-01.

## Create (`create`)

Pages under `/create/` on the shared shell (the Frame), in Portuguese;
headlines are always in English. Tabs: **Criar**, **Biblioteca**, **Regras**.
Create is a chat, as auto-creative was, with iteration added (decision 0019):

1. **A session.** Pick the vertical (our fixed list, `shared/verticals`) and
   name the session in the left column, or open one already there. The
   vertical and the name are the library folders what it saves goes in
   (`<vertical>/<session>`); a name used before in that vertical opens that
   session again. Click the name to rename it; once saved, its library
   folder (and the Drive folder) takes the new name too.
2. **Send.** Write what you want, set how many pictures (up to 8) and
   headlines (up to 20), and press Enviar (Ctrl+Enter). One text call writes
   the headlines and, for several pictures from words alone, one brief per
   picture so they differ; then each picture is its own call, three at a
   time. A picture that fails fails alone.
3. **Pick and iterate.** Click any picture or headline to pick it (the
   number is the order picked; a prompt can say "the first"). Send again:
   picked pictures go to the picture model and are changed as the prompt
   says, or varied when the prompt is empty; picked headlines are varied,
   and picked pictures are what new headlines are written for. Every new
   item remembers what it came from. Repeat as long as you like.
4. **Bring your own.** + Imagem do computador, + Da biblioteca (pictures and
   headlines of the session's vertical) and + Escrever headline add items
   that can be picked like any other.
   **From Spy** (decision 0022): Spy's "Criar variações" links to
   `/create/?from=spy&creative=<id>` (`tracks_api.creative_v1.id`). Create
   opens a session in the ad's vertical (from `spy_api.creative_class_v1`;
   the person picks one when Spy has none), named "Spy <id> · <brand>", with
   the ad's picture and newest headline added (origin `spy`) and picked. The
   same ad again opens the same session. The picture is downloaded by
   Create's server from public addresses only.
5. **Warnings.** Every headline shows Taboola's warnings (hidden characters,
   length, capitals, cure claims, disease names, amounts, the team's blocked
   words with a swap button); they never block. A headline is edited in
   place until it is saved.
6. **Save.** With items picked, Salvar na biblioteca writes them into the
   session's folder, with the person's AI label for made pictures (on by
   default, with a warning when switched off). Nothing reaches the library
   before that. The head then links Abrir no Launch (`/launch/new?set=<id>`).

The **Biblioteca** tab browses the library (pictures, headlines, folders)
through Create's server. **Regras** lists the rules and the team's blocked
words.

### How it works

Rows are in `create_app` (`create` is a reserved word in SQL): sessions,
turns, items, saves (`session_save`) and the worker's queue (`work`). Every
send and save goes through the same `create_api` functions Desk calls and
queues work; a worker in the same binary runs it, `CREATE_WORKERS` at a
time, and the page asks again every 2 seconds while something runs. At a
start, a picture left running by a stop is failed (it may have been paid
for), anything else runs again. Every OpenAI reply is kept on disk as it
came (`CREATE_KEEP_DIR`) before it is read; uploads and pictures are kept in
`CREATE_FILES`. A session's first save makes its set in the library and
remembers the id at once, so every later save adds to the same folder.

The brief pages of decision 0015 are gone; their tables (`brief`,
`reference`, `option`, `save`, `job`, `event`) and rows stay, untouched, and
`/create/new` and `/create/briefs/{id}` land on the chat.

### For Desk and other apps: `create_api`

`contract/sql/create/`: views `session_v1`, `turn_v1` (state making, done
or failed), `item_v1` (a picture's `image_url` is its path on Create's
address) and `session_save_v1` (`library_set_id` once saved), and three
functions, the same the page calls:

- `create_api.new_session_v1(p_name, p_vertical_id, p_vertical_name, p_requested_by, p_origin) RETURNS BIGINT`.
- `create_api.send_turn_v1(p_session_id, p_prompt, p_picked BIGINT[], p_images, p_headlines, p_requested_by, p_origin) RETURNS BIGINT`:
  picked items must be the session's and done.
- `create_api.save_items_v1(p_session_id, p_item_ids BIGINT[], p_ai_label, p_requested_by, p_origin) RETURNS BIGINT`.

The same `p_origin` again returns the same row. The brief views and
functions were dropped by migration 0005 (decision 0021); their tables and
rows stay. Callers log in with a role granted `create_api_read`.

### Create's API (for its pages)

Under `/create/api/`, JSON, errors `{"error": "<pt-BR line>"}`. The person
is Cloudflare Access's `Cf-Access-Authenticated-User-Email`; a change sent
from another site's page is refused.

- `GET status`, `GET rules`, `GET verticals` (the fixed list by category).
- `GET sessions?vertical=&limit=`, `POST sessions` (`{"name", "vertical_id"}`),
  `GET sessions/{id}` (the session, turns, items with warnings, saves),
  `PATCH sessions/{id}` (`{"name"}`; renames the library set too, and a
  name the library refuses puts the old one back).
- `GET spy/{creative}` (the ad, its vertical's name and the session name it
  would get), `POST spy/{creative}/session` (`{"vertical_id", "name"}`, both
  optional when Spy knows the vertical): `{"session", "picked", "warning"}`;
  the page opens `/create/s/<id>?pick=<ids>`.
- `POST sessions/{id}/turns` (`{"prompt", "picked", "images", "headlines"}`).
- `POST sessions/{id}/items`: a multipart `file`, or `{"headline"}`,
  `{"library_creative": id}`, `{"library_headline": id, "headline"}`.
  `PATCH items/{id}` (`{"text"}`, a headline not yet saved).
- `POST sessions/{id}/saves` (`{"item_ids", "ai_label"}`), `GET saves/{id}`.
- `GET /create/files/items/{id}`: a picture. `GET /create/library-api/…`:
  the library's reads.

### Settings

`deploy/create.env.example`: `DATABASE_URL` (the `create_app` login; it
runs the migrations on start), `OPENAI_API_KEY` (empty: the pages work and
making is off), `CREATE_ADDR` (`127.0.0.1:8095`), `OPS_ADDR`
(`127.0.0.1:9112`), `CREATE_FILES`, `CREATE_KEEP_DIR`, `LIBRARY_URL`
(`http://127.0.0.1:8093`), `CREATE_WORKERS` (3), and the same model and
price settings as create-web (below).

### Run it

```
DATABASE_URL=postgres://… CREATE_FILES=file:///tmp/create/files CREATE_KEEP_DIR=/tmp/create/kept \
  OPENAI_API_KEY=… go run ./create/cmd/create
open http://127.0.0.1:8095/create/
```

## The campaign launcher (`create-web`, retired)

Retired on 2026-10-01 for Launch; see "Run it". What follows describes the
old page.

One page, in Portuguese, for the team that puts ads on Taboola by hand. The
ads themselves (headlines, descriptions, CTAs) are always in English. It is
drawn in AdHunters Ember (dark, one orange accent, Archivo, IBM Plex Sans and
Mono; the fonts are served from `web/launcher/fonts/`), and works on a phone:
the steps become a strip under the header. Eleven steps, in the order the
team works. Each step opens once the steps it needs are done (a dimmed step
says what is missing), and the button at the bottom goes to the next thing to
do; Taboola's rules only warn and never hold a step. What holds the new
campaign comes with buttons that set the values that would clear it (the
daily cap over the total, a missing country, a bid over the ceiling), and so
does a refusal from Taboola the page can read: when Taboola names a minimum
daily cap, the page offers it and remembers it for that account:

1. **Vertical.** Required: it picks the team's headlines the model learns from.
2. **References** (optional). Ads that are performing (their pictures) and
   headlines of the person's own. When given they weigh the most: about two
   in three image ideas are close variations of the reference pictures, and
   the headlines follow the person's before the team library.
3. **Generate.** Optional extra instructions (for the images, the headlines or
   both), the age of the people in the pictures, how many images and
   headlines. Create asks OpenAI for headlines and one idea per image, then
   makes each image, three at a time. While it runs, a panel shows what is
   happening now with a clock, and every step and image with its own state,
   time and cost. Nothing made is used until a person ticks it.
4. **Images** and 5. **Headlines.** Tick the options worth using, add your own
   images or headlines, edit any headline in place.
6. **CTAs.** One or more of Taboola's buttons (Read More, Learn More, ...).
7. **Combination** (GLOSSARY.md), over images, headlines and CTAs:
   - *Sortido* (mixed): the longest list once each, the shorter ones repeated
     in turn. 10 images, 5 headlines and 2 CTAs make 10 ads.
     Reshuffle picks another order.
   - *Um a um* (one to one): a grid where each tick is one ad.
   - *Todas as combinações* (every combination).

   The grid of images by headlines shows the pairs in every mode. Changing a
   cell outside *Um a um* switches to it, starting from the pairs shown.
8. **Brand and description.** The brand is the campaign's branding text.
9. **AI disclosure.** The person's answer, with a warning when AI-made
   content is marked "no".
10. **Campaign.** Pick one or more of the account's campaigns loaded from
    Taboola, or set up a new one: start from scratch or copy an existing one
    (without its ads); objective; campaign group (automatic, an existing one,
    or a new group with its own budget); countries and devices; bid, daily
    cap and total budget; start and end dates. Then **tracking and landing
    page**: pick the tracker (RedTrack, Voluum, other, none) and paste its
    link; the page splits it into the address that goes in every ad and the
    parameters with `{macros}`, which go in the campaign's tracking code
    (Taboola escapes macros inside an ad's link).
11. **Review and CREATE.** Every ad with its warnings, then **CRIAR NO
    TABOOLA**: the group, the campaign, the image uploads and the ads, in
    that order. The same ads cannot be sent twice by accident. The bulk sheet
    (below) stays as the fallback, under "Baixar para o Bulk Upload".

**Everything is created paused.** Campaign groups, campaigns and ads are made
paused; they run only when a person turns them on in Taboola's own dashboard.
Taboola's mass create answers `is_active: true` even when asked for false
(seen 2026-09-29), so every ad is paused again right after it is made, and
the result says which ones could not be.

Without Taboola keys the campaign step says Taboola is not connected and the
bulk sheet is the way out.

### The bulk sheet (fallback)

**The sheet is always Realize's own template.** Realize rejects a workbook
that is not in its template's shape (a home-made one got "Invalid number of
library creatives"): the template carries hidden METADATA tabs naming the
account and the field behind each column. The page carries that template
with no account in it (`shared/adsweb/assets/realize-base.xlsx`, the US one, made from a
downloaded template by `shared/adsweb/tools/realize-base.mjs`, which empties the
Accounts, Conversion Events, audience and Campaign Groups lists and blanks
every string only they used). When it builds a sheet it writes the
account's part back from the API (`GET /api/taboola/workbook`: the network
account as METADATA's accountName, the accounts, the group names), so
nobody picks a template. A template picked by hand still wins and is kept in
the browser, for a login where Realize refuses the built-in one. Realize's
upload only creates (rows with no id) or updates (rows with an id); the page
writes no ids, so a sheet can only add ads. The page writes one row
per ad into its Ads tab from row 3, finding columns by their header in row
2 and leaving every other file in the workbook as Realize wrote it
(`shared/adsweb/assets/template.js`). "Campaign ID" takes several ids as `123; 456`.
Status is Paused on every ad. The Ads tab runs to row 500, so
one sheet holds 498 ads. "Copiar linhas" copies the same rows to paste at A3
by hand.

Nothing is sent to RedTrack from here.

**The team's own prompts are built in** (their material, 2026-09-29, is in
`internal/openai/rules/` and the project files at research/create-prompts/):

- *Performing ads* are analysed first, the team's "Etapa 1": for seven
  aspects (subject, gesture, product, setting, framing, emotion, photo style)
  what is fixed and what can vary. The page shows it as a table. These ads
  go to the text model only, never to the image model.
- *Image ideas* follow the team's "Etapa 2": about half close variations of
  that pattern and half new angles (spoon, straw, shot, bottle, blender,
  just before or after taking it, the reaction after), always people of the
  age asked for, a candid everyday scene, nobody looking at the camera, no
  text or brands. Each idea carries its angle, and the options are grouped
  by it. Where the team's rules and Taboola's guide disagree (Taboola
  suggests eye contact and plain backgrounds) the team's win.
- *Headlines* learn from the team's headlines for the vertical (Blood
  Pressure, Memory Loss, Neuropathy, Tinnitus, Weight Loss): a random 40 per
  plan, or 10 when the person gave headlines of their own, which come first
  and weigh the most.
- *Blocked words*: the team's list of words Taboola has blocked for them
  (`rules/blocked.txt`). The model is told never to use them, and the page
  warns when a headline, the extra instructions, the reference headlines or
  the description (for the words blocked there too) has one. Under each
  warning are other words to put in its place (`rules/synonyms.txt`), one
  tap each; the person may keep theirs. The team's own examples use many of these words; the model
  keeps their structure and says it another way.

**Warnings, never blocks.** Headlines and images are checked against
Taboola's rules (research/taboola-policies/digest.md in the project files):
hidden characters, over 60 characters, shouting, "cure", disease names,
amounts, emoji, the team's blocked words, image size and shape, `{macros}`
in the link. Each shows as a
warning; the person decides. The AI label is their choice too: download
waits only until they answer it, with a warning when AI-made content is
marked "no".

**Ad id.** Each ad gets `ah-<10 hex of the image's SHA-256>-<10 hex of the
headline's>` in Taboola's Custom ID, so results can be joined back to the ad
later (Intel).

### What is not confirmed yet

- The first upload of a filled template: the template layout is Realize's
  (a real one, 2026-09-29, is in the project files at
  research/taboola-bulk-upload/realize-template-2026-09-29.xlsx), but no
  filled one has gone through Realize yet. Cells are written as inline
  text, which Excel reads the same as its own shared strings.
- The bulk sheet can only add ads to an *existing* campaign; CREATE makes
  the campaign too.
- CREATE was tried for real on 2026-09-29, paused, on the lent ZoltaGroup
  test account (research/taboola-api/create-web-paused-test-2026-09-29/ in
  the project files): a new group, a campaign in it, a copy of a campaign
  with a new name, brand, bid and cap, one image and two ads. A copy stays
  in its original's campaign group, so the page does not offer a group when
  copying. Not tried yet: start and end dates, SMART bidding, several
  campaigns in one send.
- Title over 60 characters is Taboola's guideline; the template says under 100.

## API (for the page)

Errors are `{"error": "<a line in Portuguese>"}`: 400 bad input, 402 out of
OpenAI credit, 503 no key, 502 any other OpenAI failure. Only same-origin
requests are accepted.

- `GET /api/status`: whether making options is on, the models, the price of
  one image, the team's blocked words and the verticals with team headlines.
- `POST /api/plan` (JSON: prompt, headline_examples, language, vertical,
  ages, headlines 0-30, images 0-12, has_references, avoid, and 0-6
  `winners` as `data:` URLs of performing ads): an analysis of the ads (empty
  without them), headlines, and one `{angle, brief}` per image grouped by
  angle, cleaned of hidden characters and duplicates. The ads themselves are
  not kept, only how many there were.
- `POST /api/image` (multipart: brief, optional quality, 0-6 `reference`
  JPEG or PNG files): one picture, base64. With references it goes to
  OpenAI's edits endpoint. At most 4 pictures are made at once.

Taboola (without keys, status says `{"connected": false, "reason"}` and the
rest answer 503; 400 for anything the server refuses before calling Taboola,
502 when Taboola refuses, 504 when it does not answer in time):

- `GET /api/taboola/status`: the accounts, the bid and daily cap ceilings,
  and whether this is a lent account (only-own, with its name prefix).
- `GET /api/taboola/campaigns?account=` and `GET /api/taboola/groups?account=`.
- `POST /api/taboola/groups` (JSON: account, name, spending_limit,
  spending_limit_model MONTHLY or ENTIRE, marketing_objective): a paused group.
- `POST /api/taboola/campaigns` (JSON: account, name, brand, cpc, daily_cap,
  spending_limit, countries, platforms, tracking_code, marketing_objective,
  bid_strategy FIXED or SMART, start_date, end_date, group_id, or copy_from
  instead of group_id): a paused campaign.
- `POST /api/taboola/ads` (multipart: account, `campaigns` JSON, `ads` JSON of
  `{image, title, description, cta, url, custom_id, ai}`, and the `image`
  files): uploads each image once, creates the ads in every campaign, then
  pauses them. Answers per campaign what was made and any error.

Every reply is saved raw under `CREATE_KEEP_DIR/<UTC date>/` before it is
read, with a `.json` beside each picture saying what was asked and what it
cost. Every Taboola request and answer is kept there too. Spend is counted in `/metrics` (`ops.Spent`).

## Settings

`create-web` reads the environment (`create/deploy/create-web.env.example`,
the full list in `cmd/create-web/main.go`):

| Name | Default | |
|---|---|---|
| `OPENAI_API_KEY` | unset | Unset: the page works for uploads and typed headlines; making options is off. |
| `CREATE_WEB_ADDR` | `127.0.0.1:8091` | The page and `/api/`. |
| `OPS_ADDR` | `127.0.0.1:9109` | `/healthz`, `/metrics`. |
| `CREATE_KEEP_DIR` | `create-kept` | Every OpenAI reply, as received, before it is read. |
| `CREATE_IMAGE_MODEL`, `CREATE_IMAGE_QUALITY` | `gpt-image-2.5-flare`, `medium` | |
| `CREATE_TEXT_MODEL`, `CREATE_TEXT_REASONING` | `gpt-5-mini`, `low` | |
| `CREATE_*_PRICE_IN/OUT` | see main.go | USD per million tokens, for the spend shown on the page and `ops.Spent`. |
| `OPENAI_BASE_URL` | the real API | A local fake, to try the page without spending. |
| `TABOOLA_CLIENT_ID`, `TABOOLA_CLIENT_SECRET` | unset | One Taboola login's Backstage API keys. Unset: Taboola is off and the bulk sheet is the way out. |
| `TABOOLA_ACCOUNTS` | unset | That login's advertiser accounts the page may use, comma separated. A `-network` account is refused at boot. |
| `TABOOLA_MAX_CPC`, `TABOOLA_MAX_DAILY_CAP`, `TABOOLA_MAX_SPEND_LIMIT` | `1.00`, `20`, `20` | Ceilings for a new campaign, in USD. Every campaign made or copied gets a total limit of at most `TABOOLA_MAX_SPEND_LIMIT` (the owner's rule, 2026-10-01: never above $20). |
| `TABOOLA_ONLY_OWN` | off | A lent account: only groups and campaigns this server made (recorded in the state file) are listed or touched. |
| `TABOOLA_NAME_PREFIX` | unset | Optional, with only-own: every group and campaign name must start with it. |
| `TABOOLA_STATE_FILE` | `<keep dir>/taboola-state.json` | What this server made, for only-own. |
| `TABOOLA_BASE_URL` | the real API | A local fake. |

OpenAI is the only generator (the clients require it for images).

The only Taboola keys we hold today are the lent ZoltaGroup login's. The
owner allowed paused tests there (2026-09-29), so on that login create-web
runs with `TABOOLA_ONLY_OWN=1`, and whatever it made (the ids in the state
file) is deleted after the test. Names are free: the owner dropped the test
prefix on 2026-09-29.

## The sign-in (`create-web`)

Every request to hunt-teste.fyi goes through `create-web`. It replaced
Cloudflare Access on 2026-10-01 (owner's word) with our own sign-in:

- One username and password for the whole team (`SIGNIN_USER`,
  `SIGNIN_PASSWORD_HASH` in `/etc/adhunters/create-web.env`). Only a
  PBKDF2-SHA256 hash is kept, printed by `create-web hash-password` (it reads
  the password on stdin). It refuses to start without both.
- A page at `/_signin` (Portuguese). A good password sets a signed cookie
  (`ah_session`, HttpOnly, Secure, SameSite=Lax) that lasts 30 days and is
  renewed past half of that. `/_signin/out` signs out. A new password signs
  everyone out, since the cookie's key comes from the hash.
- Without a valid cookie, a page opened in the browser goes to the sign-in
  and back after it; anything else (the apps' API calls) gets 401.
- Ten wrong passwords from one address (Cloudflare's `Cf-Connecting-Ip`)
  within 15 minutes block that address for the rest of the 15 minutes.
- Signed in, each request goes to the app whose path it starts with
  (`web.Apps`: `/launch`, `/create`, `/intel`, `/spy`, `/funnels`, `/desk`,
  `/raposa`) as it is, streams included, with the session cookie taken off;
  every other path goes to `/launch/`. The apps listen only on localhost,
  except raposa-web, which listens on the worker's private address
  (`10.20.1.10:8090`), and none does sign-in of its own.

Landing sites (funnels-edge) have their own hostnames and no sign-in.

## Run it

```
printf 'a long password' | go run ./create/cmd/create-web hash-password
SIGNIN_USER=team SIGNIN_PASSWORD_HASH=pbkdf2-sha256:… go run ./create/cmd/create-web
open http://127.0.0.1:8091/
```

The old launcher's code above stays in `web/launcher/` until it is deleted.

On the data box it is the `create-web` unit, installed by
`platform/servers/setup.sh` and reached at https://hunt-teste.fyi through a
Cloudflare Tunnel whose only hunt-teste.fyi rule points at it
(platform/OPERATIONS.md). Install
cloudflared once, on the data box:

```
curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg | sudo tee /usr/share/keyrings/cloudflare-main.gpg >/dev/null
echo "deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main" | sudo tee /etc/apt/sources.list.d/cloudflared.list
sudo apt-get update && sudo apt-get install -y cloudflared
sudo cloudflared service install TOKEN
```

## Code shared with Launch

The page's ad code (pairing, checks, the bulk sheet, the tracker link split,
the ZIP) is `shared/adsweb`, served at `/_ads/`, and its Taboola client is
`shared/taboola/write`: Launch (`launch/`) uses both and takes over every
Taboola write; this page keeps working until Launch replaces it.

## Tests

```
node --test shared/adsweb/test/*.test.js  # pairing, checks, tracking, zip, xlsx (needs python3)
cd create && go test ./...
```

## Next (release order)

1. This page: from a vertical to ads on Taboola (bulk sheet as the fallback).
2. Whole campaigns: presets for the campaign step, drafts saved on the server.
3. Create proper: saved briefs, image variations, the angle list.
4. Results per ad: Intel reads RedTrack by Custom ID and shows which ads win.
5. Straight to Taboola through the Backstage API: built (CREATE); waiting
   on keys for the team's own logins.
6. Desk: ask for all of this in a conversation.
