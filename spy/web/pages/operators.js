// The operators list: who is running ads in the range, with their presence,
// momentum and launches.

import { frame, h, api, params, setParams, rangeParams, rangePicker, load, presence, momentum, num, pct, word, direction, table, link, windowText, keepRange, fill } from './spy.js';

frame('operators');

const main = document.querySelector('main');
const aside = document.querySelector('aside[data-frame="filters"]');
const SORTS = [['presence', 'Presença'], ['share', 'Share of voice'], ['momentum', 'Subindo mais'], ['launches', 'Lançamentos'], ['hit_rate', 'Taxa de acerto'], ['creatives', 'Criativos no ar'], ['name', 'Nome']];
const KINDS = [['direct', 'Direto'], ['affiliate', 'Afiliado'], ['arbitrage', 'Arbitragem']];
const FILTERS = ['q', 'vertical', 'kind', 'publisher', 'network', 'sort'];
let items = [];

function set(name, value) {
  const p = params();
  if (!value) p.delete(name); else p.set(name, value);
  setParams(p);
  refresh();
}

function select(name, label, opts) {
  const cur = params().get(name) || '';
  return h('label', { class: 'field' }, label, h('select', { onchange: (e) => set(name, e.target.value) },
    h('option', { value: '' }, 'Todos'), opts.map(([v, l]) => h('option', { value: v, selected: String(v) === cur }, l))));
}

async function drawFilters() {
  const q = h('input', { type: 'search', value: params().get('q') || '', placeholder: 'Nome, código, marca' });
  q.addEventListener('change', () => set('q', q.value.trim()));
  aside.append(h('label', { class: 'field' }, 'Buscar', q), select('kind', 'Tipo', KINDS));
  const [vs, facets] = await Promise.all([api('verticals'), api('facets')]).catch(() => [null, null]);
  if (vs) {
    const verts = [];
    for (const c of vs.categories) for (const v of c.verticals) verts.push([v.id, c.name + ' · ' + v.name]);
    aside.append(select('vertical', 'Vertical', verts));
  }
  if (facets) {
    aside.append(select('network', 'Rede', facets.networks.map((n) => [n.id, n.name])),
      select('publisher', 'Publisher', facets.publishers.map((x) => [x.id, x.name])));
  }
}

async function refresh(more = false) {
  const p = params();
  const q = { ...rangeParams(), limit: 50 };
  for (const f of FILTERS) if (p.get(f)) q[f] = p.get(f);
  if (more) q.offset = items.length;
  const list = main.querySelector('.sp-list');
  const out = await load(list, () => api('operators', q));
  if (!out) return;
  items = more ? items.concat(out.items) : out.items;
  main.querySelector('.sp-count').textContent = num(out.total) + ' operadores · ' + windowText(out.window);
  fill(list, table([
    ['Operador', (r) => h('span', {}, link('/spy/operators/' + r.id + keepRange(), r.name || r.code), h('div', { class: 'faint' }, [r.code, (r.brands || []).join(', ')].filter(Boolean).join(' · ')))],
    ['Vertical', (r) => r.vertical_name || '–'],
    ['Presença', (r) => presence(r.presence), 'num'],
    ['Share', (r) => pct(r.share_pct), 'num'],
    ['Contra o usual', (r) => h('span', { title: word(r.momentum_word) }, momentum(r.momentum)), 'num'],
    ['Direção', (r) => direction(r.direction, r.direction_text) || '–'],
    ['No ar', (r) => num(r.live_creatives_count), 'num'],
    ['Lançamentos', (r) => num(r.launches), 'num'],
    ['Acerto', (r) => r.hit_rate_pct == null ? '–' : pct(r.hit_rate_pct, 0), 'num'],
  ], items, 'Nenhum operador com esses filtros neste período.'),
  items.length < out.total ? h('div', { class: 'sp-more' }, h('button', { type: 'button', onclick: () => refresh(true) }, 'Mostrar mais')) : null);
}

const sort = h('select', { 'aria-label': 'Ordenar', onchange: (e) => set('sort', e.target.value === 'presence' ? '' : e.target.value) },
  SORTS.map(([v, l]) => h('option', { value: v, selected: v === (params().get('sort') || 'presence') }, l)));
main.append(
  h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Operadores'), h('p', { class: 'sp-count' }))),
  h('div', { class: 'sp-toolbar' }, rangePicker(() => refresh()), sort),
  h('div', { class: 'sp-list' }));
drawFilters();
refresh();
