// matrix: the plain-data part of Novos anúncios' combinations and of the
// team's names, with no page in it, so node can test it (launch/web/test).
// Pictures are columns (A, B, C…), headlines rows; each ticked cell is one
// ad, AD01, AD02… in reading order (row by row, left to right), and the same
// ads go into every chosen campaign.

// CHIPS are the buttons Botão shows, Taboola's label with ours. The rest of
// the bulk sheet's CTAS (the ones Backstage also takes) are under "Mais".
export const CHIPS = [
  ['Learn More', 'Saiba mais'],
  ['Read More', 'Leia mais'],
  ['Shop Now', 'Compre agora'],
  ['Get Offer', 'Ver oferta'],
  ['Sign Up', 'Inscreva-se'],
  ['', 'Sem botão'],
];

// moreCTAs is what "Mais" offers: every CTA not already a chip.
export function moreCTAs(all) {
  const chips = new Set(CHIPS.map(([v]) => v));
  return all.filter((c) => !chips.has(c));
}

// letter names a column: A…Z, then AA, AB…
export function letter(i) {
  let s = '';
  for (let n = i + 1; n > 0; n = Math.floor((n - 1) / 26)) s = String.fromCharCode(65 + ((n - 1) % 26)) + s;
  return s;
}

const pad = (n) => String(n).padStart(2, '0');

// cell is the key of one cell: a headline's id and a picture's sha256.
export const cell = (row, col) => row + '|' + col;

// matrixAds lists the ticked cells as ads, in reading order: {row, col, n}
// with row and col their places and n the ad's number (1 for AD01).
// rows: [{id}], cols: [{sha256}], ticked: a Set of cell keys.
export function matrixAds(rows, cols, ticked) {
  const out = [];
  rows.forEach((r, ri) => cols.forEach((c, ci) => {
    if (ticked.has(cell(r.id, c.sha256))) out.push({ row: ri, col: ci, n: out.length + 1 });
  }));
  return out;
}

// toggle ticks every cell in keys, or unticks them all when all are
// ticked already (a click on a row's or column's header).
export function toggle(ticked, keys) {
  const all = keys.length > 0 && keys.every((k) => ticked.has(k));
  for (const k of keys) all ? ticked.delete(k) : ticked.add(k);
  return ticked;
}

// forget drops the cells of a row or column taken out.
export function forget(ticked, part) {
  for (const k of [...ticked]) if (k.split('|').includes(part)) ticked.delete(k);
  return ticked;
}

// ---- the team's names (launch/internal/actions/naming.go) ----

const NUMBERED = /^\s*(?:GRP)?(\d{1,6})\s*$/i;
const TAIL = /-(?:Desk|Desktop|Mobile)-pp-bl$/i;

// groupPrefix is what a group's campaigns start with: GRP<nn> for a
// numbered group, else the group's own name.
export function groupPrefix(name) {
  const m = NUMBERED.exec(name || '');
  return m ? 'GRP' + pad(+m[1]) : String(name || '').trim().split(/\s+/).join(' ');
}

// campaignName is the team's name for a campaign: GRP01-CMP03-Desk-pp-bl.
export function campaignName(prefix, number, device) {
  return `${prefix}-CMP${pad(number)}-${device === 'mobile' ? 'Mobile' : 'Desk'}-pp-bl`;
}

// adName is the n-th ad's name in a campaign: AD<nn> before the device
// ending (GRP01-CMP01-AD01-Desk-pp-bl), or after a name without one.
// Taboola's items have no name: it shows here and in History only.
export function adName(campaign, n) {
  const name = String(campaign || '').trim();
  const m = TAIL.exec(name);
  return m ? `${name.slice(0, m.index)}-AD${pad(n)}${m[0]}` : `${name}-AD${pad(n)}`;
}

// split is a campaign name's base and device ending ('' when none).
function split(name) {
  const m = TAIL.exec(name);
  return m ? [name.slice(0, m.index), m[0]] : [name, ''];
}

