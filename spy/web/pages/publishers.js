// The publishers list: every site we check, how often, and how many ads
// and operators show on it.

import { frame, h, api, params, setParams, rangeParams, rangePicker, load, num, pct, table, link, windowText, keepRange, fill } from './spy.js';

frame('publishers');

const main = document.querySelector('main');
const aside = document.querySelector('aside[data-frame="filters"]');

function set(name, value) {
  const p = params();
  if (!value) p.delete(name); else p.set(name, value);
  setParams(p);
  refresh();
}

async function drawFilters() {
  const q = h('input', { type: 'search', value: params().get('q') || '', placeholder: 'Nome ou domínio' });
  q.addEventListener('change', () => set('q', q.value.trim()));
  aside.append(h('label', { class: 'field' }, 'Buscar', q));
  const facets = await api('facets').catch(() => null);
  if (facets) {
    const cur = params().get('network') || '';
    aside.append(h('label', { class: 'field' }, 'Rede', h('select', { onchange: (e) => set('network', e.target.value) },
      h('option', { value: '' }, 'Todas'), facets.networks.map((n) => h('option', { value: n.id, selected: String(n.id) === cur }, n.name)))));
  }
}

async function refresh() {
  const p = params();
  const q = { ...rangeParams(), q: p.get('q'), network: p.get('network') };
  const list = main.querySelector('.sp-list');
  const out = await load(list, () => api('publishers', q));
  if (!out) return;
  main.querySelector('.sp-count').textContent = num(out.items.length) + ' publishers · ' + windowText(out.window);
  fill(list, table([
    ['Publisher', (r) => h('span', {}, link('/spy/publishers/' + r.id + keepRange(), r.name), h('div', { class: 'faint' }, r.domain || ''))],
    ['Checagens', (r) => num(r.checks), 'num'],
    ['Vistas', (r) => num(r.sightings), 'num'],
    ['Por 100 checagens', (r) => num(r.per_100_checks, 1), 'num'],
    ['Share', (r) => pct(r.share_pct), 'num'],
    ['Operadores', (r) => num(r.operators), 'num'],
    ['Criativos, 30 dias', (r) => num(r.creatives_30d), 'num'],
  ], out.items, 'Nenhum publisher com esses filtros.'));
}

main.append(
  h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Publishers'), h('p', { class: 'sp-count' }))),
  h('div', { class: 'sp-toolbar' }, rangePicker(refresh)),
  h('div', { class: 'sp-list' }));
drawFilters();
refresh();
