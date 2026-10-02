// The team's library beside Novos anúncios' matrix ("Biblioteca do
// Create", Figma "Launch · Novos anúncios"): the pictures and headlines
// Create saved, by folder (a vertical's platform folder, or one set). A
// click on a picture makes it a column of the matrix, a click on a headline
// a row; a second click takes it out. Launch only reads the library,
// through its own proxy (/launch/api/library/…).
//
// Todas, Originais and Geradas filter the pictures by where they came from:
// made in Create (origin create) is GERADA; uploaded or found in Drive
// (upload, drive) is ORIGINAL, as Create's pages show them.
import { api, h, note, input, plural, store } from './lib.js';
import { MAX_HEADLINE, ORIGINS, originLabel, folderList } from './matrix.js';

const ALL = { key: '', path: ['Todas as pastas'], q: {} };

function svg(cls, d) {
  const ns = 'http://www.w3.org/2000/svg';
  const el = document.createElementNS(ns, 'svg');
  for (const [k, v] of Object.entries({ viewBox: '0 0 16 16', 'aria-hidden': 'true', class: cls })) el.setAttribute(k, v);
  for (const one of [].concat(d)) {
    const p = document.createElementNS(ns, 'path');
    p.setAttribute('d', one);
    el.append(p);
  }
  return el;
}

