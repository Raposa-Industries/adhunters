// Create's pages, on the shared shell (/create/_frame). Criar is a chat in
// three columns (Draw Designer d587e1b829): the conversations on the left,
// the conversation in the middle with the composer under it, and the
// library on the right. A person references pictures and headlines (of the
// conversation, of the library, or from their computer), asks for more like
// them, and saves the ones they like into a library folder with tags.
// References are what a turn picks (GLOSSARY: pick). /create/ is a new
// conversation (Início), /create/s/{id} one conversation, /create/library
// the library Launch uses too (folders, originals and generated, Mover,
// Apagar, Subir originais), /create/rules the rules. Every change goes to
// /create/api; the server's worker does the paid work and this page asks
// again every 2 seconds while something is being made.

import { mountFrame, h } from '/create/_frame/frame.js';
import * as P from '/create/static/parts.js';

const TABS = [
  { id: 'chat', label: 'Criar', href: '/create/' },
  { id: 'library', label: 'Biblioteca', href: '/create/library' },
  { id: 'rules', label: 'Regras', href: '/create/rules' },
];
const LIB = '/create/library-api';
// LIBCOL_LIMIT is how many pictures and headlines the library column reads
// (its counts say "500+" past that).
const LIBCOL_LIMIT = 500;
// libNames: library creative names the page has seen, by id, so a picture
// brought from the library is called by its name.
const libNames = new Map();

// ---- talking to the server ---------------------------------------------------

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body instanceof FormData) opts.body = body;
  else if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  if (res.status === 204) return null;
  let data = null;
  try { data = await res.json(); } catch { /* not JSON */ }
  if (!res.ok) throw new Error((data && data.error) || `erro ${res.status}`);
  return data;
}

let rules = null;
async function getRules() {
  if (!rules) rules = await api('GET', '/create/api/rules');
  return rules;
}

let categories = null;
async function getCategories() {
  if (!categories) categories = (await api('GET', '/create/api/verticals')).categories || [];
  return categories;
}
async function verticalNames() {
  const out = {};
  for (const c of await getCategories()) for (const v of c.verticals) out[v.id] = v.name;
  return out;
}

// The library's folders, read again after anything changes them.
let foldersCache = null;
async function getFolders(fresh = false) {
  if (!foldersCache || fresh) foldersCache = await api('GET', `${LIB}/api/folders`).catch(() => ({ totals: {}, verticals: [] }));
  return foldersCache;
}

function remember(key, value) {
  try {
    if (value === undefined) return localStorage.getItem(key) || '';
    localStorage.setItem(key, value);
  } catch { /* private windows: the page just forgets */ }
  return '';
}

// ---- small helpers -----------------------------------------------------------

function toast(msg, kind = 'fail', action = null) {
  const n = h('div', { class: 'note toast ' + kind, role: 'status' }, msg,
    action ? h('button', { type: 'button', class: 'small', onclick: () => { n.remove(); action.run(); } }, action.label) : null);
  document.body.append(n);
  setTimeout(() => n.remove(), action ? 10000 : 6000);
}

async function run(fn) {
  try { return await fn(); } catch (e) { toast(e.message); return undefined; }
}

function money(v) { return 'US$ ' + (v || 0).toFixed(2).replace('.', ','); }

const SVGNS = 'http://www.w3.org/2000/svg';
const GLYPH = {
  folder: 'M1.5 4.5V12a1 1 0 0 0 1 1h11a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3.5h-4a1 1 0 0 0-1 1z',
  plus: 'M8 3v10M3 8h10',
  expand: 'M9.5 2.5h4v4M6.5 13.5h-4v-4M13.5 2.5 9 7M2.5 13.5 7 9',
  upload: 'M8 10.5V2.5M5 5.5l3-3 3 3M2.5 10v3.5h11V10',
  search: 'M7 11.6a4.6 4.6 0 1 0 0-9.2 4.6 4.6 0 0 0 0 9.2zM10.4 10.4l3.2 3.2',
  check: 'm3.5 8.5 3 3 6-7',
  tick: 'M4 6.5 7.2 12 12 3.8',
  caret: 'm6 4 4 4-4 4',
};
function glyph(name, size = 14) {
  const s = document.createElementNS(SVGNS, 'svg');
  s.setAttribute('viewBox', '0 0 16 16');
  s.setAttribute('width', size);
  s.setAttribute('height', size);
  s.setAttribute('fill', 'none');
  s.setAttribute('stroke', 'currentColor');
  s.setAttribute('stroke-width', '1.5');
  s.setAttribute('stroke-linecap', 'round');
  s.setAttribute('stroke-linejoin', 'round');
  s.setAttribute('aria-hidden', 'true');
  const p = document.createElementNS(SVGNS, 'path');
  p.setAttribute('d', GLYPH[name]);
  s.append(p);
  return s;
}

// originBadge is ORIGINAL or GERADA over a library picture.
function originBadge(c) {
  const orig = P.isOriginal(c);
  return h('span', { class: 'obadge ' + (orig ? 'orig' : 'gen') }, orig ? 'ORIGINAL' : 'GERADA');
}

function verticalSelect(cats, value, cls = '') {
  return h('select', { 'aria-label': 'Vertical', class: cls }, h('option', { value: '' }, 'Escolha a vertical'),
    cats.map((c) => h('optgroup', { label: c.name }, c.verticals.map((v) => h('option', { value: v.id, selected: v.id === value }, v.name)))));
}

const PLATFORMS = [{ id: 'taboola', name: 'Taboola' }, { id: 'newsbreak', name: 'NewsBreak' }];
function platformSelect(value, cls = '', none = false) {
  return h('select', { 'aria-label': 'Plataforma', class: cls },
    none ? h('option', { value: '', selected: !value }, 'Sem plataforma') : null,
    PLATFORMS.map((p) => h('option', { value: p.id, selected: p.id === (value || (none ? '' : 'taboola')) }, p.name)));
}

// swap puts alt in place of a blocked word, in its case (the server's
// checks name the word; the person chose the alternative).
function swap(text, blocked, alt) {
  const esc = blocked.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/\s+/g, '\\s+');
  const ending = /\s/.test(blocked.trim()) ? '' : '(?:s|es|ing|ed|ting|ful)?';
  return text.replace(new RegExp(`(^|[^\\p{L}\\p{N}])(${esc}${ending})(?![\\p{L}\\p{N}])`, 'giu'), (_, before, word) => {
    const letters = word.replace(/[^\p{L}]/gu, '');
    let out = alt;
    if (letters.length > 1 && letters === letters.toUpperCase()) out = alt.toUpperCase();
    else if (/^\p{Lu}/u.test(word)) out = alt.replace(/(^|\s)(\p{Ll})/gu, (m, s, c) => s + c.toUpperCase());
    return before + out;
  });
}

function dialog(title, body, cls = '') {
  const dlg = h('dialog', { class: 'sheet ' + cls },
    h('div', { class: 'spread' }, h('h2', {}, title), h('button', { type: 'button', class: 'ghost small', 'aria-label': 'Fechar', onclick: () => dlg.close() }, '×')),
    body);
  dlg.addEventListener('close', () => dlg.remove());
  dlg.addEventListener('click', (e) => { if (e.target === dlg) dlg.close(); });
  document.body.append(dlg);
  dlg.showModal();
  return dlg;
}

// popover shows content under (or above) anchor until a click outside,
// Escape, or close().
let openPop = null;
// align 'end' puts its right edge at the right of the anchor's group (a
// picture's or headline's buttons), as Salvar na biblioteca is drawn.
function popover(anchor, content, cls = '', align = 'start') {
  if (openPop) openPop.close();
  const el = h('div', { class: 'pop ' + cls, role: 'dialog' }, content);
  document.body.append(el);
  const place = () => {
    const r = anchor.getBoundingClientRect();
    const w = el.offsetWidth;
    const hgt = el.offsetHeight;
    const gap = align === 'end' ? 13 : 6;
    const right = align === 'end' ? anchor.parentElement.getBoundingClientRect().right - 2.5 : 0;
    let left = Math.min(Math.max(8, align === 'end' ? right - w : r.left), window.innerWidth - w - 8);
    let top = r.bottom + gap;
    if (top + hgt > window.innerHeight - 8 && r.top - hgt - gap > 8) top = r.top - hgt - gap;
    el.style.left = left + window.scrollX + 'px';
    el.style.top = Math.max(8, top) + window.scrollY + 'px';
  };
  place();
  const outside = (e) => { if (!el.contains(e.target) && !anchor.contains(e.target)) close(); };
  const esc = (e) => { if (e.key === 'Escape') close(); };
  function close() {
    el.remove();
    document.removeEventListener('mousedown', outside, true);
    document.removeEventListener('keydown', esc);
    if (openPop && openPop.el === el) openPop = null;
  }
  setTimeout(() => { document.addEventListener('mousedown', outside, true); document.addEventListener('keydown', esc); });
  openPop = { el, close, place };
  return openPop;
}

// ---- the folder chooser --------------------------------------------------------------

// folderTree draws tree nodes (parts.tree): a caret to open a folder, its
// name and count, a check on the chosen one. pick(node) is called on a
// click; can(node) says whether a node can be chosen.
function folderTree(nodes, { chosen, open, pick, can = () => true, counts = true, carets = true, check = true }) {
  const ul = h('ul', { class: 'ftree', role: 'tree' });
  const draw = (list, depth, into) => {
    for (const n of list) {
      const isOpen = open.has(n.key);
      const on = chosen === n.key;
      const caret = !carets ? null : n.kids.length
        ? h('button', { type: 'button', class: 'caret' + (isOpen ? ' open' : ''), 'aria-label': isOpen ? 'Fechar' : 'Abrir', onclick: (e) => {
          e.stopPropagation();
          if (isOpen) open.delete(n.key); else open.add(n.key);
          pick(null);
        } }, h('span', { class: 'tri' }))
        : h('span', { class: 'caret none' });
      into.append(h('li', { role: 'treeitem', 'aria-selected': on ? 'true' : 'false', class: (on ? 'on ' : '') + (can(n) ? '' : 'opener'), style: `--depth:${depth};--indent:${Math.max(0, depth - 1)}` },
        h('div', { class: 'frow', onclick: () => pick(n) }, caret, h('span', { class: 'ficon' }, glyph('folder')),
          h('span', { class: 'fname' }, n.label),
          on && check ? h('span', { class: 'fcheck' }, glyph('tick', 13)) : counts && n.count != null ? h('span', { class: 'fcount mono' }, n.count) : null)));
      if (isOpen) draw(n.kids, depth + 1, into);
    }
  };
  draw(nodes, 0, ul);
  return ul;
}

