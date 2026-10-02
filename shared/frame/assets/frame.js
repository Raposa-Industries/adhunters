// The Frame: the shell every AdHunters app sits in (Command Frame, chosen
// 2026-09-28). A top bar with the app selector (232 px, the width of the
// filters under it), the app's page tabs, ⌘K search and the account; a left
// column that holds only filters and folds away; G then a letter to switch
// apps. Apps differ by name and glyph, never by colour.
//
// A page loads frame.css, keeps its own content in <main> (and its filters,
// if it has any, in <aside data-frame="filters">), and calls:
//
//   import { mountFrame } from './_frame/frame.js';
//   mountFrame({
//     app: 'launch',
//     tabs: [{ id: 'campaigns', label: 'Campanhas', href: '/launch/' }, …],
//     active: 'campaigns',
//     user: 'mari@example.com',           // optional: the account button
//     ready: { intel: true },             // optional: apps up beside this one
//     search: async (q) => [{ title, sub, href, img }],  // optional: ⌘K results
//     searchLabel: 'Buscar campanhas, anúncios, apps',
//   });
//
// Nothing here loads from another site; the fonts come from ./fonts.

import { apps as appList, app as findApp, Chord, typing, rank, marks, switchTo, initials } from './core.js';

const GLYPHS = {
  spy: '<circle cx="7" cy="7" r="4.5"/><path d="m10.4 10.4 3.6 3.6"/><circle cx="7" cy="7" r="1.4" fill="currentColor" stroke="none"/>',
  create: '<path d="M8 1.5v4M8 10.5v4M1.5 8h4M10.5 8h4"/><path d="m4 4 1.8 1.8M10.2 10.2 12 12M12 4l-1.8 1.8M5.8 10.2 4 12" stroke-width="1.2"/>',
  launch: '<path d="M2.5 10v3.5h11V10" stroke-linejoin="round"/><path d="M8 11V2.5M4.8 5.7 8 2.5l3.2 3.2" stroke-linejoin="round"/>',
  intel: '<path d="M1.5 12.5 5.5 8l3 2.5 6-7" stroke-linejoin="round"/><path d="M1.5 14.5h13" stroke-width="1.2"/>',
  funnels: '<path d="M1.5 2.5h13L9.5 8.5v5l-3-1.5V8.5z" stroke-linejoin="round"/>',
  raposa: '<path d="M2 2.5 5.5 6h5L14 2.5 13 9l-5 5-5-5z" stroke-linejoin="round"/><circle cx="6" cy="8.5" r=".9" fill="currentColor" stroke="none"/><circle cx="10" cy="8.5" r=".9" fill="currentColor" stroke="none"/>',
  desk: '<path d="M2 3h12v7.5H8l-3.5 3v-3H2z" stroke-linejoin="round"/><path d="M5 6h6M5 8h3.5" stroke-width="1.2"/>',
  search: '<circle cx="7" cy="7" r="4.6"/><path d="m10.4 10.4 3.2 3.2"/>',
  chevron: '<path d="m4.5 6.5 3.5 3.5 3.5-3.5"/>',
  filter: '<path d="M2 4h12M4.5 8h7M7 12h2"/>',
};

const SVG = 'http://www.w3.org/2000/svg';

// icon draws one 16-unit glyph. The paths are ours (above), never a
// person's text, so building them from markup is safe.
export function icon(name, size = 16) {
  const s = document.createElementNS(SVG, 'svg');
  s.setAttribute('viewBox', '0 0 16 16');
  s.setAttribute('width', size);
  s.setAttribute('height', size);
  s.setAttribute('fill', 'none');
  s.setAttribute('stroke', 'currentColor');
  s.setAttribute('stroke-width', '1.5');
  s.setAttribute('aria-hidden', 'true');
  s.innerHTML = GLYPHS[name] || '';
  return s;
}

// h builds an element: h('a', { href, class: 'x', onclick }, child, 'text').
export function h(tag, attrs = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat()) {
    if (k == null || k === false) continue;
    el.append(k instanceof Node ? k : document.createTextNode(String(k)));
  }
  return el;
}

function store(key, value) {
  try {
    if (value === undefined) return localStorage.getItem(key);
    localStorage.setItem(key, value);
  } catch {
    // Private windows and blocked storage: the Frame works, it just forgets.
  }
  return null;
}

const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || '');

