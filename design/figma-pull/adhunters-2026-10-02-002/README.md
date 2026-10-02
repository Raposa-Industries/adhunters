# adhunters-2026-10-02-002: full pull of AdHunters · Ember

- Figma file: https://www.figma.com/design/N4eZQ2Mi2dPjo3yBGolUx1 (AdHunters · Ember)
- Exported 2026-10-02T12:13:10Z by the Figma Middleman Bridge plugin (all pages, SVGs for vectors, 2x PNGs of every top-level frame).
- Compared with export 001 (2026-10-02T04:55:03Z, right after the first copy was applied).
- The export ran before Draw Designer batch 126 (Launch nested table, slim filters, top bars, creation pages), which was queued after it. Those edits are NOT in this export; a FIGMA-CHANGED notice follows when they land.

## Headline
1. **There is no new account/accounts page in the Figma file.** No page or frame was added except Draw Designer's own backup page, and no text in any frame changed. The account/credentials screen exists only in Adhunters' code so far. To get it into Figma, send a push request (new capture of that page, same library format as 001).
2. **App selector (Draw Designer edit "Option A" + sidebar alignment)**: the 5 `App selector · *` components (Components page) gained a "Selected app" block (glyph, app name in Archivo Expanded Bold, one-line description, keyboard keys, chevron); the old compact pill parts are hidden, height 28 -> 48. `Ember · Menu de apps aberto` follows. Originals kept on page "Draw Designer backups" (12 frames).
3. **Headings regressed to Inter Bold**: 458 heading texts on Ember, Launch, Create, Spy, Intel and Funnels (Raposa unchanged) went from Archivo ExtraBold (normal width) to Inter Bold. The Archivo Expanded fix batch asked for "Archivo Expanded ExtraBold", which the user's Figma does not have, so the plugin fell back to Inter. Only 42 texts are in Archivo Expanded (Bold). Widths/x positions of those headings and their auto-layout parents shifted as a result (the "layout" changes in diff.md). Being raised with the user; treat Archivo Expanded ExtraBold as the intended heading font.
4. **Variables/styles**: none in the file, before or after (variables.json). Tokens are raw hex values on nodes.

## Files
- `diff.md`: readable diff, frame by frame. `diff.json`: the same, machine-readable (every changed field old -> new).
- `frames/<page>-<frame>.json`: full node tree (layout, sizing, text, fonts, colors, radius, effects, instance links, keys `adh.*`) for each added/changed frame. `frames/*.outline.txt`: the same as an indented outline.
- `png/<page>-<frame>.png`: 2x PNG of each added/changed frame.
- `variables.json`: current variables (none).
- `raw/export.json`: the complete export (all pages). `raw/baseline-export-001.json`: export 001. `raw/result.json`: plugin result.
- `tools/diff.py`: regenerate: `python3 tools/diff.py raw/baseline-export-001.json raw/export.json .`