// openPath opens every folder above key.
function openPath(nodes, key, open) {
  const walk = (list, above) => {
    for (const n of list) {
      if (n.key === key) { above.forEach((k) => open.add(k)); return true; }
      if (walk(n.kids, [...above, n.key])) return true;
    }
    return false;
  };
  walk(nodes, []);
}

// chooser is the folder picker of Salvar, Mover and Subir originais: a
// search, the tree (sets can be chosen; the others open), and a new folder
// in the folder last opened. extra is a set-like node shown first (the
// session's own folder, made by its first save). vertical, when set, keeps
// to that vertical (Mover refiles within one).
async function chooser({ initial = '', extra = null, vertical = null, first = '', onChange = () => {} }) {
  const names = await verticalNames();
  let folders = await getFolders();
  const open = new Set();
  let chosen = initial;
  let ctx = null; // the folder a new one goes in
  const q = h('input', { type: 'search', placeholder: 'Buscar pasta', 'aria-label': 'Buscar pasta' });
  const box = h('div', { class: 'ftree-box' });
  const newRow = h('div');
  const nodesOf = () => {
    let nodes = P.tree(folders, [vertical, extra && extra.folder.vertical].filter(Boolean));
    if (vertical) nodes = nodes.filter((n) => n.folder.vertical === vertical);
    // The conversation's vertical first.
    if (first) nodes = [...nodes.filter((n) => n.folder.vertical === first), ...nodes.filter((n) => n.folder.vertical !== first)];
    if (extra) {
      // The session's folder sits in its vertical › platform.
      const v = nodes.find((n) => n.folder.vertical === extra.folder.vertical);
      const p = v && v.kids.find((k) => k.folder.platform === extra.folder.platform && !k.folder.set);
      const into = p || v;
      if (into && !into.kids.some((k) => k.key === extra.key)) into.kids.unshift(extra);
      else if (!into) nodes.unshift(extra);
    }
    return nodes;
  };
  const selected = () => {
    if (!chosen) return null;
    if (extra && chosen === extra.key) return extra;
    const find = (list) => { for (const n of list) { if (n.key === chosen) return n; const k = find(n.kids); if (k) return k; } return null; };
    return find(nodesOf());
  };
  if (initial) openPath(nodesOf(), initial, open);
  if (!initial && extra) { chosen = extra.key; openPath(nodesOf(), extra.key, open); }
  const s0 = selected();
  if (s0) ctx = s0.folder;
  else if (vertical) { ctx = { vertical, platform: '', set: 0 }; open.add('v:' + vertical); }
  function draw() {
    const nodes = P.filterTree(nodesOf(), q.value);
    if (q.value.trim()) for (const n of nodes) { open.add(n.key); n.kids.forEach((k) => open.add(k.key)); }
    box.replaceChildren(nodes.length ? folderTree(nodes, { chosen, open, counts: false, carets: false, can: (n) => !!n.folder.set || (extra && n.key === extra.key), pick: (n) => {
      if (n) {
        ctx = n.folder;
        if (n.folder.set || (extra && n.key === extra.key)) chosen = n.key;
        else if (open.has(n.key)) open.delete(n.key); else open.add(n.key);
      }
      draw();
      onChange(selected());
    } }) : h('p', { class: 'faint small-note' }, 'Nenhuma pasta com esse nome.'));
    const where = ctx && ctx.vertical ? P.folderPath(folders, { ...ctx, set: 0 }, names) : [];
    newRow.replaceChildren(where.length
      ? h('button', { type: 'button', class: 'newfolder', onclick: () => newFolder({ vertical: ctx.vertical, platform: ctx.platform }) },
        glyph('plus', 13), `Nova pasta em ${where[where.length - 1]}`)
      : h('p', { class: 'faint small-note' }, 'Abra uma vertical para criar uma pasta nela.'));
  }
  function newFolder(where) {
    const input = h('input', { type: 'text', maxlength: 120, placeholder: 'Nome da pasta', 'aria-label': 'Nome da pasta' });
    const done = async () => {
      const name = input.value.trim();
      if (!name) { draw(); return; }
      const set = await run(() => api('POST', '/create/api/library/sets', { name, vertical_id: where.vertical, platform: where.platform }));
      if (!set) return;
      folders = await getFolders(true);
      chosen = 's:' + set.id;
      ctx = { vertical: where.vertical, platform: where.platform, set: set.id };
      openPath(nodesOf(), chosen, open);
      draw();
      onChange(selected());
    };
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); done(); } if (e.key === 'Escape') { e.stopPropagation(); draw(); } });
    newRow.replaceChildren(h('div', { class: 'newfolder-form' }, input, h('button', { type: 'button', class: 'small primary', onclick: done }, 'Criar')));
    input.focus();
  }
  q.addEventListener('input', draw);
  draw();
  // Show the chosen folder when the box is shorter than the tree.
  requestAnimationFrame(() => {
    const on = box.querySelector('li.on > .frow');
    if (on && on.offsetTop + on.offsetHeight > box.clientHeight) box.scrollTop = on.offsetTop - on.offsetHeight;
  });
  return {
    el: h('div', { class: 'chooser' }, h('div', { class: 'search-in' }, glyph('search', 13), q), box, newRow),
    selected,
    folders: () => folders,
  };
}

// ---- boot --------------------------------------------------------------------

const path = location.pathname.replace(/\/+$/, '') || '/create';
const main = document.querySelector('main');
let status = { user: '', openai_why: '' };

async function boot() {
  status = await api('GET', '/create/api/status').catch(() => ({ user: '', openai_why: '' }));
  let active = 'chat';
  let render;
  let m;
  let searchLabel = 'Buscar conversas';
  let search = searchSessions;
  const aside = h('aside', { 'data-frame': 'filters' });
  if ((m = path.match(/^\/create\/s\/(\d+)$/))) {
    render = () => chatPage(aside, Number(m[1]));
  } else if (path === '/create/library') {
    active = 'library';
    searchLabel = 'Buscar na biblioteca';
    search = searchLibrary;
    render = () => libraryPage(aside);
  } else if (path === '/create/rules') {
    active = 'rules';
    render = rulesPage;
  } else {
    const q = new URLSearchParams(location.search);
    render = q.get('from') === 'spy' && /^\d+$/.test(q.get('creative') || '')
      ? () => spyStart(aside, Number(q.get('creative')))
      : () => chatPage(aside, 0);
  }
  if (active !== 'rules') document.body.prepend(aside);
  mountFrame({ app: 'create', tabs: TABS, active, user: status.user, searchLabel, search });
  main.replaceChildren();
  await render();
}

async function searchSessions(q) {
  const { sessions } = await api('GET', '/create/api/sessions?limit=200');
  const want = q.toLowerCase();
  return sessions.filter((s) => `${s.name} ${s.vertical_name}`.toLowerCase().includes(want)).slice(0, 20)
    .map((s) => ({ title: s.name, sub: `${s.vertical_name} · ${P.whenLabel(s.updated_at)}`, href: `/create/s/${s.id}` }));
}

async function searchLibrary(q) {
  const qs = `q=${encodeURIComponent(q)}&limit=12`;
  const [c, hl] = await Promise.all([api('GET', `${LIB}/api/creatives?${qs}`), api('GET', `${LIB}/api/headlines?${qs}`)]);
  return [
    ...c.creatives.map((x) => ({ title: x.name, sub: P.isOriginal(x) ? 'Original' : 'Gerada', img: `${LIB}/thumbs/${x.id}`, href: `/create/library?open=c${x.id}` })),
    ...hl.headlines.slice(0, 8).map((x) => ({ title: x.text, sub: 'Headline', href: `/create/library?tab=headlines&q=${encodeURIComponent(x.text)}` })),
  ];
}

// ---- the conversations column ------------------------------------------------------------

async function conversationsColumn(aside, currentID) {
  // The Frame's "Filtros" head is not in the design: [ still folds the
  // column (the Frame's own key).
  const list = h('nav', { class: 'sessions', 'aria-label': 'Conversas' });
  aside.replaceChildren(
    h('a', { class: 'button primary newconv', href: '/create/' }, glyph('plus', 14), 'Nova conversa'),
    h('div', { class: 'filter-group conv-list' }, h('span', { class: 'fr-label' }, 'Conversas'), list));
  async function load() {
    const { sessions } = await api('GET', '/create/api/sessions?limit=100');
    list.replaceChildren(sessions.length
      ? h('ul', {}, sessions.map((s) => h('li', {}, h('a', { href: `/create/s/${s.id}`, 'aria-current': s.id === currentID ? 'page' : null },
        h('b', {}, s.name), h('small', {}, `${s.vertical_name} · ${P.whenLabel(s.updated_at)}` + (s.making ? ' · fazendo' : ''))))))
      : h('p', { class: 'faint' }, 'Nenhuma conversa ainda.'));
  }
  await load().catch(() => list.replaceChildren(h('p', { class: 'faint' }, 'As conversas não carregaram.')));
  return { reload: () => load().catch(() => {}) };
}

// ---- the library column (Criar) ---------------------------------------------------------

