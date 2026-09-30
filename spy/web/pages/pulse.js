// The market: each vertical's presence and momentum over the range, how
// many creatives and operators are rising, fading and stopped now, and the
// latest changes of direction.

import { frame, h, api, rangeParams, rangePicker, load, presence, momentum, num, pct, when, word, direction, table, link, windowText, keepRange, fill } from './spy.js';

frame('pulse');

const main = document.querySelector('main');
const KIND = { creative: 'Criativos', operator: 'Operadores' };

async function draw() {
  const body = main.querySelector('.sp-body');
  const out = await load(body, () => Promise.all([api('pulse', rangeParams()), api('events', { limit: 40 })]));
  if (!out) return;
  const [pulse, events] = out;
  main.querySelector('.sp-window').textContent = windowText(pulse.window);

  const counts = {};
  for (const d of pulse.direction) (counts[d.kind] ||= {})[d.direction] = d.n;
  const now = h('section', { class: 'panel' }, h('h3', {}, 'Agora'), table([
    ['', (k) => KIND[k] || k],
    ['Subindo', (k) => num(counts[k].rising || 0), 'num'],
    ['Estável', (k) => num(counts[k].steady || 0), 'num'],
    ['Caindo', (k) => num(counts[k].fading || 0), 'num'],
    ['Parou', (k) => num(counts[k].stopped || 0), 'num'],
  ], Object.keys(counts)));

  const verts = h('section', { class: 'panel' }, h('h3', {}, 'Verticais'), table([
    ['Vertical', (r) => r.vertical_id ? link('/spy/?vertical=' + encodeURIComponent(r.vertical_id) + keepRange().replace('?', '&'), r.vertical_name || r.vertical_id) : 'Sem vertical'],
    ['Categoria', (r) => r.category_name || '–'],
    ['Presença', (r) => presence(r.presence), 'num'],
    ['Usual', (r) => presence(r.presence_usual), 'num'],
    ['Share', (r) => pct(r.share_pct), 'num'],
    ['Contra o usual', (r) => h('span', { title: r.momentum_sure ? 'certo' : 'ainda incerto' }, momentum(r.momentum) + ' ' + word(r.momentum_word)), 'num'],
    ['Vistas', (r) => num(r.sightings), 'num'],
  ], pulse.verticals));

  const feed = h('section', { class: 'panel' }, h('h3', {}, 'Mudanças de direção'), table([
    ['Quando', (e) => when(e.at)],
    ['O quê', (e) => e.kind === 'creative' ? link('/spy/ads/' + e.subject_id, e.headline || 'Anúncio ' + e.subject_id)
      : e.kind === 'operator' ? link('/spy/operators/' + e.subject_id, 'Operador ' + e.subject_id) : (e.key || e.kind)],
    ['De', (e) => direction(e.from_direction) || '–'],
    ['Para', (e) => direction(e.to_direction, e.reason_text) || '–'],
    ['Por quê', (e) => h('span', { class: 'muted' }, e.reason_text || '')],
  ], events, 'Nenhuma mudança ainda.'));

  fill(body, now, verts, feed);
}

main.append(
  h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Mercado'), h('p', { class: 'sp-count sp-window' }))),
  h('div', { class: 'sp-toolbar' }, rangePicker(draw)),
  h('div', { class: 'sp-body' }));
draw();
