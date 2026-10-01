# library

Every creative and headline the team keeps, shared by Create and Launch
([decision 0014](../decisions/0014-library.md)). Create saves what people
choose here; Launch makes ads from it. Every file lives in the team's Google
Drive folder, where the team can see it and add files by hand
([decision 0020](../decisions/0020-library-on-drive-only.md)). The apps never
show that it is Drive.

It runs on the data box as one binary, `library`: an HTTP API on localhost
for the apps, and the Drive sync. It owns the `library` and `library_api`
schemas.

## What it keeps

- **Creatives.** One per picture, found by its bytes: saving the same bytes
  again returns the same creative. Saved from an app with a vertical, a
  creative gets a name minted from the vertical's code, the network letter
  and a counter that only goes up: `BP` + `T` + `43` = `BPT43`, as
  auto-creative did. A creative that came from Drive keeps its file's name.
  Each has an angle, the idea it was made from, where it came from
  (`create`, `upload`, `drive`), who saved it, and the AI label (the
  person's answer: `unset`, `ai`, `not_ai`).
- **Headlines.** One per text, cleaned of hidden characters
  (`shared/text`), found by the text. A headline's text never changes: a new
  text is a new headline, because the ad id is made from it.
- **Sets.** Creatives and headlines made or uploaded together: one session in
  Create, one folder in Drive. A creative or headline can be in several sets.
- **Files, in Drive only.** A picture saved from an app waits in its row
  until the next Drive pass uploads it (seconds later), and then only Drive
  has its bytes; `/files/{id}` reads them back from there. Each creative's
  480px JPEG thumbnail stays in its row. A file deleted in Drive is gone:
  its row, name and thumbnail stay, `/files/{id}` answers 410, and saving the
  same bytes again brings it back.

Nothing is deleted. Hiding a creative or headline takes it out of the lists;
anything that points at it still finds it.

The verticals are our fixed list's ids (shared/verticals/verticals.yaml). The team's nine
must-haves start with codes (Blood Pressure `BP`, Memory Loss `MM`, Weight
Loss `WL`, Tinnitus `TN`, Diabetes `DB`, Neuropathy `NP`, Prostate Health
`PR`, Joint Pain `JP`, Vision `VS`); another vertical gets a code from its
initials the first time something is saved in it. A code and the counter can
be changed (`PATCH /api/verticals/{id}`); names already minted keep theirs,
and the counter never goes down. auto-creative's own counters were not
copied: set `next_number` past the team's last file before the first save
if the old names should not repeat.

## Google Drive

The team's library folder is
https://drive.google.com/drive/folders/1ZDULRMNk-5JohCAI_ZG9W5lvvEp8HP3Q, in
a Google account the owner created. Every 5 minutes, and right after an app
saves something, one pass:

1. **Out.** Each creative not in Drive yet is uploaded to
   `<vertical>/<set>/<name>.<ext>` (folders made as needed), and each set whose
   headlines changed gets its `Headlines.txt` written again (one per line,
   hidden ones left out). Our id rides on each file as a Drive app property,
   which only the library's own Google client can read.
2. **In.** The whole folder is listed; each listing page is kept raw in
   `library.drive_page` first. A picture the library does not have is
   downloaded once, for its hash and thumbnail, and added, its bytes staying
   in that Drive file: its vertical from the top folder's name (or code), its set from
   the folder it is directly in. Pictures are JPEG, PNG, WebP or GIF, up to
   40 MB; anything else is recorded and left alone.
3. **Gone.** After a whole listing, a file that was not in it is marked gone.
   Its creative's row and thumbnail stay; its bytes went with the file.

The library never deletes or moves anything in Drive. The one thing it
renames is a set's folder, when the app that owns the set renames it
(`PATCH /api/sets/{id}`; Create does when a session is renamed). Headlines
typed into a Drive file by hand are not read (only pictures are).

**Signing in.** The folder's owner is a personal Google account, where a
service account cannot own files, so the library signs in as that account,
once, and keeps the refresh token (`library.drive_login`, never in
`library_api`). The Google Cloud project `adhunters-library` has the Drive
API on, an External consent screen **published** (a project left in testing
loses its sign-in every 7 days), and a **Desktop app** OAuth client whose id
and secret go in `/etc/adhunters/library.env`. Then, on the data box:

```
sudo /opt/adhunters/bin/library drive-login
```

It prints a link; open it, allow access (Google warns the app is not
verified: Advanced, then continue), and paste back the address the browser
ends on (a page that does not load). It checks the account can open the
folder, saves the sign-in, and the running library picks it up on its next
pass. Without a sign-in, saving still works and every new creative waits in
its row to be uploaded; pictures already in Drive answer 503 until the
sign-in is back.

## API

On `LIBRARY_ADDR` (localhost only). Called by the apps' servers; a request
from a page (with an `Origin` header) is refused. Errors are
`{"error": "<one line>"}`: 400 bad input, 404 not found, 410 the picture's
Drive file was deleted, 413 too big, 503 Drive off, 500 anything else. Lists are newest first; `?before=<id>`
continues after the last id of a page.

- `GET /api/status`: counts, and Drive: on or why not, the account, how
  many creatives wait, the last pass.
- `POST /api/drive/sync`: a pass now (202).
- `GET /api/verticals`, `PATCH /api/verticals/{id}` (`name`, `code`,
  `network_letter`, `next_number`).
- `GET /api/sets?vertical=`, `POST /api/sets` (`name`, `vertical_id`,
  `vertical_name` for a new vertical, `origin`, `origin_ref`, `made_by`; a
  name already used in the vertical gets " (2)"), `GET /api/sets/{id}` (the
  set, its creatives and headlines in order), `PATCH /api/sets/{id}`
  (`{"name": ...}`: a name another set of the vertical has is refused; the
  Drive folder is renamed on the next pass).
- `GET /api/creatives?vertical=&set=&angle=&origin=&ai_label=&q=&hidden=1&limit=&before=`,
  `GET /api/creatives/{id}`.
- `POST /api/creatives`: multipart, `file` (the picture) and `meta` (JSON:
  `vertical_id`, `vertical_name`, `set_id`, `angle`, `idea`, `origin`,
  `origin_ref`, `ai_label`, `made_by`, and `name` when there is no vertical).
  201 new, 200 when those bytes were kept already (then only added to the
  set).
- `PATCH /api/creatives/{id}` and `PATCH /api/headlines/{id}`: `angle`,
  `ai_label`, `hidden`, `add_to_set`.
- `GET /api/headlines?...` (the same filters), `POST /api/headlines`
  (`{"headlines": [{text, vertical_id, set_id, angle, origin, origin_ref,
  ai_label, made_by}]}`; a text kept already returns that headline).
- `GET /files/{id}`, `GET /thumbs/{id}`: the picture (from Drive once
  uploaded) and its thumbnail (JPEG). Cached for good: an id's bytes never
  change.

The same rows are published for reading in `library_api`
(contract/sql/library): `vertical_v1`, `creative_v1`, `headline_v1`,
`set_v1`, `set_creative_v1`, `set_headline_v1`. `sha256` is there for the ad
id: `ah-` + the creative's first 10 + `-` + the headline's first 10.

## Settings

`library/deploy/library.env.example`:

| Name | Default | |
|---|---|---|
| `DATABASE_URL` | | The `library` login. It runs the migrations on start. |
| `LIBRARY_ADDR` | `127.0.0.1:8093` | The API. |
| `LIBRARY_FILES` | unset | Only to move out of the old bucket once (decision 0020): `s3://adhunters-library` with `S3_*`. The library copies its thumbnails and any waiting bytes into the rows on start; then remove it. |
| `LIBRARY_DRIVE_FOLDER` | unset | The library folder's id. Unset: Drive is off. |
| `LIBRARY_GOOGLE_CLIENT_ID`, `LIBRARY_GOOGLE_CLIENT_SECRET` | unset | The Desktop app OAuth client. Unset: Drive is off. |
| `LIBRARY_GOOGLE_TOKEN_URL`, `LIBRARY_GOOGLE_API_URL` | unset | For tests only: a fake Google token endpoint and Drive API. Unset: Google's own. |
| `OPS_ADDR` | | `/healthz`, `/metrics` (`127.0.0.1:9110` on the data box). The Drive pass reports as task `drive-sync`. |

## Run it

```
DATABASE_URL=postgres://… go run ./library/cmd/library
curl -s localhost:8093/api/status
```

## Tests

```
PG_TEST_URL=postgres://postgres:test@localhost:5432/postgres?sslmode=disable go test ./...
```

The Drive tests run against a fake Drive (`internal/drive/drivetest`); the
real one was not called from CI.