// libraryColumn is the library beside the chat: a search, a folder, Todas,
// Originais, Geradas, then pictures and headlines. A click references one
// in the conversation (refs), a second click takes it out.
async function libraryColumn({ folder, isOn, toggle }) {
  const names = await verticalNames();
  let folders = await getFolders();
  const view = { folder, origin: 'all', q: '' };
  let creatives = [];
  let headlines = [];
  const q = h('input', { type: 'search', placeholder: 'Buscar imagens e headlines', 'aria-label': 'Buscar imagens e headlines' });
  let timer;
  q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => { view.q = q.value; load(); }, 300); });
  const folderBtn = h('button', { type: 'button', class: 'folder-drop' });
  folderBtn.addEventListener('click', () => {
    const open = new Set();
    const nodes = [{ key: 'root', label: 'Toda a biblioteca', folder: { ...P.ROOT }, count: folders.totals.creatives, kids: [] }, ...P.tree(folders, [view.folder.vertical].filter(Boolean))];
    openPath(nodes, P.folderKey(view.folder), open);
    const box = h('div', { class: 'ftree-box' });
    const draw = () => box.replaceChildren(folderTree(nodes, { chosen: P.folderKey(view.folder), open, pick: (n) => {
      if (!n) { draw(); return; }
      view.folder = n.folder;
      remember('create.libfolder', n.key);
      pop.close();
      drawHead();
      load();
    } }));
    draw();
    const pop = popover(folderBtn, box, 'folder-pop');
  });
  const filters = h('div', { class: 'chips libcol-chips' });
  const head = h('div', { class: 'libcol-tools' }, h('div', { class: 'search-in' }, glyph('search', 13), q), folderBtn, filters);
  const imgsHead = h('div', { class: 'libcol-sect' });
  // The design shows three rows of pictures and three headlines; the rest
  // are in the same boxes, a scroll away.
  const grid = h('div', { class: 'libgrid' });
  const hlsHead = h('div', { class: 'libcol-sect' });
  const hls = h('div', { class: 'libhls' });
  const el = h('section', { class: 'libcol', 'aria-label': 'Biblioteca' },
    h('div', { class: 'libcol-title' }, h('h2', {}, 'Biblioteca'), h('span', {}, 'a mesma do Launch')),
    head, imgsHead, grid, hlsHead, hls,
    h('p', { class: 'libcol-foot' }, 'Clique numa imagem ou headline para referenciar na conversa. Clique de novo para tirar.'));

  function drawHead() {
    const path = P.folderPath(folders, view.folder, names);
    folderBtn.replaceChildren(glyph('folder', 15), h('span', { class: 'folder-name' }, path.length ? path.join(' › ') : 'Toda a biblioteca'), h('span', { class: 'drop', 'aria-hidden': 'true' }));
    filters.replaceChildren(...[['all', 'Todas'], ['original', 'Originais'], ['generated', 'Geradas']].map(([v, l]) => {
      const i = h('input', { type: 'radio', name: 'libcol-origin', value: v, checked: view.origin === v });
      i.addEventListener('change', () => { view.origin = v; load(); });
      return h('label', { class: 'chip' }, i, h('span', {}, l));
    }));
  }

  const many = (n) => (n >= LIBCOL_LIMIT ? LIBCOL_LIMIT + '+' : String(n));
  function draw() {
    imgsHead.replaceChildren(h('span', { class: 'fr-label' }, 'Imagens'), h('span', { class: 'libcol-count' }, many(creatives.length)));
    grid.replaceChildren(...(creatives.length ? creatives.map((c) => {
      const on = isOn({ type: 'creative', id: c.id });
      return h('button', { type: 'button', class: 'libthumb' + (on ? ' on' : ''), 'aria-pressed': String(on), title: c.name,
        onclick: () => { toggle({ type: 'creative', id: c.id, name: c.name, thumb: `${LIB}/thumbs/${c.id}`, vertical_id: c.vertical_id }); draw(); } },
      h('img', { src: `${LIB}/thumbs/${c.id}`, alt: c.name, loading: 'lazy' }),
      originBadge(c),
      on ? h('span', { class: 'tick' }, glyph('tick', 12)) : null);
    }) : [h('p', { class: 'libcol-empty' }, 'Nenhuma imagem aqui.')]));
    hlsHead.replaceChildren(h('span', { class: 'fr-label' }, 'Headlines'), h('span', { class: 'libcol-count' }, many(headlines.length)));
    hls.classList.toggle('none', !headlines.length);
    hls.replaceChildren(...(headlines.length ? headlines.map((x) => {
      const on = isOn({ type: 'headline', id: x.id });
      return h('button', { type: 'button', class: 'libhl' + (on ? ' on' : ''), 'aria-pressed': String(on),
        onclick: () => { toggle({ type: 'headline', id: x.id, text: x.text, vertical_id: x.vertical_id }); draw(); } },
      h('span', { class: 'cbox' + (on ? ' on' : '') }, on ? glyph('tick', 12) : null), h('span', { class: 'libhl-t' }, x.text));
    }) : [h('p', { class: 'libcol-empty' }, 'Nenhuma headline aqui.')]));
  }

  async function load() {
    try {
      const opts = { origin: view.origin, q: view.q, limit: LIBCOL_LIMIT };
      const [c, hl] = await Promise.all([
        api('GET', `${LIB}/api/creatives?${P.listQuery(view.folder, opts)}`),
        api('GET', `${LIB}/api/headlines?${P.listQuery(view.folder, opts)}`),
      ]);
      creatives = c.creatives;
      headlines = hl.headlines;
      for (const x of creatives) libNames.set(x.id, x.name);
      draw();
    } catch (e) {
      grid.replaceChildren(h('div', { class: 'note fail' }, 'A biblioteca não respondeu: ' + e.message));
    }
  }
  drawHead();
  await load();
  return { el, redraw: draw, folder: () => view.folder, chooseFolder: () => folderBtn.click(), refresh: async () => { folders = await getFolders(true); drawHead(); await load(); } };
}

// ---- Criar: Início and a conversation --------------------------------------------------------

