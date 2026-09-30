# 0014 · One library for Create and Launch, stored in the team's Drive

**Decided:** 29 Sep 2026 by Marcos (the library on Google Drive, never shown
as Drive), 30 Sep 2026 (the Google account and folder). Built by the
library service.

Create and Launch keep one library of creatives and headlines. Create saves
what people choose into it; Launch makes ads from it. It is its own service,
`library/`, which owns the `library` schema and publishes `library_api`.
Apps read the views and add through its localhost API (which carries the
files). Neither app keeps creatives of its own.

The team knows Google Drive and can upload and download there, so every
file also lives in the team's folder, in `<vertical>/<set>/`, and files the
team drops in the folder come into the library. The apps never show that it
is Drive. The database is the truth; Drive is a copy people can use:

- Each picture's bytes are kept in our own store before its row is written,
  so a file deleted in Drive is never lost.
- The library never deletes, moves or renames anything in Drive.
- Our id rides on each Drive file as an app property, so a renamed or moved
  file is still ours.

The folder's owner is a personal Google account, where a service account
cannot own files, so the library signs in once as that account (OAuth,
Desktop client, a published consent screen) and keeps the refresh token.

**Why:** two apps with their own creative stores would drift, and the team
would not find in Launch what it made in Create. Drive is where the team
already works; the safe copy keeps the rule that no data is lost.

**Not decided here:** importing auto-creative's galeria (its names, like
MMT48, and counters); reading headlines people type into Drive.
