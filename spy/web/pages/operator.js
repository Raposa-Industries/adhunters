// One operator: its numbers, the creatives with the most presence, its
// accounts, brands and publishers.

import { frame, h, api, rangeParams, rangePicker, load, presence, momentum, num, pct, when, day, word, direction, spark, table, stats, link, windowText, keepRange, fill, kind } from './spy.js';

frame('operators');

const main = document.querySelector('main');
const id = location.pathname.split('/').pop();

async function draw() {
  const body = main.querySelector('.sp-body');
  const out = await load(body, () => api('operators/' + id, rangeParams()));
  if (!out) return;
  const o = out.operator;
  document.title = (o.name || o.code) + ' · Spy';
  main.querySelector('h1').textContent = o.name || o.code;
  main.querySelector('.sp-window').textContent = windowText(out.window);
  const n = out.numbers[0] || {};
  const head = h('div', { class: 'panel' },
    h('div', { class: 'sp-meta', style: 'margin:0 0 12px' },
      o.code ? h('span', { class: 'badge' }, o.code) : null,
      o.kind ? h('span', { class: 'badge review' }, kind(o.kind)) : null,
      o.vertical_name ? h('span', { class: 'badge' }, o.vertical_name) : null,
      direction(o.direction, o.direction_text),
      ...(o.brands || []).slice(0, 8).map((b) => h('span', { class: 'badge' }, b))),
    stats([
      ['Presença', presence(n.presence)],
      ['Usual', presence(n.presence_usual)],
      ['Contra o usual', momentum(n.momentum) + (n.momentum_word ? ' ' + word(n.momentum_word) : '')],
      ['Share of voice', pct(n.share_pct)],
      ['Lançamentos', num(n.launches)],
      ['Acerto', n.hit_rate_pct == null ? '–' : pct(n.hit_rate_pct, 0)],
      ['Criativos no ar', num(o.live_creatives_count)],
      ['Criativos', num(o.creatives_count)],
      ['Vistas, 30 dias', num(o.sightings_30d)],
      ['Primeira vez', day(o.first_seen_at)],
    ]),
    o.direction_text ? h('p', { class: 'muted' }, o.direction_text) : null);

  const creatives = h('section', { class: 'panel' }, h('h3', {}, 'Criativos com mais presença'),
    out.creatives.length ? h('div', { class: 'sp-cards' }, out.creatives.map((c) => h('a', { class: 'sp-card', href: '/spy/ads/' + c.id + keepRange() },
      c.image_url ? h('img', { src: c.image_url, alt: '', loading: 'lazy', referrerpolicy: 'no-referrer' }) : h('div', { class: 'sp-noimg' }),
      h('div', { class: 'sp-card-body' },
        h('div', { class: 'sp-card-head' }, c.headline || 'Sem headline'),
        h('div', { class: 'sp-card-sub' }, [c.vertical_name, c.running ? 'no ar' : 'saiu'].filter(Boolean).join(' · ')),
        h('div', { class: 'sp-card-foot' }, h('span', {}, 'Presença ', h('b', { class: 'num' }, presence(c.presence))), h('span', { class: 'num' }, momentum(c.momentum))))))) : h('p', { class: 'empty' }, 'Nenhum criativo neste período.'));

  const accounts = h('section', { class: 'panel' }, h('h3', {}, 'Contas'), table([
    ['Conta', (r) => h('span', { class: 'mono' }, r.external_id)],
    ['Organização', (r) => h('span', { class: 'mono' }, r.org_external_id || '–')],
    ['Última vez', (r) => when(r.last_seen_at)],
  ], out.accounts, 'Nenhuma conta ligada.'));
  const brands = h('section', { class: 'panel' }, h('h3', {}, 'Marcas'), table([
    ['Marca', (r) => r.name], ['Anúncios', (r) => num(r.ads), 'num'], ['Vistas', (r) => num(r.sightings), 'num'],
  ], out.brands));
  const pubs = h('section', { class: 'panel' }, h('h3', {}, 'Publishers'), table([
    ['Publisher', (r) => link('/spy/publishers/' + r.id + keepRange(), r.name)], ['Dispositivo', (r) => r.device], ['Vistas', (r) => num(r.sightings), 'num'],
  ], out.publishers));
  fill(body, head, h('div', { style: 'margin-top:14px' }, creatives),
    h('div', { class: 'sp-grid', style: 'margin-top:14px' }, pubs, brands), h('div', { style: 'margin-top:14px' }, accounts));
}

main.append(
  h('nav', { class: 'crumbs' }, link('/spy/operators/' + keepRange(), 'Operadores'), h('span', { class: 'sep' }, '/'), h('span', { 'aria-current': 'page' }, 'Operador')),
  h('div', { class: 'page-head' }, h('h1', {}, 'Operador')),
  h('div', { class: 'sp-toolbar' }, rangePicker(draw), h('span', { class: 'sp-count sp-window' })),
  h('div', { class: 'sp-body' }));
draw();