async function chatPage(aside, id) {
  const r = await getRules();
  const isStart = !id;
  let d = null;
  if (!isStart) {
    d = await api('GET', `/create/api/sessions/${id}`).catch((e) => { main.append(h('div', { class: 'note fail' }, e.message)); return null; });
    if (!d) return;
    remember('create.vertical', d.session.vertical_id);
  }
  const column = await conversationsColumn(aside, id);

  // refs: what the next turn starts from, in the order referenced (a
  // prompt can say "the first").
  let refs = [];
  const qs = new URLSearchParams(location.search);
  if (d && qs.get('pick')) {
    const have = new Set(d.items.map((it) => it.id));
    refs = qs.get('pick').split(',').map(Number).filter((x) => have.has(x)).map((x) => ({ type: 'item', id: x }));
  }
  if (qs.get('ref')) refs = await refsFromAddress(qs.get('ref'));
  if (qs.get('pick') || qs.get('ref')) history.replaceState(null, '', location.pathname);
  try {
    const msg = sessionStorage.getItem('create.toast');
    if (msg) { sessionStorage.removeItem('create.toast'); setTimeout(() => toast(msg), 300); }
  } catch { /* the toast is a nicety */ }

  let counts = null; // the person's own counts, once they touch them
  const sizes = status.sizes && status.sizes.length ? status.sizes : [{ id: 'landscape', label: '16:9 horizontal' }];
  let size = remember(`create.size.${id}`) || sizes[0].id;
  if (!sizes.some((x) => x.id === size)) size = sizes[0].id;
  let polling = null;
  let sending = false;
  const byID = () => new Map((d ? d.items : []).map((it) => [it.id, it]));

  const head = h('header', { class: 'conv-head' });
  const log = h('div', { class: 'chat-log', 'aria-live': 'polite' });
  const composer = h('div', { class: 'composer' });
  // Without an OpenAI key nothing can be made; the design has no place for
  // it, so it is one quiet line under the composer.
  const off = h('p', { class: 'composer-off', hidden: true }, 'Fazer está desligado: ' + (status.openai_why || ''));
  const conv = h('section', { class: 'conv' + (isStart ? ' start' : '') }, head, log, h('div', { class: 'dock' }, composer, off));
  const folders0 = await getFolders();
  let libFolder = P.parseFolderKey(remember('create.libfolder'), folders0);
  if (d) {
    // The conversation's vertical, in its platform's folder when there is one.
    const v = (folders0.verticals || []).find((x) => x.id === d.session.vertical_id);
    const hasPlat = v && v.platforms.some((p) => p.id === d.session.platform);
    libFolder = { vertical: d.session.vertical_id, platform: hasPlat ? d.session.platform : '', set: 0 };
  }
  const lib = await libraryColumn({
    folder: libFolder,
    isOn: (x) => refs.some((y) => P.refKey(y) === P.refKey(x)),
    toggle: (x) => toggleRef(x),
  });
  main.classList.add('chat-main');
  main.append(h('div', { class: 'criar' }, conv, lib.el));

  function toggleRef(x) {
    const res = P.toggleRef(refs, x, r.max_picked);
    if (res.full) { toast(`Referencie no máximo ${r.max_picked} de uma vez`); return; }
    refs = res.refs;
    if (d) draw(); else drawComposer();
    lib.redraw();
  }

  // -- names of things, as chips say them --
  function madeNumbers() {
    const out = new Map();
    let n = 0;
    for (const it of d ? d.items : []) if (it.kind === 'image' && it.origin === 'made') out.set(it.id, ++n);
    return out;
  }
  function itemName(it, made = madeNumbers()) {
    if (it.kind === 'headline') return `“${it.text}”`;
    if (it.origin === 'library') return libNames.get(Number(it.library_ref)) || `biblioteca #${it.library_ref}`;
    return { made: `gerada #${made.get(it.id) || it.id}`, upload: 'do computador', spy: 'anúncio do Spy' }[it.origin] || 'imagem';
  }
  function refView(x) {
    if (x.type === 'item') {
      const it = byID().get(x.id);
      if (!it) return null;
      return it.kind === 'image' ? { img: it.image_url, name: itemName(it) } : { text: it.text };
    }
    if (x.type === 'creative') {
      if (x.name) libNames.set(x.id, x.name);
      return { img: x.thumb || `${LIB}/thumbs/${x.id}`, name: x.name || `biblioteca #${x.id}` };
    }
    if (x.type === 'headline') return { text: x.text || '' };
    return { img: x.url, name: x.name };
  }
  // chip draws a reference; short cuts a headline to its first words, as
  // the composer shows it.
  function chip(view, onRemove, short = false) {
    if (!view) return null;
    return h('span', { class: 'rchip' + (view.text != null ? ' hl' : ''), title: view.text != null ? view.text : view.name },
      view.img ? h('img', { src: view.img, alt: '' }) : null,
      h('span', { class: 'rchip-t' }, view.text != null ? `“${short ? P.shortText(view.text) : view.text}”` : view.name),
      onRemove ? h('button', { type: 'button', class: 'x', 'aria-label': 'Tirar', onclick: onRemove }, '×') : null);
  }

  // -- the head --
  function drawHead() {
    if (!d) {
      head.replaceChildren();
      return;
    }
    const s = d.session;
    const title = h('h1', { title: 'Clique para renomear', tabindex: 0 }, s.name);
    const rename = () => {
      const input = h('input', { type: 'text', value: s.name, maxlength: 120, 'aria-label': 'Nome da conversa' });
      const done = async (ok) => {
        if (ok && input.value.trim() && input.value !== s.name) {
          const res = await run(() => api('PATCH', `/create/api/sessions/${id}`, { name: input.value }));
          if (res) { d.session = { ...d.session, ...res }; column.reload(); }
        }
        drawHead();
      };
      input.addEventListener('keydown', (e) => { if (e.key === 'Enter') done(true); if (e.key === 'Escape') done(false); });
      input.addEventListener('blur', () => done(true));
      title.replaceWith(input);
      input.focus();
      input.select();
    };
    title.addEventListener('click', rename);
    title.addEventListener('keydown', (e) => { if (e.key === 'Enter') rename(); });
    head.replaceChildren(title,
      h('span', { class: 'conv-meta' }, [s.vertical_name, P.platformName(s.platform) || 'Taboola', money(s.cost_usd)].join(' · ')));
  }

  // -- the log --
  // referenceBtn adds an item to the references (the design keeps its
  // look; the chip's x takes it out again).
  function referenceBtn(it) {
    return h('button', { type: 'button', class: 'small', onclick: () => addRef({ type: 'item', id: it.id }) }, glyph('plus', 13), 'Referenciar');
  }
  function addRef(x) {
    if (refs.some((y) => P.refKey(y) === P.refKey(x))) { prompt.focus(); return; }
    toggleRef(x);
    prompt.focus();
  }

  function picture(it, savedAt) {
    if (it.state !== 'done') {
      const retry = it.state === 'failed' && it.origin === 'made'
        ? h('button', { type: 'button', class: 'small', onclick: () => retryItem(it) }, 'Tentar de novo') : null;
      return h('figure', { class: 'pic' + (it.state === 'failed' ? ' failed' : '') },
        h('div', { class: 'wait' + (it.state === 'failed' ? ' failed' : '') }, h('span', {}, it.state === 'failed' ? (it.error || 'Falhou') : it.state === 'making' ? 'Fazendo…' : 'Na fila'), retry));
    }
    const saveBtn = h('button', { type: 'button', class: 'small', onclick: () => saveItems([it], saveBtn) }, glyph('folder', 13), 'Salvar');
    return h('figure', { class: 'pic' + (savedAt != null ? ' saved' : '') },
      h('img', { src: it.image_url, alt: it.brief || 'imagem', loading: 'lazy', class: it.height > it.width ? 'portrait' : null, onclick: () => zoom(it) }),
      savedAt != null ? h('span', { class: 'savedtag' }, glyph('tick', 10), 'SALVA' + (savedAt ? ' EM ' + savedAt.toUpperCase() : '')) : null,
      h('figcaption', { class: 'pic-tools' }, referenceBtn(it), saveBtn,
        h('button', { type: 'button', class: 'small icon', title: 'Ver grande', 'aria-label': 'Ver grande', onclick: () => zoom(it) }, glyph('expand', 13))));
  }

  // headlineRow: a saved headline shows only "✓ SALVA" (it is in the
  // library column to reference from there).
  function headlineRow(it, savedAt) {
    const len = [...it.text].length;
    return h('div', { class: 'hrow' },
      h('div', { class: 'hrow-body' }, h('span', { class: 'hl-text' }, it.text),
        (it.warnings || []).length ? h('div', { class: 'warns' }, it.warnings.map((w) => h('div', { class: 'warn' }, w.message,
          savedAt == null && w.kind === 'blocked' ? (w.alternatives || []).slice(0, 3).map((alt) => h('button', { type: 'button', class: 'small', onclick: () => edit(it, swap(it.text, w.blocked, alt)) }, 'Trocar por ' + alt)) : null))) : null),
      h('span', { class: 'count' + (len > 60 ? ' over' : '') }, len),
      ...(savedAt != null ? [h('span', { class: 'savedtag inline' }, glyph('tick', 10), 'SALVA')] : [referenceBtn(it), (() => {
        const b = h('button', { type: 'button', class: 'small', onclick: () => saveItems([it], b) }, glyph('folder', 13), 'Salvar');
        return b;
      })()]));
  }

  async function edit(it, text) {
    const res = await run(() => api('PATCH', `/create/api/items/${it.id}`, { text }));
    if (res) Object.assign(it, res);
    draw();
  }

  function results(items, saved) {
    const imgs = items.filter((it) => it.kind === 'image');
    const heads = items.filter((it) => it.kind === 'headline');
    return [
      imgs.length ? h('div', { class: 'pics' }, imgs.map((it) => picture(it, saved.has(it.id) ? saved.get(it.id) : null))) : null,
      heads.length ? h('div', { class: 'hrows' }, heads.map((it) => headlineRow(it, saved.has(it.id) ? saved.get(it.id) : null))) : null,
    ];
  }

  function you(t, at) {
    const mine = !t || !t.made_by || !status.user || t.made_by === status.user;
    return (mine ? 'Você' : t.made_by.split('@')[0]) + ' · ' + P.hhmm(at);
  }

  function draw() {
    if (!d) { drawStart(); return; }
    const saved = P.savedIn(d.saves);
    const made = madeNumbers();
    const all = byID();
    const byTurn = new Map();
    const picked = new Set(d.turns.flatMap((t) => t.picked));
    const loose = [];
    for (const it of d.items) {
      if (it.turn_id) {
        if (!byTurn.has(it.turn_id)) byTurn.set(it.turn_id, []);
        byTurn.get(it.turn_id).push(it);
      } else if (!picked.has(it.id)) loose.push(it);
    }
    // One timeline: turns, and what was added and never referenced.
    const events = [
      ...d.turns.map((t) => ({ at: t.created_at, id: t.id, turn: t })),
      ...groupLoose(loose).map((g) => ({ at: g[0].created_at, id: 0, added: g })),
    ].sort((a, b) => (a.at < b.at ? -1 : a.at > b.at ? 1 : a.id - b.id));
    const kids = [];
    if (!events.length) kids.push(h('div', { class: 'chat-empty' }, h('p', {}, 'Escreva embaixo o que quer, ou referencie imagens e headlines da biblioteca à direita.')));
    for (const ev of events) {
      if (ev.added) {
        kids.push(h('div', { class: 'msg you' }, h('div', { class: 'bubble' }, h('div', { class: 'who' }, you(null, ev.at)),
          h('p', { class: 'faint' }, ev.added.every((it) => it.origin === 'spy') ? 'Do anúncio do Spy' : 'Adicionou'))),
        h('div', { class: 'msg out' }, results(ev.added, saved)));
        continue;
      }
      const t = ev.turn;
      const its = byTurn.get(t.id) || [];
      kids.push(h('div', { class: 'msg you' }, h('div', { class: 'bubble' },
        h('div', { class: 'who' }, you(t, t.created_at)),
        t.picked.length ? h('div', { class: 'rchips' }, t.picked.map((p) => {
          const it = all.get(p);
          return it ? chip(it.kind === 'image' ? { img: it.image_url, name: itemName(it, made) } : { text: it.text }) : null;
        })) : null,
        t.prompt ? h('p', {}, t.prompt) : h('p', { class: 'faint' }, 'Variações, sem pedido escrito'))));
      const done = its.filter((it) => it.state === 'done');
      const label = t.state === 'making'
        ? P.answerLabel(t.images, t.headlines).replace('Create · ', 'Create · fazendo ') + '…'
        : P.answerLabel(done.filter((it) => it.kind === 'image').length, done.filter((it) => it.kind === 'headline').length);
      const out = h('div', { class: 'msg out' }, h('div', { class: 'answer-head' }, h('span', { class: t.state === 'making' ? 'making' : '' }, label)));
      if (t.interrupted_at) out.append(h('div', { class: 'note warn' }, 'Interrompida: o que não tinha começado não foi feito.' + (its.some((it) => it.state === 'making') ? ' A imagem que já estava sendo feita aparece aqui se chegar.' : '')));
      if (t.state === 'failed' && !t.interrupted_at && !done.length) out.append(h('div', { class: 'note fail' }, 'Não saiu nada: ' + (t.error || 'falhou')));
      else if (t.state === 'done' && t.error && !t.interrupted_at) out.append(h('div', { class: 'note warn' }, t.error));
      out.append(...results(its, saved).filter(Boolean));
      kids.push(out);
    }
    // Redrawing keeps the person where they were.
    const y = window.scrollY;
    log.replaceChildren(...kids);
    drawComposer();
    window.scrollTo({ top: y });
  }

  function groupLoose(loose) {
    const groups = [];
    for (const it of loose) {
      const last = groups[groups.length - 1];
      const after = last && d.turns.some((t) => t.created_at > last[last.length - 1].created_at && t.created_at < it.created_at);
      if (last && !after) last.push(it);
      else groups.push([it]);
    }
    return groups;
  }

  // -- Início: an empty chat that asks what to make --
  function drawStart() {
    log.replaceChildren(h('div', { class: 'hello' },
      h('h1', {}, 'O que vamos criar?'),
      h('p', { class: 'lead' }, 'Clique em imagens e headlines da biblioteca à direita para referenciar, e peça mais parecidas.', h('br'), 'Salve as que gostar numa pasta.'),
      h('div', { class: 'ideas' },
        h('button', { type: 'button', onclick: () => idea('', { images: 4, headlines: 0 }) }, 'Variações desta imagem'),
        h('button', { type: 'button', onclick: () => idea('Mesma cena, com outro público: ', { images: 4, headlines: 0 }) }, 'Mesma cena, outro público'),
        h('button', { type: 'button', onclick: () => idea('10 headlines nesse tom', { images: 0, headlines: 10 }) }, '10 headlines nesse tom'))));
    drawComposer();
  }
  function idea(text, c) {
    prompt.value = text;
    counts = c;
    drawComposer();
    prompt.focus();
    prompt.setSelectionRange(prompt.value.length, prompt.value.length);
  }

  // -- the composer --
  const prompt = h('textarea', { rows: 2, maxlength: 4000, 'aria-label': 'Pedido', placeholder: 'Peça sobre as referências. Ex.: mesma cena, mais quente, colher na mesa…' });
  prompt.addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); send(); } });
  prompt.value = remember(`create.draft.${id}`);
  prompt.addEventListener('input', () => {
    remember(`create.draft.${id}`, prompt.value);
    for (const btn of composer.querySelectorAll('.quick-edits button')) {
      const on = prompt.value.includes(btn.title);
      btn.classList.toggle('on', on);
      btn.setAttribute('aria-pressed', String(on));
    }
  });
  const upload = h('input', { type: 'file', accept: 'image/jpeg,image/png,image/gif', multiple: true, hidden: true });
  upload.addEventListener('change', () => { addFiles([...upload.files]); upload.value = ''; });
  let fileN = 0;
  function addFiles(files) {
    for (const f of files) {
      const res = P.toggleRef(refs, { type: 'file', key: ++fileN, file: f, name: f.name || 'colada.png', url: URL.createObjectURL(f) }, r.max_picked);
      if (res.full) { toast(`Referencie no máximo ${r.max_picked} de uma vez`); break; }
      refs = res.refs;
    }
    drawComposer();
  }
  // Ctrl+V of a picture (a screenshot) anywhere on the page references it.
  document.addEventListener('paste', (e) => {
    const files = [...((e.clipboardData && e.clipboardData.items) || [])]
      .filter((x) => x.kind === 'file' && /^image\/(png|jpeg|gif)$/.test(x.type)).map((x) => x.getAsFile()).filter(Boolean);
    if (!files.length || document.querySelector('dialog[open]')) return;
    e.preventDefault();
    toast(files.length === 1 ? 'Imagem colada' : `${files.length} imagens coladas`, 'ok');
    addFiles(files);
  });

  function refKinds() {
    return refs.map((x) => {
      if (x.type === 'item') return byID().get(x.id)?.kind;
      return x.type === 'headline' ? 'headline' : 'image';
    });
  }

  // pill is a choice drawn as the design's pill: its words, with the
  // select over them (see-through), so it is as wide as what it says.
  function pill(label, sel) {
    const t = h('span', { class: 'pill-t', 'aria-hidden': 'true' });
    const say = () => { t.textContent = sel.selectedOptions[0] ? sel.selectedOptions[0].textContent : ''; };
    sel.addEventListener('change', say);
    say();
    return h('label', { class: 'pill' }, h('span', { class: 'sr' }, label), t, sel);
  }

  function drawComposer() {
    if (d) {
      const all = byID();
      refs = refs.filter((x) => x.type !== 'item' || all.get(x.id)?.state === 'done');
    }
    const c = counts || P.defaultCounts(refKinds());
    const chips = refs.length ? h('div', { class: 'rchips' }, refs.map((x) => chip(refView(x), () => {
      refs = refs.filter((y) => P.refKey(y) !== P.refKey(x));
      if (x.type === 'file') URL.revokeObjectURL(x.url);
      if (d) draw(); else drawComposer();
      lib.redraw();
    }, true))) : null;
    const num = (key, max, one, many) => {
      const sel = h('select', { 'aria-label': many },
        Array.from({ length: max + 1 }, (_, n) => h('option', { value: n, selected: n === c[key] }, P.plural(n, one, many))));
      sel.addEventListener('change', () => { counts = { ...(counts || c), [key]: Number(sel.value) }; drawComposer(); });
      return pill(many, sel);
    };
    const sizeSel = h('select', { 'aria-label': 'Tamanho das imagens', title: 'Tamanho das imagens' },
      sizes.map((x) => h('option', { value: x.id, selected: x.id === size }, { landscape: '16:9', vertical: '9:16', newsbreak: 'NewsBreak' }[x.id] || x.label)));
    sizeSel.addEventListener('change', () => { size = sizeSel.value; remember(`create.size.${id}`, size); });
    composer.replaceChildren(...[
      chips,
      prompt,
      h('div', { class: 'compose-bar' },
        h('button', { type: 'button', class: 'fromdisk', onclick: () => upload.click() }, glyph('upload', 14), 'Do computador'),
        num('images', r.max_images, 'imagem', 'imagens'),
        num('headlines', r.max_headlines, 'headline', 'headlines'),
        pill('Tamanho', sizeSel),
        upload,
        h('span', { class: 'spacer' }),
        h('button', { type: 'button', class: 'primary send', disabled: sending || !!status.openai_why || (c.images + c.headlines === 0), onclick: send,
          title: status.openai_why ? 'Fazer está desligado: ' + status.openai_why : 'Ctrl+Enter' }, sending ? 'Enviando…' : 'Enviar')),
    ].filter(Boolean));
    off.hidden = !status.openai_why;
  }

  // startWhere is where a new conversation goes, as the design has no
  // place to pick it: the vertical of the first library reference, else
  // the folder open in the library column, else the one used last; the
  // platform of that folder, else the one used last, else Taboola.
  function startWhere() {
    const ref = refs.find((x) => x.vertical_id);
    const f = lib.folder();
    const vertical = (ref && ref.vertical_id) || f.vertical || remember('create.vertical');
    const platform = (f.vertical === vertical && f.platform) || remember('create.platform') || 'taboola';
    return { vertical, platform };
  }

  // itemFor turns a reference into the session's item: a library picture or
  // headline is added to the session first (once), a file is uploaded.
  async function itemFor(sessionID, items, x) {
    if (x.type === 'item') return x.id;
    if (x.type === 'file') {
      const fd = new FormData();
      fd.append('file', x.file, x.name);
      const it = await api('POST', `/create/api/sessions/${sessionID}/items`, fd);
      items.push(it);
      return it.id;
    }
    const have = P.libraryItemFor(items, x);
    if (have) return have.id;
    const body = x.type === 'creative' ? { library_creative: x.id } : { library_headline: x.id, headline: x.text };
    const it = await api('POST', `/create/api/sessions/${sessionID}/items`, body);
    items.push(it);
    return it.id;
  }

  async function send() {
    if (sending) return;
    const c = counts || P.defaultCounts(refKinds());
    const where = startWhere();
    if (!d && !where.vertical) {
      toast('Abra uma pasta da biblioteca à direita (ou referencie algo dela): a conversa fica na vertical dela.');
      lib.chooseFolder();
      return;
    }
    sending = true;
    drawComposer();
    let made = 0; // a conversation this send started
    try {
      let sid = id;
      let items = d ? d.items : [];
      if (!d) {
        const s = await api('POST', '/create/api/sessions', { name: P.sessionNameFrom(prompt.value), vertical_id: where.vertical, platform: where.platform, fresh: true });
        remember('create.vertical', where.vertical);
        remember('create.platform', where.platform);
        sid = made = s.id;
        items = [];
      }
      const picked = [];
      for (const x of refs) picked.push(await itemFor(sid, items, x));
      const body = { prompt: prompt.value, picked, images: c.images, headlines: c.headlines, size };
      await api('POST', `/create/api/sessions/${sid}/turns`, body);
      remember(`create.draft.${id}`, '');
      for (const x of refs) if (x.type === 'file') URL.revokeObjectURL(x.url);
      if (!d) { location.href = `/create/s/${sid}`; return; }
      prompt.value = '';
      refs = [];
      counts = null;
      lib.redraw();
      await refresh();
      scrollEnd();
    } catch (e) {
      toast(e.message);
      if (made) {
        // The conversation exists now: go on there.
        try { sessionStorage.setItem('create.toast', e.message); } catch { /* the toast is a nicety */ }
        location.href = `/create/s/${made}`;
        return;
      }
      if (d) await refresh();
    } finally {
      sending = false;
      drawComposer();
    }
  }

  async function retryItem(it) {
    const res = await run(() => api('POST', `/create/api/items/${it.id}/retry`));
    if (!res) return;
    Object.assign(it, res);
    await refresh();
  }

  // saveItems opens Salvar na biblioteca for items, by anchor.
  async function saveItems(items, anchor) {
    const s = d.session;
    const own = { key: 'own', label: s.name, folder: { vertical: s.vertical_id, platform: s.platform, set: s.library_set_id || 0 }, kids: [] };
    if (s.library_set_id) own.key = 's:' + s.library_set_id;
    // The folder last saved into from this conversation, else its own.
    let last = remember(`create.savefolder.${id}`);
    if (!last || last === 'own') last = s.library_set_id ? 's:' + s.library_set_id : '';
    let tags = [];
    const go = h('button', { type: 'button', class: 'primary wide' });
    const ch = await chooser({ initial: last, extra: s.library_set_id ? null : own, first: s.vertical_id, onChange: () => drawGo() });
    function chosenName() {
      const n = ch.selected();
      if (!n) return '';
      return n.key === own.key && !s.library_set_id ? s.name : n.label;
    }
    function drawGo() {
      const name = chosenName();
      go.disabled = !name;
      go.textContent = name ? `Salvar em ${name}` : 'Escolha a pasta';
    }
    // Tags as the design draws them: + tag first, then the vertical's most
    // used tags (three, one line) and the ones typed here as pills; a pill
    // ticks on or off.
    const tagBox = h('div', { class: 'tags' });
    const suggestions = h('datalist', { id: 'tag-list' });
    let shown = [];
    api('GET', `${LIB}/api/tags?vertical=${encodeURIComponent(s.vertical_id)}`).then((x) => {
      suggestions.replaceChildren(...x.tags.map((t) => h('option', { value: t.tag })));
      shown = [...new Set([...shown, ...x.tags.slice(0, 3).map((t) => t.tag)])];
      drawTags();
    }).catch(() => {});
    function drawTags() {
      const add = h('button', { type: 'button', class: 'tag add', onclick: () => {
        const input = h('input', { type: 'text', maxlength: 40, list: 'tag-list', placeholder: 'tag', 'aria-label': 'Nova tag' });
        const done = () => {
          const t = input.value.trim().replace(/^#/, '').toLowerCase();
          if (t && !tags.includes(t)) tags.push(t);
          if (t && !shown.includes(t)) shown.push(t);
          drawTags();
        };
        input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); done(); } if (e.key === 'Escape') { e.stopPropagation(); drawTags(); } });
        input.addEventListener('blur', done);
        add.replaceWith(input);
        input.focus();
      } }, '+ tag');
      tagBox.replaceChildren(add, ...shown.map((t) => {
        const on = tags.includes(t);
        return h('button', { type: 'button', class: 'tag' + (on ? ' on' : ''), 'aria-pressed': String(on), onclick: () => {
          tags = on ? tags.filter((x) => x !== t) : [...tags, t];
          drawTags();
        } }, t);
      }));
    }
    drawTags();
    go.addEventListener('click', async () => {
      const n = ch.selected();
      if (!n) return;
      const isOwn = n.key === own.key && !s.library_set_id;
      const setID = isOwn ? 0 : n.folder.set;
      const name = chosenName();
      remember(`create.savefolder.${id}`, isOwn ? 'own' : n.key);
      // A picture made here always goes with the AI label (the worker keeps
      // other pictures and headlines unlabelled).
      const v = await run(() => api('POST', `/create/api/sessions/${id}/saves`, { item_ids: items.map((it) => it.id), ai_label: 'ai', set_id: setID, set_name: name, tags }));
      if (!v) return;
      pop.close();
      toast(`Salvando em ${name}…`, 'ok');
      await refresh();
    });
    drawGo();
    const pop = popover(anchor, h('div', { class: 'savepop' }, h('h3', { class: 'pop-title' }, 'Salvar na biblioteca'), ch.el, tagBox, suggestions, go), 'save-pop', 'end');
    // The button whose popover is open shows it.
    anchor.classList.add('solid');
    const close = pop.close;
    pop.close = () => { anchor.classList.remove('solid'); close(); };
    new MutationObserver((_, obs) => { if (!pop.el.isConnected) { anchor.classList.remove('solid'); obs.disconnect(); } }).observe(document.body, { childList: true });
  }

  function zoom(it) {
    const all = byID();
    const from = it.from_ids.map((f) => all.get(f)).filter(Boolean);
    const made = madeNumbers();
    dialog(itemName(it, made), h('div', { class: 'zoom-box' },
      h('img', { src: it.image_url, alt: it.brief || '' }),
      h('dl', {},
        it.brief ? [h('dt', {}, 'O que o modelo recebeu'), h('dd', {}, it.brief)] : null,
        from.length ? [h('dt', {}, 'Feita a partir de'), h('dd', {}, h('div', { class: 'rchips' }, from.map((f) => chip(f.kind === 'image' ? { img: f.image_url, name: itemName(f, made) } : { text: f.text }))))] : null,
        h('dt', {}, 'Origem'), h('dd', {}, { made: 'Feita no Create', upload: 'Do computador', library: 'Da biblioteca', typed: 'Escrita', spy: 'De um anúncio do Spy' }[it.origin] || it.origin),
        it.cost_usd ? [h('dt', {}, 'Custo'), h('dd', {}, money(it.cost_usd))] : null),
      h('div', { class: 'actions' },
        h('button', { type: 'button', class: 'primary', onclick: (e) => { e.target.closest('dialog').close(); addRef({ type: 'item', id: it.id }); } }, '+ Referenciar'),
        h('a', { class: 'button', href: it.image_url, download: `create-${it.id}`, title: `O arquivo como foi feito, ${it.width} × ${it.height}` }, 'Baixar'))), 'wide');
  }

  function scrollEnd() { window.scrollTo({ top: document.body.scrollHeight }); }

  async function refresh() {
    const next = await api('GET', `/create/api/sessions/${id}`).catch(() => null);
    if (!next) return;
    for (const v of next.saves) {
      const was = d.saves.find((o) => o.id === v.id);
      if (v.state === 'done' && was && was.state !== 'done') { toast(`Salvo na biblioteca, em ${v.set_name}`, 'ok'); lib.refresh(); }
      if (v.state === 'failed' && was && was.state !== 'failed') toast('Salvar falhou: ' + v.error);
    }
    const finished = d.turns.some((t) => t.state === 'making' && next.turns.some((n) => n.id === t.id && n.state !== 'making'));
    d = next;
    if (finished) column.reload();
    drawHead();
    draw();
    poll();
  }

  function poll() {
    if (!d) return;
    const busy = d.turns.some((t) => t.state === 'making') || d.items.some((it) => it.state === 'making' || it.state === 'waiting') ||
      d.saves.some((v) => v.state === 'waiting' || v.state === 'saving');
    clearTimeout(polling);
    if (busy) polling = setTimeout(refresh, 2000);
  }

  drawHead();
  draw();
  poll();
  if (d) scrollEnd();
  prompt.focus();
}

