// The team's library beside Novos anúncios' matrix ("Biblioteca do
// Create"): the pictures and headlines Create saved, by folder (a library
// set is one folder in Drive). A click on a picture makes it a column of the
// matrix, a click on a headline a row; a second click takes it out. Launch
// only reads the library, through its own proxy (/launch/api/library/…).
//
// The library does not say which pictures were generated and which are
// originals (its origin is create, upload or drive, and the AI label is the
// person's), so there is no Originais/Geradas filter.
import { api, h, note, input, select, plural } from './lib.js';
import { MAX_HEADLINE } from './matrix.js';

// libraryPanel is the panel. letterOf(sha256) is a picture's column letter
// ('' when not a column); hasHeadline(text) says whether a headline is a
// row; pickImage(creative) and pickHeadline(headline) add or take out one.
export function libraryPanel({ letterOf, hasHeadline, pickImage, pickHeadline }) {
  const search = input({ type: 'search', placeholder: 'Buscar', 'aria-label': 'Buscar na biblioteca' });
  const folder = select([['', 'Todas as pastas']], '', { 'aria-label': 'Pasta da biblioteca' });
  const out = h('div');
  const imgCount = h('span', {}, 'Imagens');
  const hlCount = h('span', {}, 'Headlines');
  const imgs = h('div', { class: 'lib-grid' });
  const hls = h('ul', { class: 'lib-hls' });
  const hlBox = h('div', { class: 'lib-part', tabindex: -1 },
    h('div', { class: 'lib-label' }, hlCount, h('span', { class: 'faint' }, 'clique para virar uma linha')), hls,
    h('p', { class: 'faint lib-foot' }, `Headlines com mais de ${MAX_HEADLINE} letras ficam de fora: o Taboola não aceita.`));
  const el = h('div', { class: 'lib-panel-in' },
    h('div', { class: 'lib-top' }, h('h2', {}, 'Biblioteca ', h('small', {}, 'do Create')), search),
    h('div', { class: 'lib-folders' }, folder),
    out,
    h('div', { class: 'lib-part' }, h('div', { class: 'lib-label' }, imgCount, h('span', { class: 'faint' }, 'clique para virar uma coluna')), imgs),
    hlBox);
  let creatives = [];
  let lines = [];
  let used = {};
  let loaded = false;
  let run = 0;
  const working = new Set(); // creatives being brought in

  async function load() {
    if (loaded) return;
    loaded = true;
    try {
      const [v, s] = await Promise.all([api('library/verticals'), api('library/sets?limit=200')]);
      const names = new Map((v.verticals || []).map((x) => [x.id, x.name]));
      const sets = (s.sets || []).map((x) => [String(x.id), [names.get(x.vertical_id) || x.vertical_id, x.name].filter(Boolean).join(' › ')]);
      sets.sort((a, b) => a[1].localeCompare(b[1], 'pt-BR'));
      const keep = folder.value;
      folder.replaceChildren(...[['', 'Todas as pastas'], ...sets].map(([id, label]) => h('option', { value: id }, label)));
      folder.value = keep;
    } catch (e) {
      loaded = false;
      out.replaceChildren(note('warn', e.message));
      imgCount.textContent = 'Imagens';
      return;
    }
    await list();
  }

  async function list() {
    const mine = ++run;
    const q = new URLSearchParams({ limit: '60' });
    if (folder.value) q.set('set', folder.value);
    if (search.value.trim()) q.set('q', search.value.trim());
    const qh = new URLSearchParams(q);
    qh.set('limit', '200');
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
        c.ai_label === 'ai' ? h('span', { class: 'lib-ai', title: 'Marcada como feita com IA na biblioteca' }, 'IA') : null),
      h('span', { class: 'lib-name' }, c.name || 'criativo ' + c.id),
      n ? h('span', { class: 'lib-used' }, 'no Launch: ' + plural(n, 'anúncio', 'anúncios')) : null);
    }) : [h('p', { class: 'faint' }, loaded ? 'Nenhuma imagem aqui.' : 'Carregando…')]));
    hls.replaceChildren(...(lines.length ? lines.map((x) => {
      const on = hasHeadline(x.text);
      return h('li', {}, h('button', { type: 'button', class: 'lib-hl' + (on ? ' on' : ''), 'aria-pressed': String(on), onclick: () => { pickHeadline(x); draw(); } },
        h('span', { class: 'lib-mark', 'aria-hidden': 'true' }, on ? '✓' : '+'), h('span', { class: 'lib-text' }, x.text), h('span', { class: 'mono faint' }, String(x.text.trim().length))));
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
  folder.addEventListener('change', () => list());

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
      if (![...folder.options].some((o) => o.value === String(id))) folder.append(h('option', { value: String(id) }, d.set?.name || 'Pasta ' + id));
      folder.value = String(id);
      list();
      return { creatives: (d.creatives || []).filter((c) => !c.hidden), headlines: (d.headlines || []).filter((x) => !x.hidden && x.text && x.text.trim().length <= MAX_HEADLINE) };
    },
  };
}
