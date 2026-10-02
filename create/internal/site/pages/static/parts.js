// Create's page parts that are plain data, with no page around them, so
// node can test them (create/internal/site/test): dates as the lists say
// them, references and how they travel in an address, the library's folder
// tree, and what a turn or a save says.

// ---- dates ---------------------------------------------------------------------

function day(d) { return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime(); }
function two(n) { return String(n).padStart(2, '0'); }

// whenLabel is a past moment as the conversations list says it: hoje,
// ontem, or dd/mm.
export function whenLabel(t, now = new Date()) {
  const d = new Date(t);
  const diff = Math.round((day(now) - day(d)) / 86400000);
  if (diff <= 0) return 'hoje';
  if (diff === 1) return 'ontem';
  return `${two(d.getDate())}/${two(d.getMonth() + 1)}`;
}

// ddmm is a date as dd/mm; hhmm a time as hh:mm.
export function ddmm(t) { const d = new Date(t); return `${two(d.getDate())}/${two(d.getMonth() + 1)}`; }
export function hhmm(t) { const d = new Date(t); return `${two(d.getHours())}:${two(d.getMinutes())}`; }

export function plural(n, one, many) { return `${n} ${n === 1 ? one : many}`; }

// ---- references -----------------------------------------------------------------

// A reference is what the next turn starts from (GLOSSARY: pick): an item of
// the session {type: 'item', id}, a library creative {type: 'creative', id}
// or headline {type: 'headline', id}, or a picture from the computer
// {type: 'file'}. refKey names one, so a second click finds it.
export function refKey(r) {
  if (r.type === 'file') return 'f:' + r.key;
  return { item: 'i', creative: 'c', headline: 'h' }[r.type] + ':' + r.id;
}

// toggleRef adds r to refs, or takes it out when it is there; at most max.
// It returns the new list and whether r is in it now.
export function toggleRef(refs, r, max = 8) {
  const k = refKey(r);
  if (refs.some((x) => refKey(x) === k)) return { refs: refs.filter((x) => refKey(x) !== k), on: false };
  if (refs.length >= max) return { refs, on: false, full: true };
  return { refs: [...refs, r], on: true };
}

// refsParam writes the library references for an address
// (/create/?ref=c12,h5); parseRefs reads them back.
export function refsParam(refs) {
  return refs.filter((r) => r.type === 'creative' || r.type === 'headline').map((r) => (r.type === 'creative' ? 'c' : 'h') + r.id).join(',');
}

export function parseRefs(s) {
  const out = [];
  for (const part of String(s || '').split(',')) {
    const m = part.trim().match(/^([ch])(\d{1,15})$/);
    if (m) out.push({ type: m[1] === 'c' ? 'creative' : 'headline', id: Number(m[2]) });
  }
  return out;
}

// libraryItemFor is the session's item that already holds a library
// reference, so sending it twice does not add it twice.
export function libraryItemFor(items, r) {
  const kind = r.type === 'creative' ? 'image' : 'headline';
  return items.find((it) => it.origin === 'library' && it.kind === kind && it.state === 'done' && it.library_ref === String(r.id)) || null;
}

// shortText is a headline cut to its first words (at most n characters,
// whole words) with "...", as the composer's chips show it.
export function shortText(text, n = 26) {
  const t = String(text || '').trim();
  if (t.length <= n) return t;
  const cut = t.slice(0, n + 1);
  const at = cut.lastIndexOf(' ');
  return (at > 8 ? cut.slice(0, at) : t.slice(0, n)).replace(/[\s.,;:!?-]+$/, '') + '...';
}

// ---- turns and saves --------------------------------------------------------------

// answerLabel is how an answer starts: "Create · 4 imagens e 3 headlines".
export function answerLabel(images, headlines) {
  const parts = [];
  if (images) parts.push(plural(images, 'imagem', 'imagens'));
  if (headlines) parts.push(plural(headlines, 'headline', 'headlines'));
  return 'Create · ' + (parts.join(' e ') || 'nada');
}

// savedIn maps each saved item id to the folder its last done save put it
// in.
export function savedIn(saves) {
  const out = new Map();
  for (const v of saves || []) {
    if (v.state !== 'done') continue;
    for (const id of v.item_ids) out.set(id, v.set_name || '');
  }
  return out;
}

// sessionNameFrom names a new conversation from its first words, or its
// date when it has none; a name is a folder, so no slashes.
export function sessionNameFrom(prompt, now = new Date()) {
  const words = String(prompt || '').replace(/[\\/]+/g, ' ').replace(/\s+/g, ' ').trim().split(' ').filter(Boolean).slice(0, 6).join(' ');
  if (words) return words.length > 60 ? words.slice(0, 60).trim() : words.charAt(0).toUpperCase() + words.slice(1);
  return `Conversa ${ddmm(now)} ${hhmm(now)}`;
}

// defaultCounts is how many pictures and headlines to ask for, from what
// is referenced: only headlines asks for headlines only.
export function defaultCounts(kinds) {
  const pics = kinds.includes('image');
  const heads = kinds.includes('headline');
  if (heads && !pics) return { images: 0, headlines: 5 };
  return { images: 4, headlines: 5 };
}

// ---- the library ---------------------------------------------------------------------

// isOriginal: a creative the team brought in (upload, drive), not one made
// in Create.
export function isOriginal(c) { return c.origin !== 'create'; }

