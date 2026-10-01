# 0020 · The library keeps its files in Drive only

**Decided:** 1 Oct 2026 by Marcos ("drive only no backup"), when asked
whether the library should keep its own bucket behind Drive. Replaces the
safe-copy part of [0014](0014-library.md); the rest of 0014 stands. Built by
the library service.

A creative's bytes live in the team's Drive folder and nowhere else of ours.
There is no bucket:

- A picture saved from an app waits in its row (`library.creative.pending`)
  until the Drive sync uploads it, a few seconds later, and then the row lets
  the bytes go. Saving therefore never waits on Drive, and a save made while
  Drive is signed out is uploaded once it is back.
- A picture someone drops in the folder is read once, for its hash and
  thumbnail, and its bytes stay in that Drive file.
- `GET /files/{id}` reads the bytes from Drive. Each creative's 480px JPEG
  thumbnail stays in its row, so lists never wait on Drive.
- Drive's raw listing pages are kept in `library.drive_page`, one row per
  distinct page (anything from outside is saved raw before parsing).

**What this gives up:** a picture whose file someone deletes in Drive is
gone. Its row, name, thumbnail and the ads made from it stay; `/files/{id}`
answers 410. Saving the same bytes again brings it back. Drive's own trash
keeps a deleted file for 30 days. This is the owner's call against AGENTS.md's
"never lose collected data" for this one kind of data: pictures the team made
or chose, which they can see and manage in Drive themselves.

**The old bucket:** `adhunters-library` held the safe copies for a few hours
on 1 Oct. On the first start with `LIBRARY_FILES` still set, the library
moves each thumbnail and any bytes still waiting out of it into the rows;
after that the setting is removed and the bucket is unused. Deleting the
bucket is the owner's call.
