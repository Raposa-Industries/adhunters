// The ads list: every creative seen in the range, as cards with their
// sparkline, filtered on the left and sorted on top.

import { frame, h, api, params, setParams, rangeParams, rangePicker, load, presence, momentum, num, word, direction, spark, windowText, keepRange, fill } from './spy.js';

frame('ads');

const main = document.querySelector('main');
const aside = document.querySelector('aside[data-frame="filters"]');

const SORTS = [
  ['presence', 'Presença'],
  ['momentum', 'Subindo mais'],
  ['share', 'Share of voice'],
  ['sightings', 'Vistas'],
  ['newest', 'Mais novos'],
  ['last_seen', 'Vistos por último'],
  ['lifespan', 'No ar há mais tempo'],
  ['days', 'Dias ativos'],
  ['total', 'Vistas no total'],
];
const STATUS = [
  ['new', 'Novo'], ['running', 'No ar'], ['ended', 'Saiu'], ['rising', 'Subindo'],
  ['fading', 'Caindo'], ['scaled', 'Escalado'], ['stopped', 'Parou'],
];
const FILTERS = ['q', 'category', 'vertical', 'operator', 'publisher', 'network', 'account', 'tracker', 'affiliate', 'device', 'status', 'min_days', 'sort'];

let items = [];

function option(value, label, current) {
  return h('option', { value, selected: String(value) === (current || '') }, label);
}

function select(name, label, opts) {
  const p = params();
  return h('label', { class: 'field' }, label,
    h('select', { name, onchange: (e) => set(name, e.target.value) }, option('', 'Todos', p.get(name)), opts.map(([v, l]) => option(v, l, p.get(name)))));
}

function set(name, value) {
  const p = params();
  if (value === '' || value == null) p.delete(name); else p.set(name, value);
  p.delete('offset');
  setParams(p);
  refresh();
}

async function drawFilters() {
  const p = params();
  const q = h('input', { type: 'search', name: 'q', value: p.get('q') || '', placeholder: 'Headline, marca, operador' });
  q.addEventListener('change', () => set('q', q.value.trim()));
  const status = h('div', { class: 'chips' }, STATUS.map(([v, l]) => h('label', { class: 'chip' },
    h('input', { type: 'checkbox', value: v, checked: p.getAll('status').includes(v), onchange: () => {
      const n = params();
      n.delete('status');
      for (const c of status.querySelectorAll('input:checked')) n.append('status', c.value);
      n.delete('offset');
      setParams(n);
      refresh();
    } }), h('span', {}, l))));
  const device = h('div', { class: 'segmented' }, [['', 'Todos'], ['phone', 'Celular'], ['desktop', 'Desktop']].map(([v, l]) =>
    h('label', {}, h('input', { type: 'radio', name: 'device', value: v, checked: (p.get('device') || '') === v, onchange: () => set('device', v) }), h('span', {}, l))));
  const minDays = h('input', { type: 'number', min: 0, name: 'min_days', value: p.get('min_days') || '', placeholder: '0' });
  minDays.addEventListener('change', () => set('min_days', minDays.value));
  aside.append(
    h('label', { class: 'field' }, 'Buscar', q),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Situação'), status),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Dispositivo'), device),
    h('label', { class: 'field' }, 'Dias ativos, no mínimo', minDays));
  const [vs, facets] = await Promise.all([api('verticals'), api('facets')]).catch(() => [null, null]);
  if (vs) {
    const verts = [];
    for (const c of vs.categories) for (const v of c.verticals) verts.push([v.id, c.name + ' · ' + v.name]);
    aside.append(
      select('category', 'Categoria', vs.categories.map((c) => [c.id, c.name])),
      select('vertical', 'Vertical', verts));
  }
  if (facets) {
    aside.append(
      select('network', 'Rede', facets.networks.map((n) => [n.id, n.name])),
      select('publisher', 'Publisher', facets.publishers.map((x) => [x.id, x.name])),
      select('tracker', 'Tracker', facets.trackers.map((t) => [t.value, t.value + ' (' + t.creatives + ')'])),
      select('affiliate', 'Rede de afiliados', facets.affiliates.map((t) => [t.value, t.value + ' (' + t.creatives + ')'])));
  }
  const clear = h('button', { type: 'button', class: 'ghost small', onclick: () => {
    const n = params();
    for (const f of FILTERS) n.delete(f);
    setParams(n);
    location.reload();
  } }, 'Limpar filtros');
  aside.append(clear);
}

function card(a) {
  const sub = [a.brand, a.operator_name].filter(Boolean).join(' · ');
  return h('a', { class: 'sp-card', href: '/spy/ads/' + a.id + keepRange() },
    a.image_url ? h('img', { src: a.image_url, alt: '', loading: 'lazy', referrerpolicy: 'no-referrer' }) : h('div', { class: 'sp-noimg' }),
    h('div', { class: 'sp-card-body' },
      h('div', { class: 'sp-card-head' }, a.headline || 'Sem headline'),
      h('div', { class: 'sp-card-sub' }, sub || '–'),
      h('div', { class: 'sp-card-badges' },
        a.vertical_name ? h('span', { class: 'badge', title: a.vertical_unsure ? 'Classificação incerta' : '' }, a.vertical_name + (a.vertical_unsure ? ' ?' : '')) : null,
        a.is_new ? h('span', { class: 'badge running' }, 'novo') : null,
        a.scaled ? h('span', { class: 'badge pair' }, 'escalado') : null,
        direction(a.direction, a.direction_text),
        a.running ? null : h('span', { class: 'badge' }, 'saiu')),
      h('div', { class: 'sp-card-foot' },
        h('span', { title: 'Vistas por 100 checagens' }, 'Presença ', h('b', { class: 'num' }, presence(a.presence))),
        h('span', { title: 'Contra o usual: ' + word(a.momentum_word) }, h('span', { class: 'num' }, momentum(a.momentum))),
        spark(a.series, 90, 24))));
}

async function refresh(more = false) {
  const p = params();
  const q = { ...rangeParams(), limit: 48 };
  for (const f of FILTERS) {
    const v = f === 'status' ? p.getAll(f) : p.get(f);
    if (v != null && v !== '' && !(Array.isArray(v) && !v.length)) q[f] = v;
  }
  const list = main.querySelector('.sp-list');
  if (more) q.offset = items.length;
  const out = await load(more ? main.querySelector('.sp-more') : list, () => api('ads', q));
  if (!out) return;
  items = more ? items.concat(out.items) : out.items;
  main.querySelector('.sp-count').textContent = num(out.total) + ' criativos · ' + windowText(out.window);
  const cards = h('div', { class: 'sp-cards' }, items.map(card));
  fill(list, items.length ? cards : h('p', { class: 'empty' }, 'Nenhum anúncio com esses filtros neste período.'),
    out.next_offset != null ? h('div', { class: 'sp-more' }, h('button', { type: 'button', onclick: () => refresh(true) }, 'Mostrar mais')) : null);
}

const sort = h('select', { 'aria-label': 'Ordenar', onchange: (e) => set('sort', e.target.value === 'presence' ? '' : e.target.value) },
  SORTS.map(([v, l]) => option(v, l, params().get('sort') || 'presence')));
main.append(
  h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Anúncios'), h('p', { class: 'sp-count' }, ''))),
  h('div', { class: 'sp-toolbar' }, rangePicker(() => refresh()), sort),
  h('div', { class: 'sp-list' }));
drawFilters();
refresh();
