// One publisher: how often we check it, and the operators and creatives
// that show most on it over the last 30 days.

import { frame, h, api, rangeParams, rangePicker, load, num, pct, day, when, table, stats, link, windowText, keepRange, fill } from './spy.js';

frame('publishers');

const main = document.querySelector('main');
const id = location.pathname.split('/').pop();

async function draw() {
  const body = main.querySelector('.sp-body');
  const out = await load(body, () => api('publishers/' + id, rangeParams()));
  if (!out) return;
  const p = out.publisher;
  const n = out.numbers[0] || {};
  document.title = p.name + ' · Spy';
  main.querySelector('h1').textContent = p.name;
  main.querySelector('.sp-window').textContent = windowText(out.window);
  const head = h('div', { class: 'panel' },
    p.domain ? h('p', { class: 'muted', style: 'margin:0 0 12px' }, p.domain) : null,
    stats([
      ['Checagens', num(n.checks)],
      ['Vistas', num(n.sightings)],
      ['Por 100 checagens', num(n.per_100_checks, 1)],
      ['Share', pct(n.share_pct)],
      ['Operadores', num(n.operators)],
      ['Vistas, 30 dias', num(p.sightings_30d)],
      ['Criativos, 30 dias', num(p.creatives_30d)],
      ['Primeira vez', day(p.first_seen_at)],
      ['Última vez', when(p.last_seen_at)],
    ]));
  const ops = h('section', { class: 'panel' }, h('h3', {}, 'Operadores, 30 dias'), table([
    ['Operador', (r) => link('/spy/operators/' + r.id + keepRange(), r.name || r.code)],
    ['Vistas', (r) => num(r.sightings), 'num'],
    ['Share', (r) => pct(r.share_pct), 'num'],
  ], out.operators));
  const cr = h('section', { class: 'panel' }, h('h3', {}, 'Criativos, 30 dias'), table([
    ['Criativo', (r) => h('a', { href: '/spy/ads/' + r.id + keepRange() }, r.headline || 'Anúncio ' + r.id)],
    ['Vertical', (r) => r.vertical_name || '–'],
    ['Vistas', (r) => num(r.sightings), 'num'],
  ], out.creatives));
  fill(body, head, h('div', { class: 'sp-grid', style: 'margin-top:14px' }, ops, cr));
}

main.append(
  h('nav', { class: 'crumbs' }, link('/spy/publishers/' + keepRange(), 'Publishers'), h('span', { class: 'sep' }, '/'), h('span', { 'aria-current': 'page' }, 'Publisher')),
  h('div', { class: 'page-head' }, h('h1', {}, 'Publisher')),
  h('div', { class: 'sp-toolbar' }, rangePicker(draw), h('span', { class: 'sp-count sp-window' })),
  h('div', { class: 'sp-body' }));
draw();