// refsFromAddress reads /create/?ref=c12,h5 (Referenciar no Criar), with
// the names and texts the library page left for it.
async function refsFromAddress(s) {
  const want = P.parseRefs(s);
  let stash = [];
  try { stash = JSON.parse(sessionStorage.getItem('create.refs') || '[]'); sessionStorage.removeItem('create.refs'); } catch { stash = []; }
  const out = [];
  for (const x of want) {
    const got = stash.find((y) => P.refKey(y) === P.refKey(x));
    if (got) { out.push(got); continue; }
    if (x.type === 'creative') {
      const c = await api('GET', `${LIB}/api/creatives/${x.id}`).catch(() => null);
      if (c) out.push({ type: 'creative', id: c.id, name: c.name, thumb: `${LIB}/thumbs/${c.id}`, vertical_id: c.vertical_id });
    } else {
      const x2 = await api('GET', `${LIB}/api/headlines/${x.id}`).catch(() => null);
      if (x2) out.push({ type: 'headline', id: x2.id, text: x2.text, vertical_id: x2.vertical_id });
    }
  }
  return out;
}

// spyStart opens a conversation from a Spy ad (/create/?from=spy&creative=<id>):
// straight away when Spy knows its vertical, else after the person picks one.
async function spyStart(aside, creative) {
  await conversationsColumn(aside, 0);
  const got = await api('GET', `/create/api/spy/${creative}`).catch((e) => { main.append(h('div', { class: 'note fail' }, e.message)); return null; });
  if (!got) return;
  const open = async (body) => {
    const r = await run(() => api('POST', `/create/api/spy/${creative}/session`, { platform: remember('create.platform') || 'taboola', ...body }));
    if (!r) return;
    if (r.warning) { try { sessionStorage.setItem('create.toast', r.warning); } catch { /* the toast is a nicety */ } }
    location.replace(`/create/s/${r.session.id}?pick=${r.picked.join(',')}`);
  };
  const ad = got.ad;
  const card = h('div', { class: 'start spy-start' },
    h('h1', {}, 'Criar variações'),
    h('p', { class: 'lead' }, 'Do anúncio do Spy' + (ad.brand ? ` de ${ad.brand}` : '') + '. A imagem e a headline dele entram na conversa já referenciadas; é só escrever o que mudar.'),
    h('div', { class: 'spy-ad' },
      ad.image_url ? h('img', { src: ad.image_url, alt: '', referrerpolicy: 'no-referrer' }) : null,
      ad.headline ? h('p', { class: 'hl-text' }, ad.headline) : h('p', { class: 'faint' }, 'Sem headline.')));
  main.append(card);
  if (got.vertical_name) {
    card.append(h('p', { class: 'making' }, `Abrindo a conversa em ${got.vertical_name}`));
    await open({});
    return;
  }
  const cats = await getCategories();
  const sel = verticalSelect(cats, '');
  const name = h('input', { type: 'text', value: got.name, maxlength: 120, 'aria-label': 'Nome da conversa' });
  const platform = platformSelect(remember('create.platform'));
  card.append(h('form', { class: 'new-session', onsubmit: (e) => {
    e.preventDefault();
    if (!sel.value) { toast('Escolha a vertical'); sel.focus(); return; }
    remember('create.platform', platform.value);
    open({ vertical_id: sel.value, name: name.value, platform: platform.value });
  } }, h('p', { class: 'hint' }, 'O Spy ainda não sabe a vertical deste anúncio.'), sel, platform, name,
  h('button', { type: 'submit', class: 'primary' }, 'Abrir conversa')));
  sel.focus();
}