// libraryPanel is the panel. letterOf(sha256) is a picture's column letter
// ('' when not a column); hasHeadline(text) says whether a headline is a
// row; pickImage(creative) and pickHeadline(headline) add or take out one.
export function libraryPanel({ letterOf, hasHeadline, pickImage, pickHeadline }) {
  const search = input({ type: 'search', placeholder: 'Buscar', 'aria-label': 'Buscar na biblioteca' });
  const out = h('div');
  let folders = [ALL];
  let folder = ALL;
  let origin = '';
  const folderName = h('span', { class: 'lib-folder-name' });
  const folderMenu = h('div', { class: 'lib-folder-menu', role: 'listbox', 'aria-label': 'Pastas da biblioteca' });
  const folderBox = h('details', { class: 'lib-folder' },
    h('summary', { 'aria-label': 'Pasta da biblioteca' }, svg('lib-icon', 'M2 4.5A1.5 1.5 0 0 1 3.5 3h3l1.5 1.5h4.5A1.5 1.5 0 0 1 14 6v5.5a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 2 11.5z'),
      folderName, svg('chev', 'M4 6l4 4 4-4')),
    folderMenu);
  const originBox = h('div', { class: 'lib-origins', role: 'radiogroup', 'aria-label': 'Imagens originais ou geradas' });
  const imgCount = h('span', {}, 'Imagens');
  const hlCount = h('span', {}, 'Headlines');
  const imgs = h('div', { class: 'lib-grid' });
  const hls = h('ul', { class: 'lib-hls' });
  const hlBox = h('div', { class: 'lib-part', tabindex: -1 },
    h('div', { class: 'lib-label' }, hlCount, h('span', { class: 'faint' }, 'clique para virar uma linha')), hls,
    h('p', { class: 'faint lib-foot' }, `Headlines com mais de ${MAX_HEADLINE} letras ficam de fora: o Taboola não aceita.`));
  const el = h('div', { class: 'lib-panel-in' },
    h('div', { class: 'lib-top' }, h('h2', {}, 'Biblioteca ', h('small', {}, 'do Create')),
      h('label', { class: 'lib-search' }, svg('lib-icon', ['M7 12A5 5 0 1 0 7 2a5 5 0 0 0 0 10z', 'M10.5 10.5L14 14']), search)),
    h('div', { class: 'lib-filters' }, folderBox, originBox),
    out,
    h('div', { class: 'lib-part' }, h('div', { class: 'lib-label' }, imgCount, h('span', { class: 'faint' }, 'clique para virar uma coluna')), imgs),
    hlBox);
  let creatives = [];
  let lines = [];
  let used = {};
  let loaded = false;
  let run = 0;
  const working = new Set(); // creatives being brought in

  function drawFolder() {
    const path = folder.path;
    folderName.replaceChildren(...(path.length > 1 ? [h('span', { class: 'faint' }, path.slice(0, -1).join(' › ') + ' › '), h('b', {}, path[path.length - 1])] : [h('b', {}, path[0])]));
    folderMenu.replaceChildren(...folders.map((f) => h('button', { type: 'button', role: 'option', class: 'lib-folder-opt' + (f.set ? ' set' : '') + (f.key === folder.key ? ' on' : ''), 'aria-selected': String(f.key === folder.key),
      onclick: () => { choose(f); folderBox.open = false; } }, f.set ? f.path[f.path.length - 1] : f.path.join(' › '))));
  }
  function drawOrigins() {
    originBox.replaceChildren(...ORIGINS.map(([v, label]) => h('button', { type: 'button', role: 'radio', class: 'lib-origin' + (origin === v ? ' on' : ''), 'aria-checked': String(origin === v),
      onclick: () => { origin = v; drawOrigins(); list(); } }, label)));
  }
  function choose(f) {
    folder = f;
    store('launch.libFolder', f.key);
    drawFolder();
    list();
  }

  async function load() {
    if (loaded) return;
    loaded = true;
    drawOrigins();
    try {
      folders = [ALL, ...folderList(await api('library/folders'))];
      const kept = store('launch.libFolder');
      if (!folder.key) folder = folders.find((f) => f.key === kept) || ALL;
      drawFolder();
    } catch (e) {
      loaded = false;
      out.replaceChildren(note('warn', e.message));
      return;
    }
    await list();
    // Then once more after a Drive pass, so what changed in Drive shows.
    try {
      folders = [ALL, ...folderList(await api('library/folders?fresh=1'))];
      folder = folders.find((f) => f.key === folder.key) || ALL;
      drawFolder();
      await list();
    } catch { /* the first lists stay */ }
  }

  async function list() {
    const mine = ++run;
    const q = new URLSearchParams({ limit: '60', ...folder.q });
    if (search.value.trim()) q.set('q', search.value.trim());
    const qh = new URLSearchParams(q);
    qh.set('limit', '200');
    if (origin) q.set('origin', origin);
    let c;
    let hd;
    try {
      [c, hd] = await Promise.all([api('library/creatives?' + q), api('library/headlines?' + qh)]);
    } catch (e) {
      if (mine === run) out.replaceChildren(note('warn', e.message));
      return;
    }
    if (mine !== run) return;
    out.replaceChildren();
    creatives = (c.creatives || []).filter((x) => !x.hidden && /^image\//.test(x.media_type || 'image/'));
    lines = (hd.headlines || []).filter((x) => !x.hidden && x.text && x.text.trim().length <= MAX_HEADLINE);
    used = {};
    draw();
    // "no Launch: N anúncios": the ads Launch made with each picture.
    const fps = creatives.map((x) => (x.sha256 || '').slice(0, 10)).filter(Boolean);
    if (fps.length) {
      try {
        used = (await api('used?creatives=' + fps.join(','))).used || {};
      } catch {
        used = {};
      }
      if (mine === run) draw();
    }
  }

  function draw() {
    imgCount.textContent = 'Imagens · ' + creatives.length;
    hlCount.textContent = 'Headlines · ' + lines.length;
    imgs.replaceChildren(...(creatives.length ? creatives.map((c) => {
      const l = letterOf(c.sha256);
      const n = used[(c.sha256 || '').slice(0, 10)] || 0;
      return h('button', { type: 'button', class: 'lib-img' + (l ? ' on' : '') + (working.has(c.id) ? ' busy' : ''), 'aria-pressed': String(!!l),
        title: l ? 'Tirar a coluna ' + l : 'Virar uma coluna', onclick: () => image(c) },
      h('span', { class: 'lib-pic' }, h('img', { src: '/launch/api/library/thumb?id=' + c.id, alt: '', loading: 'lazy' }),
        l ? h('span', { class: 'lib-letter' }, l) : null,
        h('span', { class: 'lib-origin-tag', title: c.ai_label === 'ai' ? 'Marcada como feita com IA na biblioteca' : null }, originLabel(c))),
      h('span', { class: 'lib-name' }, c.name || 'criativo ' + c.id),
      n ? h('span', { class: 'lib-used' }, 'no Launch: ' + plural(n, 'anúncio', 'anúncios')) : null);
    }) : [h('p', { class: 'faint' }, loaded ? 'Nenhuma imagem aqui.' : 'Carregando…')]));
    hls.replaceChildren(...(lines.length ? lines.map((x) => {
      const on = hasHeadline(x.text);
      return h('li', {}, h('button', { type: 'button', class: 'lib-hl' + (on ? ' on' : ''), 'aria-pressed': String(on), onclick: () => { pickHeadline(x); draw(); } },
        h('span', { class: 'lib-mark', 'aria-hidden': 'true' }, on ? svg('mark', 'M4.5 8.5l2.5 2.5 4.5-5') : svg('mark', ['M8 4.5v7', 'M4.5 8h7'])),
        h('span', { class: 'lib-text' }, x.text), h('span', { class: 'mono faint' }, String(x.text.trim().length))));
    }) : [h('li', { class: 'faint' }, loaded ? 'Nenhuma headline aqui.' : '')]));
  }

  async function image(c) {
    if (working.has(c.id)) return;
    working.add(c.id);
    draw();
    try {
      await pickImage(c);
      out.replaceChildren();
    } catch (e) {
      out.replaceChildren(note('fail', `${c.name || 'criativo ' + c.id}: ${e.message}`));
    } finally {
      working.delete(c.id);
      draw();
    }
  }

  let typing = 0;
  search.addEventListener('input', () => { clearTimeout(typing); typing = setTimeout(list, 300); });
  // A click outside the folder menu closes it.
  document.addEventListener('click', (e) => { if (folderBox.open && !folderBox.contains(e.target)) folderBox.open = false; });
  drawFolder();

  return {
    el,
    load,
    draw,
    // focusHeadlines brings the headline list into view ("+ headline da
    // biblioteca").
    focusHeadlines() {
      hlBox.scrollIntoView?.({ block: 'nearest', behavior: 'smooth' });
      search.focus();
    },
    // open shows one set (Create links /launch/new?set=<id>) and returns its
    // creatives and headlines.
    async open(id) {
      const d = await api('library/set?id=' + encodeURIComponent(id));
      const key = 's:' + id;
      let f = folders.find((x) => x.key === key);
      if (!f) {
        f = { key, path: [d.set?.name || 'Pasta ' + id], q: { set: String(id) }, set: true };
        folders.push(f);
      }
      folder = f;
      drawFolder();
      list();
      return { creatives: (d.creatives || []).filter((c) => !c.hidden), headlines: (d.headlines || []).filter((x) => !x.hidden && x.text && x.text.trim().length <= MAX_HEADLINE) };
    },
  };
}
