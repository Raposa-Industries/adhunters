# RawFileQuarantined

Chat: Telegram, silent, 08:00 to 22:00 São Paulo. Rule: `platform/observe/rules/`.

## What it means

At least one raw file failed to load 3 times, or can never load as it is, and was set aside. The loader moved on; the file's scrapes are missing from the numbers until it is fixed and replayed.

## Check

1. `tracks-loader status` lists quarantined files with their last error.
2. The error says which: a parser bug (a code fix), a bad checksum (the archive copy is damaged), or a day whose sightings were dropped (replay the whole day).

## Fix

- Parser bug: fix the parser by PR, deploy, then replay the file's hour: `tracks-loader replay -from <hour> -to <hour+1h>`. A replay also takes the files of its range out of quarantine.
- Damaged archive copy: the spool keeps files 48 hours after shipping; if the local file is still there, ask the owner before replacing the archive copy.

## After

It clears when no file is quarantined. Check `tracks-loader status -books` after the replay.
