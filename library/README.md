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
  A set may say its platform (`taboola` or `newsbreak`, from Create's
  session); its creatives are then minted with that platform's network
  letter (`BPT43`, `BPN44`; the counter is the vertical's, shared by both),
  and its folder goes under the platform's (below). Sets made without one
  (all of them before 2 Oct 2026) keep the vertical's own letter and folder.
- **Files, in Drive only.** A picture saved from an app waits in its row
  until the next Drive pass uploads it (seconds later), and then only Drive
  has its bytes; `/files/{id}` reads them back from there. Each creative's
  480px JPEG thumbnail stays in its row. A file deleted in Drive (or put in
  its trash) is gone: it leaves the lists and the counts, its row, name and
  thumbnail stay, `/files/{id}` answers 410, and saving the same bytes again,
  or taking the file back out of the trash, brings it back.

- **Tags.** Words people put on creatives and headlines ("cozinha"), kept
  in lower case without a leading `#`; up to 20 at a time. The search (`q`)
  finds them too.
- **Originals and generated.** A creative made in Create (origin `create`)
  is generated; one uploaded or found in Drive (`upload`, `drive`) is an
  original. Create's pages show them as GERADA and ORIGINAL.
- **Refiles.** Putting a creative or headline in another set (GLOSSARY:
  refile; Mover in Create's pages) takes it out of every set it was in and
  records where it was (`library.refile`). Only into a set of its own
  vertical. Its Drive file stays where it is: the library still never moves
  anything in Drive.

Nothing is deleted. Hiding a creative or headline takes it out of the lists
(Apagar in Create's pages); anything that points at it still finds it.

The verticals are our fixed list's ids (shared/verticals/verticals.yaml). The team's nine
must-haves start with codes (Blood Pressure `BP`, Memory Loss `MM`, Weight
Loss `WL`, Tinnitus `TIN`, Diabetes `DB`, Neuropathy `NP`, Prostate Health
`PR`, Joint Pain `JP`, Vision `VS`), so the team's Taboola names start
`BPT`, `DBT`, `MMT`, `NPT`, `TINT` and `WLT`. Tinnitus was `TN` until
migration 0005, which changed it only where it was still `TN`; names minted
before (`TNT…`) keep theirs. Another vertical gets a code from its
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
   `<vertical>/<set>/<name>.<ext>`, or `<vertical>/<platform>/<set>/…` for
   a set with a platform (`Taboola`, `NewsBreak`; folders made as needed; a
   set whose folder exists already keeps it where it is), and each set whose
   headlines changed gets its `Headlines.txt` written again (one per line,
   hidden ones left out). Our id rides on each file as a Drive app property,
   which only the library's own Google client can read.
2. **In.** The whole folder is listed; each listing page is kept raw in
   `library.drive_page` first. A picture the library does not have is
   downloaded once, for its hash and thumbnail, and added, its bytes staying
   in that Drive file: its vertical from the top folder's name (or code), its set from
   the folder it is directly in. A platform's folder is no set: a folder a
   person makes inside it is a set of that platform, and a picture loose in
   it belongs to the vertical only. Pictures are JPEG, PNG, WebP or GIF, up to
   40 MB; anything else is recorded and left alone.

   Headlines a person typed come in the same way: a Google Doc (or a text
   file) named `Headlines` in a folder, one headline per line, list bullets
   and numbers dropped, any length kept (no headline is refused for its
   length anywhere; the apps warn). When the file changed or moved, its text is
   kept raw in `library.drive_text`, each line becomes a headline of that
   folder (its set, or its vertical), and a line taken out takes its
   headline out of the lists (hidden, kept) unless another file still lists
   it or an app saved it first. `library.drive_file_headline` holds which
   file lists which headline. The library's own `Headlines.txt` is never
   read.
3. **Gone.** After a whole listing, a file that was not in it (deleted, or in
   Drive's trash) is marked gone, and its creative leaves the lists (Create's
   and Launch's Biblioteca) on that pass. Its row and thumbnail stay; its
   bytes went with the file. A person's `Headlines` file that is gone takes
   its headlines out the same way. A file taken back out of the trash is
   listed again on the next pass.

The library never deletes or moves anything in Drive. The one thing it
renames is a set's folder, when the app that owns the set renames it
(`PATCH /api/sets/{id}`; Create does when a session is renamed). Lines
typed into the library's own `Headlines.txt` are not read; a `Headlines`
Doc is.

Every call to Drive is counted and timed on `/metrics` by status, as
provider `drive` (`adhunters_outbound_requests_total`, `kit/ops` `Transport`).

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
- `GET /api/folders`: the library as the pages show it: every vertical with
  its platforms' folders and its sets, each with how many creatives and
  headlines (not hidden) it holds, and the whole library's counts
  (`creatives`, `original`, `generated`, `headlines`). With `?fresh=1` it
  first waits (20 s at most) for a Drive pass that started after the call:
  Create's and Launch's Biblioteca ask for it each time they open, then
  draw again, so what was added or deleted in Drive shows.
- `GET /api/tags?vertical=`: the tags in use, the most used first.
- `GET /api/sets?vertical=`, `POST /api/sets` (`name`, `vertical_id`,
  `vertical_name` for a new vertical, `origin`, `origin_ref`, `made_by`,
  optional `platform`; a name already used in the vertical gets " (2)"), `GET /api/sets/{id}` (the
  set, its creatives and headlines in order), `PATCH /api/sets/{id}`
  (`{"name": ...}`: a name another set of the vertical has is refused; the
  Drive folder is renamed on the next pass).
- `GET /api/creatives?vertical=&set=&angle=&origin=&ai_label=&q=&tag=&platform=&sort=&hidden=1&limit=&before=`,
  `GET /api/creatives/{id}`. `origin` takes several, joined by commas
  (`upload,drive`: the originals); `platform` keeps those in a set of that
  platform; `sort` is `new` (the default), `old` or `name` (`before` pages
  the default only). Each creative carries its `tags`.
- `POST /api/creatives`: multipart, `file` (the picture) and `meta` (JSON:
  `vertical_id`, `vertical_name`, `set_id`, `angle`, `idea`, `origin`,
  `origin_ref`, `ai_label`, `made_by`, optional `platform` (else the set's,
  else the vertical's letter), optional `tags`, and `name` when there is no
  vertical).
  201 new, 200 when those bytes were kept already (then only added to the
  set).
- `GET /api/headlines/{id}`: one headline, hidden or not.
- `PATCH /api/creatives/{id}` and `PATCH /api/headlines/{id}`: `angle`,
  `ai_label`, `hidden`, `add_to_set`, `refile_to` (a set of its vertical),
  `add_tags`, `remove_tags`, and `by` (who, for the record).
- `GET /api/headlines?...` (the same filters), `POST /api/headlines`
  (`{"headlines": [{text, vertical_id, set_id, angle, origin, origin_ref,
  ai_label, made_by, tags}]}`; a text kept already returns that headline,
  with the tags added).
- `GET /files/{id}`, `GET /thumbs/{id}`: the picture (from Drive once
  uploaded) and its thumbnail (JPEG). Cached for good: an id's bytes never
  change.

The same rows are published for reading in `library_api`
(contract/sql/library): `vertical_v1`, `creative_v1`, `headline_v1`,
`set_v1`, `set_creative_v1`, `set_headline_v1`. `sha256` is there for the ad
id: `ah-` + the creative's first 10 + `-` + the headline's first 10. Tags,
a set's platform and refiles are not published there yet (a new view
version in `contract/` would); the HTTP API carries them.

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