const andList = (xs) => (xs.length < 2 ? xs.join('') : xs.slice(0, -1).join(', ') + ' e ' + xs[xs.length - 1]);

// namesLine says the names the ads get in the chosen campaigns: "Nomes:
// GRP01-CMP01-AD01 a AD08, com -Desk-pp-bl e -Mobile-pp-bl". '' without
// ads or campaigns.
export function namesLine(campaigns, n) {
  if (!n || !campaigns.length) return '';
  const bases = [];
  const tails = [];
  for (const c of campaigns) {
    const [b, t] = split(String(c || '').trim());
    if (!bases.includes(b)) bases.push(b);
    if (t && !tails.includes(t)) tails.push(t);
  }
  const range = (b) => `${b}-AD01` + (n > 1 ? ` a AD${pad(n)}` : '');
  const shown = bases.slice(0, 3).map(range);
  if (bases.length > 3) shown.push(`mais ${bases.length - 3}`);
  return 'Nomes: ' + andList(shown) + (tails.length ? ', com ' + andList(tails) : '');
}

// sheetName is an ad's "Ad Name" in the bulk sheet, whose one row goes in
// every campaign: the campaigns' shared base with AD<nn>, or AD<nn> alone.
export function sheetName(campaigns, n) {
  const bases = [...new Set(campaigns.map((c) => split(String(c || '').trim())[0]))];
  return bases.length === 1 && bases[0] ? `${bases[0]}-AD${pad(n)}` : `AD${pad(n)}`;
}

// shortName is a campaign's name as a chip shows it: from CMP<nn> on,
// without the -pp-bl ending (GRP01-CMP01-Desk-pp-bl is CMP01-Desk).
export function shortName(name) {
  const n = String(name || '').trim().replace(/-pp-bl$/i, '');
  const m = /(?:^|-)(CMP\d+-.+)$/i.exec(n);
  return m ? m[1] : n;
}

// ---- the library beside the matrix (library.js) ----

// ORIGINS are the picture filters and the library's origins each keeps.
export const ORIGINS = [['', 'Todas'], ['upload,drive', 'Originais'], ['create', 'Geradas']];

// originLabel is a picture's badge: GERADA for one made in Create, else
// ORIGINAL.
export function originLabel(c) {
  return c?.origin === 'create' ? 'Gerada' : 'Original';
}

// folderList turns the library's folders (GET /api/folders) into the
// picker's choices: every vertical's platform folders ("Memory Loss ›
// Taboola"), each followed by the vertical's sets of that platform.
export function folderList(data) {
  const out = [];
  for (const v of data?.verticals || []) {
    for (const p of v.platforms || []) {
      out.push({ key: `p:${v.id}:${p.id}`, path: [v.name || v.id, p.name || p.id], q: { vertical: v.id, platform: p.id } });
      for (const st of v.sets || []) {
        if ((st.platform || '') !== p.id) continue;
        out.push({ key: 's:' + st.id, path: [v.name || v.id, p.name || p.id, st.name || 'Pasta ' + st.id], q: { set: String(st.id) }, set: true });
      }
    }
    // Sets without a platform folder still show, under their vertical.
    for (const st of v.sets || []) {
      if ((v.platforms || []).some((p) => p.id === (st.platform || ''))) continue;
      out.push({ key: 's:' + st.id, path: [v.name || v.id, st.name || 'Pasta ' + st.id], q: { set: String(st.id) }, set: true });
    }
  }
  return out;
}

// adAI says whether one ad carries Taboola's AI label (ai_disclosure): the
// person's choice for that ad in Revisar when there is one (chosen maps
// cell keys to true or false), else on when its picture or its headline is
// labelled AI in the library or the picture looks AI-made.
export function adAI(row, col, chosen) {
  const k = cell(row.id, col.sha256);
  return chosen.has(k) ? !!chosen.get(k) : !!(col.ai || row.ai);
}