// ---- Biblioteca ------------------------------------------------------------------------------

async function libraryPage(aside) {
  const cats = await getCategories();
  const names = await verticalNames();
  let folders = await getFolders(true);
  const qs = new URLSearchParams(location.search);
  const view = {
    folder: P.parseFolderKey(qs.get('f') || remember('create.libpage.folder'), folders),
    origin: 'all',
    tab: qs.get('tab') === 'headlines' ? 'headlines' : 'images',
    sort: remember('create.libpage.sort') || 'new',
    q: qs.get('q') || '',
    launch: 'all',
  };
  const openKeys = new Set();
  openPath(P.tree(folders), P.folderKey(view.folder), openKeys);
  let creatives = [];
  let headlines = [];
  let uses = { available: false, ads: {} };
  // selected: 'c:<id>' or 'h:<id>' → the creative or headline.
  const selected = new Map();
  const body = h('div', { class: 'libbody' });
  const top = h('div', { class: 'lib-top' });
  const bar = h('div', { class: 'selbar', hidden: true });
  main.classList.add('lib-main');
  main.append(top, body, bar);

  // -- the left column --
  function drawAside() {
    const t = folders.totals || {};
    const origin = h('ul', { class: 'origin-list' }, [['all', 'Tudo', t.creatives, ''], ['original', 'Originais', t.original, 'orig'], ['generated', 'Geradas', t.generated, 'gen']].map(([v, l, n, dot]) =>
      h('li', {}, h('button', { type: 'button', class: 'orow' + (view.origin === v ? ' on' : ''), 'aria-pressed': String(view.origin === v), onclick: () => { view.origin = v; drawAside(); load(); } },
        dot ? h('span', { class: 'dot ' + dot }) : null, h('span', { class: 'fname' }, l), h('span', { class: 'fcount' }, n ?? '')))));
    const nodes = P.tree(folders, [view.folder.vertical].filter(Boolean));
    const treeEl = folderTree(nodes, { chosen: P.folderKey(view.folder), open: openKeys, check: false, pick: (n) => {
      if (!n) { drawAside(); return; }
      if (n.kids.length) openKeys.add(n.key);
      setFolder(n.folder);
    } });
    const launch = uses.available ? h('div', { class: 'chips lib-launch' }, [['all', 'Todas'], ['used', 'Em anúncios'], ['never', 'Nunca usadas']].map(([v, l]) => {
      const i = h('input', { type: 'radio', name: 'launch-use', value: v, checked: view.launch === v });
      i.addEventListener('change', () => { view.launch = v; draw(); });
      return h('label', { class: 'chip' }, i, h('span', {}, l));
    })) : null;
    aside.replaceChildren(...[
      h('div', { class: 'filter-group lib-origin' }, h('span', { class: 'fr-label' }, 'Origem'), origin),
      h('div', { class: 'filter-group' }, h('div', { class: 'spread' }, h('span', { class: 'fr-label' }, 'Pastas'),
        h('button', { type: 'button', class: 'linkish', onclick: () => newFolderDialog() }, '+ Nova')), treeEl),
      launch ? h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'No Launch'), launch) : null].filter(Boolean));
  }

  function setFolder(f) {
    view.folder = { ...f };
    remember('create.libpage.folder', P.folderKey(view.folder));
    selected.clear();
    drawAside();
    load();
  }

  // -- the head --
  function drawTop() {
    const path = P.folderPath(folders, view.folder, names);
    const crumbs = h('div', { class: 'crumbs' }, h('a', { href: '#', onclick: (e) => { e.preventDefault(); setFolder(P.ROOT); } }, 'Biblioteca'),
      path.flatMap((p, i) => {
        const f = i === 0 ? { vertical: view.folder.vertical, platform: '', set: 0 }
          : i === 1 && view.folder.platform ? { vertical: view.folder.vertical, platform: view.folder.platform, set: 0 } : view.folder;
        const last = i === path.length - 1;
        return [h('span', { class: 'sep' }, '›'), last ? h('b', { 'aria-current': 'page' }, p) : h('a', { href: '#', onclick: (e) => { e.preventDefault(); setFolder(f); } }, p)];
      }));
    const up = h('input', { type: 'file', accept: 'image/jpeg,image/png,image/gif,image/webp', multiple: true, hidden: true });
    let upTo = null;
    up.addEventListener('change', () => { const files = [...up.files]; up.value = ''; uploadOriginals(files, upTo); });
    const subir = h('button', { type: 'button', class: 'primary', onclick: async () => {
      if (view.folder.set) { upTo = view.folder; up.click(); return; }
      const f = await pickFolder({ title: 'Subir originais para…', action: 'Escolher arquivos', vertical: view.folder.vertical || null,
        initialCtx: view.folder });
      if (f) { upTo = f; up.click(); }
    } }, glyph('upload', 14), 'Subir originais');
    const tabs = h('div', { class: 'segmented tabs' }, [['images', 'Imagens', creatives.length], ['headlines', 'Headlines', headlines.length]].map(([v, l, n]) => {
      const i = h('input', { type: 'radio', name: 'libtab', value: v, checked: view.tab === v });
      i.addEventListener('change', () => { view.tab = v; selected.clear(); draw(); });
      return h('label', {}, i, h('span', {}, h('b', {}, l), h('span', { class: 'tabn' }, n >= 500 ? '500+' : n)));
    }));
    const sortSel = h('select', { 'aria-label': 'Ordem' }, [['new', 'Mais recentes'], ['old', 'Mais antigas'], ['name', 'Por nome']].map(([v, l]) => h('option', { value: v, selected: view.sort === v }, l)));
    sortSel.addEventListener('change', () => { view.sort = sortSel.value; remember('create.libpage.sort', view.sort); load(); });
    const sort = h('label', { class: 'sort' }, h('span', { 'aria-hidden': 'true' }, sortSel.selectedOptions[0].textContent), h('span', { class: 'drop', 'aria-hidden': 'true' }), sortSel);
    top.replaceChildren(...[crumbs,
      h('div', { class: 'page-head' },
        h('div', {}, h('h1', {}, path.length ? path[path.length - 1] : 'Biblioteca'),
          h('p', { class: 'lead' }, 'As originais que o time sobe e as geradas salvas do Criar. O Launch escolhe as imagens e headlines dos anúncios daqui.')),
        h('div', { class: 'actions' }, h('button', { type: 'button', onclick: () => newFolderDialog() }, glyph('plus', 14), 'Nova pasta'), subir, up)),
      view.q ? h('div', { class: 'chips' }, h('span', { class: 'qchip' }, `Busca: ${view.q}`, h('button', { type: 'button', class: 'x', 'aria-label': 'Limpar busca', onclick: () => { view.q = ''; load(); } }, '×'))) : null,
      h('div', { class: 'spread tabs-row' }, tabs, sort)].filter(Boolean));
  }

  // -- the list --
  function draw() {
    drawTop();
    if (view.tab === 'images') {
      const list = creatives.filter((c) => {
        const n = uses.ads[c.sha256.slice(0, 10)] || 0;
        return view.launch === 'all' || (view.launch === 'used' ? n > 0 : n === 0);
      });
      body.replaceChildren(list.length ? h('div', { class: 'cards' }, list.map((c) => {
        const k = 'c:' + c.id;
        const on = selected.has(k);
        const n = uses.ads[c.sha256.slice(0, 10)] || 0;
        const box = h('input', { type: 'checkbox', checked: on, 'aria-label': 'Selecionar ' + c.name });
        box.addEventListener('change', () => { if (box.checked) selected.set(k, c); else selected.delete(k); draw(); });
        return h('figure', { class: 'card' + (on ? ' on' : '') },
          h('div', { class: 'card-pic' },
            h('img', { src: `${LIB}/thumbs/${c.id}`, alt: c.name, loading: 'lazy', onclick: () => showCreative(c) }),
            h('label', { class: 'card-check' + (on ? ' on' : '') }, box, on ? glyph('tick', 12) : null),
            originBadge(c)),
          h('figcaption', {}, h('div', { class: 'spread' }, h('span', { class: 'cname' }, c.name), h('span', { class: 'cdate' }, P.ddmm(c.created_at))),
            n ? h('div', { class: 'inlaunch' }, P.launchLabel(n)) : null));
      })) : h('div', { class: 'empty' }, view.origin !== 'all' || view.launch !== 'all' || view.q ? 'Nada com esses filtros.' : 'Nenhuma imagem nesta pasta ainda.'));
    } else {
      body.replaceChildren(headlines.length ? h('div', { class: 'hlist' }, headlines.map((x) => {
        const k = 'h:' + x.id;
        const on = selected.has(k);
        const box = h('input', { type: 'checkbox', checked: on, 'aria-label': 'Selecionar headline' });
        box.addEventListener('change', () => { if (box.checked) selected.set(k, x); else selected.delete(k); draw(); });
        const len = [...x.text].length;
        return h('label', { class: 'hline' + (on ? ' on' : '') }, box, h('span', { class: 'hl-text' }, x.text),
          h('span', { class: 'tags' }, (x.tags || []).map((t) => h('span', { class: 'tag' }, t))),
          h('span', { class: 'count' + (len > 60 ? ' over' : '') }, len), h('span', { class: 'mono faint' }, P.ddmm(x.created_at)));
      })) : h('div', { class: 'empty' }, view.q ? 'Nada com essa busca.' : 'Nenhuma headline nesta pasta ainda.'));
    }
    drawBar();
  }

  function drawBar() {
    bar.hidden = selected.size === 0;
    if (!selected.size) return;
    bar.replaceChildren(h('b', {}, P.plural(selected.size, 'selecionada', 'selecionadas')), h('span', { class: 'spacer' }),
      h('button', { type: 'button', class: 'primary small', onclick: referenceInCreate }, glyph('plus', 13), 'Referenciar no Criar'),
      h('button', { type: 'button', class: 'small', onclick: refile }, glyph('folder', 13), 'Mover'),
      h('button', { type: 'button', class: 'small', onclick: hide }, 'Apagar'),
      h('button', { type: 'button', class: 'selbar-x', 'aria-label': 'Limpar seleção', onclick: () => { selected.clear(); draw(); } }, '×'));
  }

  async function load() {
    try {
      const opts = { origin: view.origin, q: view.q, sort: view.sort, limit: 500 };
      const [c, hl] = await Promise.all([
        api('GET', `${LIB}/api/creatives?${P.listQuery(view.folder, opts)}`),
        api('GET', `${LIB}/api/headlines?${P.listQuery(view.folder, opts)}`),
      ]);
      creatives = c.creatives;
      headlines = hl.headlines;
      draw();
      const shas = creatives.map((x) => x.sha256.slice(0, 10));
      if (shas.length) {
        const was = uses.available;
        uses = await api('GET', `/create/api/launch-use?sha=${shas.join(',')}`).catch(() => ({ available: false, ads: {} }));
        if (uses.available !== was) drawAside();
        draw();
      }
    } catch (e) {
      body.replaceChildren(h('div', { class: 'note fail' }, 'A biblioteca não respondeu: ' + e.message));
    }
  }

  async function reloadAll() {
    folders = await getFolders(true);
    drawAside();
    await load();
  }

  // -- actions --
  function referenceInCreate() {
    const refs = [...selected.entries()].map(([k, x]) => (k.startsWith('c:')
      ? { type: 'creative', id: x.id, name: x.name, thumb: `${LIB}/thumbs/${x.id}`, vertical_id: x.vertical_id }
      : { type: 'headline', id: x.id, text: x.text, vertical_id: x.vertical_id })).slice(0, 8);
    try { sessionStorage.setItem('create.refs', JSON.stringify(refs)); } catch { /* the address still carries the pictures */ }
    location.href = '/create/?ref=' + P.refsParam(refs);
  }

  async function refile() {
    const items = [...selected.values()];
    const vert = P.sameVertical(items);
    if (vert === null) { toast('Selecione itens de uma vertical só para mover: uma pasta é de uma vertical.'); return; }
    const to = await pickFolder({ title: `Mover ${P.plural(items.length, 'item', 'itens')} para…`, action: 'Mover', vertical: vert || view.folder.vertical || null, initialCtx: view.folder });
    if (!to) return;
    let ok = 0;
    for (const [k, x] of selected) {
      const res = await run(() => api('PATCH', `/create/api/library/${k.startsWith('c:') ? 'creatives' : 'headlines'}/${x.id}`, { refile_to: to.set }));
      if (res) ok++;
    }
    if (ok) toast(`${P.plural(ok, 'item movido', 'itens movidos')} para ${P.folderPath(await getFolders(), to, names).pop()}`, 'ok');
    selected.clear();
    await reloadAll();
  }

  async function hide() {
    const list = [...selected.entries()];
    const yes = h('button', { type: 'button', class: 'primary' }, 'Apagar');
    const dlg = dialog(`Apagar ${P.plural(list.length, 'item', 'itens')}?`, h('div', { class: 'stack' },
      h('p', { class: 'muted' }, 'Saem da biblioteca, do Criar e do Launch. Nada se perde: o arquivo fica no Drive e dá para desfazer.'),
      h('div', { class: 'actions' }, yes, h('button', { type: 'button', onclick: () => dlg.close() }, 'Cancelar'))));
    yes.addEventListener('click', async () => {
      dlg.close();
      const done = [];
      for (const [k, x] of list) {
        const kind = k.startsWith('c:') ? 'creatives' : 'headlines';
        if (await run(() => api('PATCH', `/create/api/library/${kind}/${x.id}`, { hidden: true }))) done.push([kind, x.id]);
      }
      selected.clear();
      await reloadAll();
      if (done.length) toast(`${P.plural(done.length, 'item apagado', 'itens apagados')}.`, 'ok', { label: 'Desfazer', run: async () => {
        for (const [kind, xid] of done) await run(() => api('PATCH', `/create/api/library/${kind}/${xid}`, { hidden: false }));
        await reloadAll();
      } });
    });
  }

  async function uploadOriginals(files, to) {
    if (!files.length || !to) return;
    let ok = 0;
    for (const f of files) {
      const fd = new FormData();
      fd.append('file', f, f.name);
      if (to.set) fd.append('set_id', String(to.set));
      fd.append('vertical_id', to.vertical);
      if (await run(() => api('POST', '/create/api/library/creatives', fd))) ok++;
    }
    if (ok) toast(`${P.plural(ok, 'original subida', 'originais subidas')}. O Drive recebe em seguida.`, 'ok');
    if (to.set && P.folderKey(view.folder) !== P.folderKey(to)) setFolder(to);
    await reloadAll();
  }

  // pickFolder asks for a set (with a new one possible) and resolves to its
  // folder, or null.
  function pickFolder({ title, action, vertical = null, initialCtx = null }) {
    return new Promise(async (resolve) => {
      let picked = null;
      const go = h('button', { type: 'button', class: 'primary', disabled: true }, action);
      const initial = initialCtx && initialCtx.set ? P.folderKey(initialCtx) : '';
      const ch = await chooser({ initial, vertical, onChange: (n) => { go.disabled = !(n && n.folder.set); go.textContent = n && n.folder.set ? `${action}: ${n.label}` : action; } });
      if (ch.selected()) { go.disabled = false; go.textContent = `${action}: ${ch.selected().label}`; }
      const dlg = dialog(title, h('div', { class: 'stack' }, ch.el, h('div', { class: 'actions' }, go)), 'narrow');
      go.addEventListener('click', () => { const n = ch.selected(); if (n && n.folder.set) { picked = n.folder; dlg.close(); } });
      dlg.addEventListener('close', async () => {
        folders = await getFolders();
        drawAside();
        resolve(picked);
      });
    });
  }

  function newFolderDialog() {
    const vs = verticalSelect(cats, view.folder.vertical || remember('create.vertical'));
    const ps = platformSelect(view.folder.vertical ? view.folder.platform : 'taboola', '', true);
    const name = h('input', { type: 'text', maxlength: 120, placeholder: 'Nome da pasta', 'aria-label': 'Nome da pasta' });
    const dlg = dialog('Nova pasta', h('form', { class: 'stack', onsubmit: async (e) => {
      e.preventDefault();
      if (!vs.value) { toast('Escolha a vertical'); return; }
      const set = await run(() => api('POST', '/create/api/library/sets', { name: name.value, vertical_id: vs.value, platform: ps.value }));
      if (!set) return;
      dlg.close();
      folders = await getFolders(true);
      const f = { vertical: vs.value, platform: ps.value, set: set.id };
      openPath(P.tree(folders), P.folderKey(f), openKeys);
      setFolder(f);
    } }, h('label', { class: 'field' }, 'Vertical', vs), h('label', { class: 'field' }, 'Plataforma', ps), h('label', { class: 'field' }, 'Nome', name),
    h('p', { class: 'hint' }, 'Vira a pasta vertical › plataforma › nome no Drive quando algo for salvo nela.'),
    h('div', { class: 'actions' }, h('button', { type: 'submit', class: 'primary' }, 'Criar pasta'))), 'narrow');
    name.focus();
  }

  function showCreative(c) {
    const n = uses.ads[c.sha256.slice(0, 10)] || 0;
    const setNames = (c.set_ids || []).map((sid) => { const s = P.findSet(folders, sid); return s ? s.name : null; }).filter(Boolean);
    const tagsBox = h('div', { class: 'tags' });
    let tags = [...(c.tags || [])];
    const change = async (body) => {
      const res = await run(() => api('PATCH', `/create/api/library/creatives/${c.id}`, body));
      if (res) { tags = res.tags || []; c.tags = tags; drawTags(); }
    };
    function drawTags() {
      tagsBox.replaceChildren(...tags.map((t) => h('span', { class: 'tag' }, t, h('button', { type: 'button', class: 'x', 'aria-label': 'Tirar ' + t, onclick: () => change({ remove_tags: [t] }) }, '×'))),
        h('button', { type: 'button', class: 'tag add', onclick: () => {
          const input = h('input', { type: 'text', maxlength: 40, placeholder: 'tag', 'aria-label': 'Nova tag' });
          const add = () => { if (input.value.trim()) change({ add_tags: [input.value] }); else drawTags(); };
          input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); add(); } if (e.key === 'Escape') { e.stopPropagation(); drawTags(); } });
          tagsBox.lastChild.replaceWith(input);
          input.focus();
        } }, '+ tag'));
    }
    drawTags();
    const dlg = dialog(c.name, h('div', { class: 'zoom-box' },
      h('img', { src: `${LIB}/files/${c.id}`, alt: c.idea || c.name }),
      h('dl', {},
        h('dt', {}, 'Origem'), h('dd', {}, (P.isOriginal(c) ? 'Original · ' : 'Gerada · ') + ({ create: 'salva do Criar', upload: 'subida pelo time', drive: 'posta no Drive' }[c.origin] || c.origin)),
        h('dt', {}, 'Pasta'), h('dd', {}, setNames.join(', ') || '—'),
        h('dt', {}, 'Selo de IA'), h('dd', {}, { ai: 'Feita com IA', not_ai: 'Não é IA', unset: 'Não marcado' }[c.ai_label] || c.ai_label),
        c.idea ? [h('dt', {}, 'Ideia'), h('dd', {}, c.idea)] : null,
        h('dt', {}, 'Tamanho'), h('dd', {}, `${c.width} × ${c.height} · ${(c.bytes / 1048576).toFixed(1).replace('.', ',')} MB · ${P.ddmm(c.created_at)}`),
        uses.available ? [h('dt', {}, 'No Launch'), h('dd', {}, n ? P.plural(n, 'anúncio', 'anúncios') : 'nunca usada')] : null,
        h('dt', {}, 'Tags'), h('dd', {}, tagsBox)),
      h('div', { class: 'actions' },
        h('button', { type: 'button', class: 'primary', onclick: () => { selected.clear(); selected.set('c:' + c.id, c); dlg.close(); referenceInCreate(); } }, '+ Referenciar no Criar'),
        h('a', { class: 'button', href: `${LIB}/files/${c.id}`, download: c.name }, 'Baixar'))), 'wide');
  }

  drawAside();
  await load();
  const open = qs.get('open');
  if (open && /^c\d+$/.test(open)) {
    const c = creatives.find((x) => x.id === Number(open.slice(1))) || await api('GET', `${LIB}/api/creatives/${open.slice(1)}`).catch(() => null);
    if (c) showCreative(c);
  }
  if (qs.toString()) history.replaceState(null, '', location.pathname);
}

