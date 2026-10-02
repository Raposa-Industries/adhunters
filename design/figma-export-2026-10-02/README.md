# Figma export, 2026-10-02

`adhunters-library-main.json` is every AdHunters screen, captured fresh from
main c7c2d5f over test data at 1440 px wide, in the Draw Designer library
format (the same format as the Draw Designer libraries from 2026-10-01).

- 41 pages: the Ember design system and the open app menu, then 14 Launch,
  4 Create, 6 Spy, 4 Intel, 6 Funnels and 6 Raposa pages.
- 28 components with variants. The 21 that the 2026-10-01 libraries already
  had keep their component and node ids (matched by name and shape), so
  anything keyed to those ids still maps.
- Tokens are the 37 Ember colors and the font and layout variables.
- Every Ember page matches the real page within 2 px, except a few text
  offsets of 3 to 6 px.
- Raposa's pages are plain browser HTML with no Ember styles. Their form rows
  sit up to about 8 px off, because of the browser's own fieldset layout.
- The mascot is the original art, carried as-is. Never redraw it.

Made for Figma Middleman, which turns it into the Figma file
"AdHunters · Ember". Nothing in the apps reads this folder.