// originParam is the library's origin filter for Todas, Originais, Geradas.
export function originParam(f) { return f === 'original' ? 'upload,drive' : f === 'generated' ? 'create' : ''; }

// A folder is a place in the tree: {vertical, platform, set}; all empty is
// the whole library.
export const ROOT = Object.freeze({ vertical: '', platform: '', set: 0 });

export function folderKey(f) { return f.set ? 's:' + f.set : f.platform ? `p:${f.vertical}:${f.platform}` : f.vertical ? 'v:' + f.vertical : 'root'; }

export function parseFolderKey(k, folders) {
  const s = String(k || '');
  let m;
  if ((m = s.match(/^s:(\d+)$/))) {
    const set = findSet(folders, Number(m[1]));
    return set ? { vertical: set.vertical_id, platform: set.platform || '', set: set.id } : { ...ROOT };
  }
  if ((m = s.match(/^p:([a-z0-9-]+):(taboola|newsbreak)$/))) return { vertical: m[1], platform: m[2], set: 0 };
  if ((m = s.match(/^v:([a-z0-9-]+)$/))) return { vertical: m[1], platform: '', set: 0 };
  return { ...ROOT };
}

export function findSet(folders, id) {
  for (const v of (folders && folders.verticals) || []) {
    const s = (v.sets || []).find((x) => x.id === id);
    if (s) return s;
  }
  return null;
}

const PLATFORM_NAMES = { taboola: 'Taboola', newsbreak: 'NewsBreak' };
export function platformName(p) { return PLATFORM_NAMES[p] || ''; }

// folderPath is a folder's names from the top: ['Memory Loss', 'Taboola',
// 'Colher']. verticalNames names verticals the library has not seen yet.
export function folderPath(folders, f, verticalNames = {}) {
  if (!f.vertical) return [];
  const v = ((folders && folders.verticals) || []).find((x) => x.id === f.vertical);
  const out = [v ? v.name : verticalNames[f.vertical] || f.vertical];
  if (f.platform) out.push(platformName(f.platform));
  if (f.set) {
    const s = findSet(folders, f.set);
    out.push(s ? s.name : 'pasta ' + f.set);
  }
  return out;
}

// listQuery is the library list's filters for a folder, an origin filter,
// a search and a sort.
export function listQuery(f, { origin = 'all', q = '', sort = '', limit = 120 } = {}) {
  const p = new URLSearchParams();
  if (f.set) p.set('set', String(f.set));
  else {
    if (f.vertical) p.set('vertical', f.vertical);
    if (f.platform) p.set('platform', f.platform);
  }
  const o = originParam(origin);
  if (o) p.set('origin', o);
  if (q.trim()) p.set('q', q.trim());
  if (sort && sort !== 'new') p.set('sort', sort);
  p.set('limit', String(limit));
  return p.toString();
}

// tree is the folder tree as the pages draw it: verticals (those with
// anything in them, and any in keep), each with its platforms' folders
// (their sets inside) and then its sets without a platform. Each node:
// {key, label, folder, count, kids}. Sets come by name.
export function tree(folders, keep = []) {
  const out = [];
  const byName = (a, b) => a.name.localeCompare(b.name, 'pt-BR');
  for (const v of (folders && folders.verticals) || []) {
    const sets = [...(v.sets || [])].sort(byName);
    if (!v.creatives && !v.headlines && !sets.length && !keep.includes(v.id)) continue;
    const kids = [];
    const plats = [...(v.platforms || [])];
    for (const s of sets) if (s.platform && !plats.some((p) => p.id === s.platform)) plats.push({ id: s.platform, name: platformName(s.platform), creatives: 0, headlines: 0 });
    for (const p of plats) {
      const f = { vertical: v.id, platform: p.id, set: 0 };
      kids.push({ key: folderKey(f), label: p.name || platformName(p.id), folder: f, count: p.creatives,
        kids: sets.filter((s) => s.platform === p.id).map((s) => setNode(v.id, s)) });
    }
    for (const s of sets) if (!s.platform) kids.push(setNode(v.id, s));
    const f = { vertical: v.id, platform: '', set: 0 };
    out.push({ key: folderKey(f), label: v.name, folder: f, count: v.creatives, kids });
  }
  return out;
}

function setNode(vertical, s) {
  const f = { vertical, platform: s.platform || '', set: s.id };
  return { key: folderKey(f), label: s.name, folder: f, count: s.creatives, kids: [] };
}

// filterTree keeps the nodes whose name has q, with the folders above them.
export function filterTree(nodes, q) {
  const want = String(q || '').trim().toLowerCase();
  if (!want) return nodes;
  const out = [];
  for (const n of nodes) {
    const kids = filterTree(n.kids, want);
    if (n.label.toLowerCase().includes(want) || kids.length) out.push({ ...n, kids: n.label.toLowerCase().includes(want) ? n.kids : kids });
  }
  return out;
}

// sameVertical is the one vertical all the selected items are in ('' for
// none known), or null when they are in several (Mover refiles within one).
export function sameVertical(items) {
  const vs = new Set(items.map((x) => x.vertical_id).filter(Boolean));
  if (vs.size > 1) return null;
  return vs.size ? [...vs][0] : '';
}

// launchLabel is "no Launch: N anúncios", '' for none.
export function launchLabel(n) {
  if (!n) return '';
  return 'no Launch: ' + plural(n, 'anúncio', 'anúncios');
}