// ---- the rules -----------------------------------------------------------------

async function rulesPage() {
  const r = await getRules();
  main.classList.add('rules-main');
  main.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Regras'),
    h('p', { class: 'lead' }, 'O que o Create segue ao escrever e o que ele marca. As regras do Taboola só avisam: quem decide é você.'))));
  main.append(h('section', { class: 'panel' }, h('h2', {}, 'Sempre'),
    h('ul', {},
      h('li', {}, 'Imagens e headlines feitas com a OpenAI. Headlines sempre em inglês.'),
      h('li', {}, 'Pessoas espontâneas, sem olhar para a câmera, foto com cara de real, a menos que o pedido diga outra coisa.'),
      h('li', {}, 'Sem texto, logo, antes e depois, celebridades ou close de partes do corpo nas imagens.'),
      h('li', {}, 'Headlines de 34 a 45 caracteres (nunca mais de 60), sem palavras em maiúsculas, sem "!!", sintomas e não doenças, sem promessa de cura, sem valores.'),
      h('li', {}, `Até ${r.max_images} imagens e ${r.max_headlines} headlines por pedido, a partir de até ${r.max_picked} escolhidas.`),
      h('li', {}, 'Nada vai para a biblioteca até você salvar. Imagens feitas aqui vão com o selo de IA; originais sobem sem selo.'))));
  main.append(h('section', { class: 'panel' }, h('h2', {}, `Palavras que o Taboola já bloqueou para o time (${r.blocked.length})`),
    h('ul', { class: 'words' }, r.blocked.map((b) => h('li', {}, h('b', {}, b.text),
      b.description ? h('span', { class: 'muted' }, '· também em descrições') : null,
      b.alternatives && b.alternatives.length ? h('span', { class: 'muted' }, '→ ' + b.alternatives.join(', ')) : null)))));
}

boot().catch((e) => { main.replaceChildren(h('div', { class: 'note fail' }, 'Não carregou: ' + e.message)); });
