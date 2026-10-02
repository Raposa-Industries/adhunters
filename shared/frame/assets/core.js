// The Frame's rules that do not touch the page: the apps, the keys that
// switch between them, where a switch lands, and how ⌘K ranks what it finds.
// frame.js draws the Frame from these; tests run them in node.

// The apps in the order the team works: find, make, launch, measure; then
// Desk, which works across them. Every app lives under its own path on one
// address (/launch/, /create/), so a link from one to another is a plain
// path. `ready` says whether the app answers at that path yet; one that
// does not shows as "em breve".
export const APPS = [
  { id: 'spy', name: 'Spy', key: 'S', about: 'Anúncios que estão funcionando', ready: true },
  { id: 'create', name: 'Create', key: 'C', about: 'Imagens e headlines', ready: true },
  { id: 'launch', name: 'Launch', key: 'L', about: 'Criar e mudar campanhas', ready: true },
  { id: 'intel', name: 'Intel', key: 'I', about: 'O que está performando', ready: true },
  { id: 'funnels', name: 'Funnels', key: 'F', about: 'Landing pages e funis', ready: true },
  { id: 'raposa', name: 'Raposa', key: 'R', about: 'Investigar cloaks', ready: false },
  { id: 'desk', name: 'Desk', key: 'D', about: 'Pedir trabalho conversando', ready: false },
];

// Apps whose paths hold Taboola's tree after the app's name
// (/launch/taboola/<account>/g/<group>/c/<campaign>): switching between
// them keeps the tail, so G then I opens the same campaign in Intel.
export const TREE_APPS = new Set(['launch', 'intel']);

export function app(id) {
  return APPS.find((a) => a.id === id) || null;
}

// apps returns APPS with `ready` overridden where the page says so (an app
// deployed beside it, or one taken down).
export function apps(ready = {}) {
  return APPS.map((a) => (a.id in ready ? { ...a, ready: !!ready[a.id] } : a));
}

// tail is what follows /<app> in a path: "/taboola/x/g/1" for
// "/launch/taboola/x/g/1", "" for "/launch/" or another app's path.
export function tail(fromApp, pathname) {
  const p = '/' + fromApp;
  if (pathname !== p && !pathname.startsWith(p + '/')) return '';
  const rest = pathname.slice(p.length);
  return rest === '/' ? '' : rest;
}

// switchTo is where going from one app to another lands: the other app's
// home, or the same object when both hold Taboola's tree.
export function switchTo(fromApp, toApp, pathname) {
  const home = '/' + toApp + '/';
  if (!TREE_APPS.has(fromApp) || !TREE_APPS.has(toApp)) return home;
  const t = tail(fromApp, pathname);
  return t.startsWith('/taboola/') ? '/' + toApp + t : home;
}

// Chord reads G-then-letter: G arms it for 1.5 s, and a letter an app is
// known by while armed names that app. Anything else disarms it. `now` is
// milliseconds, passed in so tests control time.
export class Chord {
  constructor(list = APPS, windowMs = 1500) {
    this.list = list;
    this.windowMs = windowMs;
    this.armedAt = -Infinity;
  }

  // key takes one key press (KeyboardEvent.key) and returns the app it
  // completes, or null.
  key(k, now) {
    const up = String(k).toUpperCase();
    const armed = now - this.armedAt <= this.windowMs;
    if (armed) {
      this.armedAt = -Infinity;
      const hit = this.list.find((a) => a.key === up);
      if (hit) return hit;
    }
    if (up === 'G') this.armedAt = now;
    return null;
  }
}

// typing reports whether a key press belongs to a text field, where G and
// the other shortcuts are just letters.
export function typing(target) {
  if (!target) return false;
  if (target.isContentEditable) return true;
  const tag = String(target.tagName || '').toLowerCase();
  if (tag === 'textarea' || tag === 'select') return true;
  if (tag !== 'input') return false;
  const type = String(target.type || 'text').toLowerCase();
  return !['checkbox', 'radio', 'button', 'submit', 'reset', 'range', 'color', 'file'].includes(type);
}

// fold lowercases and drops accents, so "memoria" finds "Memória".
export function fold(s) {
  return String(s ?? '').normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();
}

// score ranks one text against a query: every word of the query must be in
// it; a match at the start of the text or of a word ranks first. 0 is no match.
export function score(text, query) {
  const t = fold(text);
  const words = fold(query).split(/\s+/).filter(Boolean);
  if (!words.length) return 1;
  let s = 0;
  for (const w of words) {
    const i = t.indexOf(w);
    if (i < 0) return 0;
    if (i === 0) s += 3;
    else if (/[^a-z0-9]/.test(t[i - 1])) s += 2;
    else s += 1;
  }
  return s;
}

// rank keeps the entries whose title (or `words`) match the query, best
// first, ties in their own order.
export function rank(entries, query) {
  return entries
    .map((e, i) => ({ e, i, s: Math.max(score(e.title, query), e.words ? score(e.words, query) : 0) }))
    .filter((x) => x.s > 0)
    .sort((a, b) => b.s - a.s || a.i - b.i)
    .map((x) => x.e);
}

// marks splits a text into [{text, hit}] runs around the query's words, for
// highlighting what matched without building HTML from strings.
export function marks(text, query) {
  const src = String(text ?? '');
  const t = fold(src);
  const words = fold(query).split(/\s+/).filter(Boolean);
  const hit = new Array(src.length).fill(false);
  // fold keeps one character per character for the Latin letters the apps
  // use, so positions in t are positions in src.
  if (t.length === src.length) {
    for (const w of words) {
      for (let i = t.indexOf(w); i >= 0; i = t.indexOf(w, i + 1)) {
        for (let j = i; j < i + w.length; j++) hit[j] = true;
      }
    }
  }
  const out = [];
  for (let i = 0; i < src.length; i++) {
    const last = out[out.length - 1];
    if (last && last.hit === hit[i]) last.text += src[i];
    else out.push({ text: src[i], hit: hit[i] });
  }
  return out;
}

// initials makes the account button's two letters from a name or an email.
export function initials(who) {
  const s = String(who ?? '').trim();
  if (!s) return '?';
  const name = s.includes('@') ? s.split('@')[0].replace(/[._-]+/g, ' ') : s;
  const parts = name.split(/\s+/).filter(Boolean);
  const two = parts.length > 1 ? parts[0][0] + parts[parts.length - 1][0] : parts[0].slice(0, 2);
  return two.toUpperCase();
}
