# adhunters-2026-10-02-003: batch-126 frames after the -008 polish

- Figma file: https://www.figma.com/design/N4eZQ2Mi2dPjo3yBGolUx1 (AdHunters · Ember)
- Exported 2026-10-02T12:26:28Z, after Draw Designer batches -007 (Launch nested table, slim filters, top bars, creation pages) and -008 (heading font, SVG arrows and tick, uppercase tags). The round-trip thread had no further polish queued when this ran.
- Diff is against the -002 export (12:13:10Z), limited to these 14 frames: Ember · Menu de apps aberto and the 13 Launch frames (8:1749, 8:1108, 8:2095, 8:2713, 8:3206, 8:742, 8:3672, 8:3823, 8:3963, 8:4209, 8:4347, 8:4806, 8:4934, 8:5206). Frame ids and sizes are unchanged.

## What changed
- The bodies of Campanhas, Grupos de campanha, Anúncios, Campanha, Novo (menu aberto), Nova campanha, Novo grupo, Novos anúncios and Ember · Menu de apps aberto were rebuilt by Draw Designer, so most of their layers have new ids. diff.md lists them as removed + added; read the new structure from `frames/*.json` / `*.outline.txt` instead of the per-layer diff.
- Presets, Histórico, Rascunhos, Pedidos and Pedido only lost 2-3 layers (the old top-bar parts) and moved a few x/w values (slimmer top bar).
- Copy: tags are uppercase now; check the outlines for exact strings.
- Fonts in these frames: headings are still Inter Bold (161 texts) except 27 in Archivo Expanded Bold. Same font fallback as in -002 (Archivo Expanded ExtraBold isn't installed in the user's Figma); intended heading font is Archivo Expanded ExtraBold. Body text is IBM Plex Sans / Mono.
- Variables/styles: none (variables.json).

## Files
- `diff.md`, `diff.json`: diff against -002.
- `frames/<page>-<frame>.json` and `.outline.txt`: full current tree of each of the 14 frames.
- `png/<page>-<frame>.png`: 2x PNG of each.
- `raw/export.json`: the plugin's export, including the 14 "(before batch 126)" backups from page "Draw Designer backups" (raw only). `raw/new-scoped.json`, `raw/old-scoped-from-002.json`: the diff inputs. `raw/result.json`: plugin result.
- `tools/diff.py`: `python3 tools/diff.py raw/old-scoped-from-002.json raw/new-scoped.json .`