export function mountFrame(opts) {
  const me = findApp(opts.app);
  if (!me) throw new Error('mountFrame: unknown app ' + opts.app);
  const list = appList(opts.ready);
  const body = document.body;
  const main = body.querySelector('main');
  const filters = body.querySelector('aside[data-frame="filters"]');
  if (!main) throw new Error('mountFrame: the page needs a <main>');

  body.classList.add('fr');

  // ---- top bar ----
  const menu = appMenu(list, me);
  const tabs = h('nav', { class: 'fr-tabs', 'aria-label': 'Páginas do ' + me.name },
    (opts.tabs || []).map((t) => h('a', {
      href: t.href,
      class: 'fr-tab',
      'aria-current': t.id === opts.active ? 'page' : null,
    }, t.label)));
  const kbd = isMac ? '⌘K' : 'Ctrl K';
  const searchBtn = h('button', { type: 'button', class: 'fr-search', 'aria-label': 'Buscar (' + kbd + ')', onclick: () => palette.open() },
    icon('search', 13), h('span', { class: 'fr-search-label' }, opts.searchLabel || 'Buscar'), h('kbd', {}, kbd));
  const filterBtn = filters
    ? h('button', { type: 'button', class: 'fr-filter-toggle', 'aria-label': 'Filtros', 'aria-expanded': 'false', onclick: () => toggleFilters() }, icon('filter', 15))
    : null;
  const account = opts.user
    ? h('span', { class: 'fr-account', title: opts.user, 'aria-label': 'Conta: ' + opts.user }, initials(opts.user))
    : null;
  const top = h('header', { class: 'fr-top' }, menu.button, tabs, h('div', { class: 'fr-spacer' }),
    h('div', { class: 'fr-tools' }, filterBtn, searchBtn, account));

  // ---- body: filters and the page ----
  const wrap = h('div', { class: 'fr-body' });
  if (filters) {
    filters.classList.add('fr-filters');
    filters.id = filters.id || 'fr-filters';
    const head = h('div', { class: 'fr-filters-head' },
      h('span', { class: 'fr-label' }, 'Filtros'),
      h('button', { type: 'button', class: 'fr-fold', 'aria-label': 'Esconder filtros ([)', title: 'Esconder filtros ([)', onclick: () => toggleFilters() }, '['));
    filters.prepend(head);
    wrap.append(filters);
    filterBtn.setAttribute('aria-controls', filters.id);
  }
  main.classList.add('fr-main');
  wrap.append(main);
  body.prepend(top, wrap, menu.panel);

  function toggleFilters(force) {
    if (!filters) return;
    const narrow = window.matchMedia('(max-width: 900px)').matches;
    if (narrow) {
      const open = force ?? !body.classList.contains('fr-filters-open');
      body.classList.toggle('fr-filters-open', open);
      filterBtn.setAttribute('aria-expanded', String(open));
      return;
    }
    const hide = force === undefined ? !body.classList.contains('fr-filters-hidden') : !force;
    body.classList.toggle('fr-filters-hidden', hide);
    store('frame.filters', hide ? 'hidden' : 'shown');
  }
  if (filters && store('frame.filters') === 'hidden') body.classList.add('fr-filters-hidden');

  // ---- ⌘K ----
  const palette = commandPalette(list, me, opts);
  body.append(palette.el);

  // ---- keys ----
  const chord = new Chord(list.filter((a) => a.ready));
  document.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      palette.open();
      return;
    }
    if (e.metaKey || e.ctrlKey || e.altKey || typing(e.target) || palette.isOpen()) return;
    if (e.key === 'Escape') {
      menu.close();
      if (body.classList.contains('fr-filters-open')) toggleFilters(false);
      return;
    }
    if (e.key === '[' && filters) {
      e.preventDefault();
      toggleFilters();
      return;
    }
    const to = chord.key(e.key, performance.now());
    if (to) {
      e.preventDefault();
      go(me, to);
    }
  });

  // ---- first visit ----
  if (store('frame.tip') !== 'seen') {
    const tip = h('aside', { class: 'fr-tip', role: 'note' },
      h('p', {}, 'Dica: ', h('kbd', {}, kbd), ' acha qualquer campanha, anúncio ou app. ',
        h('kbd', {}, 'G'), ' e depois a letra do app troca de app: ', h('kbd', {}, 'G'), ' ', h('kbd', {}, 'L'), ' abre o Launch.'),
      h('button', { type: 'button', onclick: () => { store('frame.tip', 'seen'); tip.remove(); } }, 'Entendi'));
    body.append(tip);
  }

  return { palette, toggleFilters, menu };
}

// go leaves for another app, keeping the Taboola object when both hold it.
function go(from, to) {
  if (to.id === from.id) return;
  location.assign(switchTo(from.id, to.id, location.pathname));
}

function appMenu(list, me) {
  const button = h('button', {
    type: 'button', class: 'fr-app', 'aria-haspopup': 'true', 'aria-expanded': 'false', 'aria-controls': 'fr-apps',
  }, h('span', { class: 'fr-glyph' }, icon(me.id)),
  h('span', { class: 'fr-app-text' }, h('span', { class: 'fr-app-name' }, me.name), h('small', { class: 'fr-app-about' }, me.about)),
  h('span', { class: 'fr-chev' }, icon('chevron', 16)));
  const panel = h('nav', { id: 'fr-apps', class: 'fr-apps', 'aria-label': 'Apps', hidden: true },
    list.map((a) => {
      const inner = [
        h('span', { class: 'fr-glyph' }, icon(a.id)),
        h('span', { class: 'fr-apps-text' }, h('b', {}, a.name), h('small', {}, a.ready ? a.about : a.about + ' · em breve')),
        h('span', { class: 'fr-keys' }, h('kbd', {}, 'G'), h('kbd', {}, a.key)),
      ];
      if (!a.ready) return h('span', { class: 'fr-apps-item', 'aria-disabled': 'true' }, inner);
      return h('a', {
        class: 'fr-apps-item',
        href: switchTo(me.id, a.id, location.pathname),
        'aria-current': a.id === me.id ? 'page' : null,
      }, inner);
    }));
  const close = () => { panel.hidden = true; button.setAttribute('aria-expanded', 'false'); };
  button.addEventListener('click', (e) => {
    e.stopPropagation();
    const open = panel.hidden;
    panel.hidden = !open;
    button.setAttribute('aria-expanded', String(open));
    if (open) panel.querySelector('a')?.focus();
  });
  document.addEventListener('click', (e) => { if (!panel.hidden && !panel.contains(e.target)) close(); });
  return { button, panel, close };
}

