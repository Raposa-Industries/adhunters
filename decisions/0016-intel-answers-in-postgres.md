# 0016 · Intel keeps its raw answers in Postgres, for now

**Decided:** 30 Sep 2026, as the default the Intel build took ("go ahead
with everything", Marcos, 30 Sep 19:27). A newer file can overturn it.

[0003](0003-raw-first.md) says raw goes to a local spool, then to object
storage, and facts are parsed from there. Intel keeps the first half: every
Taboola and RedTrack answer goes to intel-collect's spool on disk before
anything reads it. The second half is Postgres, not object storage: the
spool drains into `intel.answer` (the body gzipped), and intel-numbers
parses from that table and can parse any range again (`intel-numbers
reload`).

**Why:** Intel's answers are small next to Tracks' scrapes: several thousand a
day, an estimated tens of MB gzipped, where Tracks writes a raw file a minute per capture
instance. One table keeps Intel to one store to back up, and `reload` needs
no archive reader. Nothing is deleted: when the table grows past what the
database should hold (a GB or so), old answers move to object storage the
way Tracks' raw files do, and this file is replaced.
