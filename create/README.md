# AdHunters Create

Images and headlines for our ads. Was auto-creative (images only), which
stays as it is; this folder is the new Create.

## What runs today: the bulk upload page (`create-web`)

One page, in Portuguese, for the team that puts ads on Taboola by hand. It
takes the typing out of Taboola's Bulk Upload:

1. **Brief.** What the ad is about, optional reference images and headlines,
   the headline language, the vertical, how many images and headlines to make.
   Create asks OpenAI for headlines and a one-line idea per image, then makes
   each image (with the references when there are any). Everything made is an
   option: nothing is used until a person ticks it.
2. **Creatives and headlines.** Tick the options worth using, upload your own
   images, paste your own headlines, edit any headline in place.
3. **Pairing** (GLOSSARY.md):
   - *Sortido* (mixed): the longer list once each, the shorter repeated in turn.
     10 images and 5 headlines make 10 ads, each headline twice; 10 and 8
     make 10, two headlines twice. Reshuffle picks another order.
   - *Um a um* (one to one): a grid where each tick is one ad.
   - *Todas as combinações* (every combination).
4. **Campaign.** The Taboola campaign ids the ads go into (each ad goes into
   all of them), the landing page link, CTA, and whether the ads are marked
   as AI-made (the template's "AI Content" column).
5. **Download** the bulk sheet and a `.zip` of the images named as in "Image
   File Name", then upload both in Realize › Create › Bulk Upload.

**The sheet is always Realize's own template.** Realize rejects a workbook
that is not in its template's shape (a home-made one got "Invalid number of
library creatives"): the template carries hidden METADATA tabs naming the
account and the field behind each column. So the person picks the template
once (Create › Bulk Upload › Download Template, in the account that owns the
campaigns; the page keeps it in the browser), and the page writes one row
per ad into its Ads tab from row 3, finding columns by their header in row
2 and leaving every other file in the workbook as Realize wrote it
(`launcher/template.js`). "Campaign ID" takes several ids as `123; 456`.
Status stays empty: new ads start Active. The Ads tab runs to row 500, so
one sheet holds 498 ads. "Copiar linhas" copies the same rows to paste at A3
by hand.

Nothing is sent to Taboola or RedTrack from here.

**Warnings, never blocks.** Headlines and images are checked against
Taboola's rules (research/taboola-policies/digest.md in the project files):
hidden characters, over 60 characters, shouting, "cure", disease names,
amounts, emoji, image size and shape, `{macros}` in the link. Each shows as a
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
- An ad can only join an *existing* campaign, so a new campaign is made (or
  duplicated) first, in Realize.
- Title over 60 characters is Taboola's guideline; the template says under 100.

## API (for the page)

Errors are `{"error": "<a line in Portuguese>"}`: 400 bad input, 402 out of
OpenAI credit, 503 no key, 502 any other OpenAI failure. Only same-origin
requests are accepted.

- `GET /api/status`: whether making options is on, the models, the price of one image.
- `POST /api/plan` (JSON: prompt, headline_examples, language, vertical,
  headlines 0-30, images 0-12, has_references, avoid): headlines and one
  brief per image, cleaned of hidden characters and duplicates.
- `POST /api/image` (multipart: brief, optional quality, 0-6 `reference`
  JPEG or PNG files): one picture, base64. With references it goes to
  OpenAI's edits endpoint. At most 4 pictures are made at once.

Every reply is saved raw under `CREATE_KEEP_DIR/<UTC date>/` before it is
read, with a `.json` beside each picture saying what was asked and what it
cost. Spend is counted in `/metrics` (`ops.Spent`).

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

OpenAI is the only generator (the clients require it for images).

## Run it

On your computer, from the repository root:

```
OPENAI_API_KEY=sk-... go run ./create/cmd/create-web
```

Then open http://127.0.0.1:8091.

On the data box it is the `create-web` unit, installed by
`platform/servers/setup.sh` and reached at https://hunt-teste.fyi through a
Cloudflare Tunnel behind Cloudflare Access (platform/OPERATIONS.md). Install
cloudflared once, on the data box:

```
curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg | sudo tee /usr/share/keyrings/cloudflare-main.gpg >/dev/null
echo "deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main" | sudo tee /etc/apt/sources.list.d/cloudflared.list
sudo apt-get update && sudo apt-get install -y cloudflared
sudo cloudflared service install TOKEN
```

## Tests

```
node --test create/web/test/*.test.js     # pairing, checks, zip, xlsx (needs python3)
cd create && go test ./...
```

## Next (release order)

1. This page: from a brief to Taboola's bulk sheet.
2. Whole campaigns: presets for the campaign tab, drafts saved on the server.
3. Create proper: saved briefs, image variations, the angle list.
4. Results per ad: Intel reads RedTrack by Custom ID and shows which ads win.
5. Straight to Taboola through the Backstage API, once the team has keys.
6. Desk: ask for all of this in a conversation.