function commandPalette(list, me, opts) {
  const input = h('input', { type: 'search', class: 'fr-cmd-input', placeholder: opts.searchLabel || 'Buscar', 'aria-label': 'Buscar', autocomplete: 'off', spellcheck: 'false' });
  const results = h('div', { class: 'fr-cmd-results', role: 'listbox', 'aria-label': 'Resultados' });
  const el = h('dialog', { class: 'fr-cmd', 'aria-label': 'Buscar' }, h('div', { class: 'fr-cmd-box' }, h('div', { class: 'fr-cmd-head' }, icon('search', 16), input), results));
  let items = [];
  let at = 0;
  let seq = 0;

  const appEntries = list.filter((a) => a.ready).map((a) => ({
    kind: 'app', title: a.name, sub: a.about, words: a.name + ' ' + a.about + ' app',
    href: switchTo(me.id, a.id, location.pathname), glyph: a.id,
  }));

  function row(e, q, i) {
    const title = h('span', { class: 'fr-cmd-title' }, marks(e.title, q).map((m) => (m.hit ? h('mark', {}, m.text) : m.text)));
    return h('a', {
      href: e.href, class: 'fr-cmd-row' + (e.img ? ' fr-cmd-pic' : ''), role: 'option', id: 'fr-cmd-' + i,
      'aria-selected': i === at ? 'true' : 'false',
      onmousemove: () => select(i),
    },
    e.img ? h('img', { src: e.img, alt: '', loading: 'lazy' }) : h('span', { class: 'fr-glyph' }, icon(e.glyph || me.id)),
    h('span', { class: 'fr-cmd-text' }, title, e.sub ? h('small', {}, e.sub) : null));
  }

  function draw(q, found, waiting) {
    const pics = found.filter((e) => e.img);
    const text = found.filter((e) => !e.img);
    const appHits = rank(appEntries, q);
    items = [...pics, ...text, ...appHits]; // the order they are drawn in
    at = Math.min(at, Math.max(items.length - 1, 0));
    const kids = [];
    let i = 0;
    const sect = (label, entries, cls) => {
      if (!entries.length) return;
      kids.push(h('div', { class: 'fr-label fr-cmd-sect' }, label));
      kids.push(h('div', { class: cls }, entries.map((e) => row(e, q, i++))));
    };
    sect('Anúncios', pics, 'fr-cmd-grid');
    sect(me.name, text, 'fr-cmd-list');
    sect('Apps', appHits, 'fr-cmd-list');
    if (!kids.length) kids.push(h('p', { class: 'fr-cmd-empty' }, waiting ? 'Buscando…' : 'Nada encontrado.'));
    results.replaceChildren(...kids);
    input.setAttribute('aria-activedescendant', items.length ? 'fr-cmd-' + at : '');
  }

  function select(i) {
    at = i;
    for (const r of results.querySelectorAll('.fr-cmd-row')) r.setAttribute('aria-selected', r.id === 'fr-cmd-' + i ? 'true' : 'false');
    input.setAttribute('aria-activedescendant', 'fr-cmd-' + i);
    results.querySelector('#fr-cmd-' + i)?.scrollIntoView({ block: 'nearest' });
  }

  let timer = 0;
  input.addEventListener('input', () => {
    const q = input.value.trim();
    at = 0;
    draw(q, [], !!(opts.search && q));
    clearTimeout(timer);
    if (!opts.search || !q) return;
    const mine = ++seq;
    timer = setTimeout(async () => {
      let found = [];
      try {
        found = (await opts.search(q)) || [];
      } catch {
        found = [];
      }
      if (mine === seq) draw(q, found, false);
    }, 160);
  });
  input.addEventListener('keydown', (e) => {
    // A search field eats the first Escape to clear itself; here Escape
    // always closes.
    if (e.key === 'Escape') {
      e.preventDefault();
      el.close();
    } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!items.length) return;
      select((at + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length);
    } else if (e.key === 'Enter' && items[at]) {
      e.preventDefault();
      location.assign(items[at].href);
    }
  });
  el.addEventListener('click', (e) => { if (e.target === el) el.close(); });

  return {
    el,
    isOpen: () => el.open,
    open() {
      if (el.open) return;
      input.value = '';
      at = 0;
      draw('', [], false);
      el.showModal();
      input.focus();
    },
  };
}
