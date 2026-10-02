// One ad (a creative and its ads): what it is, how much it runs, where and
// when, its links and campaigns, what its auctions cost, and what Raposa
// found. From here a person asks Raposa to investigate it, or opens Create
// with this ad's image and headline to make variations.

import { frame, h, api, post, rangePicker, comparePicker, rangeParams, load, presence, momentum, num, pct, money, when, day, word, direction, spark, bars, table, stats, link, windowText, keepRange, fill } from './spy.js';

frame('ads');

const main = document.querySelector('main');
const id = location.pathname.split('/').pop();

async function draw() {
  const body = main.querySelector('.sp-body');
  const out = await load(body, () => api('ads/' + id, rangeParams()));
  if (!out) return;
  const c = out.creative;
  document.title = (c.headline || 'Anúncio ' + c.id) + ' · Spy';
  const n = out.numbers[0] || {};

  const meta = h('div', { class: 'sp-meta' },
    c.vertical_name ? h('a', { class: 'badge', href: '/spy/?vertical=' + encodeURIComponent(c.vertical_id), title: c.vertical_source === 'hand' ? 'Vertical escolhida à mão' : 'Classificado por ' + (c.vertical_source || '?') + ', confiança ' + pct(c.vertical_confidence * 100, 0) }, (c.category_name ? c.category_name + ' · ' : '') + c.vertical_name + (c.vertical_unsure ? ' ?' : '') + (c.vertical_source === 'hand' ? ' ✓' : '')) : h('span', { class: 'badge' }, 'Sem vertical'),
    fixVertical(c),
    c.is_new ? h('span', { class: 'badge running' }, 'novo') : null,
    c.running ? h('span', { class: 'badge running' }, 'no ar') : h('span', { class: 'badge' }, 'saiu'),
    c.scaled ? h('span', { class: 'badge pair' }, 'escalado') : null,
    direction(c.direction, c.direction_text),
    c.format_type ? h('span', { class: 'badge' }, c.format_type) : null,
    ...(c.trackers || []).map((t) => h('span', { class: 'badge review' }, t)),
    ...(c.affiliate_networks || []).map((t) => h('span', { class: 'badge review' }, t)));

  const raposa = drawRaposa(out.raposa);
  // Create reads the image, headline and vertical itself from the id.
  const create = h('a', { class: 'button', href: '/create/?from=spy&creative=' + encodeURIComponent(id) }, 'Criar variações');
  const actions = raposa.action || h('div', { class: 'actions' });
  actions.prepend(create);

  const hero = h('div', { class: 'sp-hero' },
    c.image_url ? h('img', { src: c.image_url, alt: '', referrerpolicy: 'no-referrer' }) : h('div', { class: 'empty' }, 'Sem imagem'),
    h('div', {},
      h('h1', {}, c.headline || 'Sem headline'),
      h('p', { class: 'lead' },
        c.brand ? c.brand : '', c.brand && c.operator_name ? ' · ' : '',
        c.operator_id ? link('/spy/operators/' + c.operator_id + keepRange(), c.operator_name || c.operator_code) : ''),
      meta,
      c.direction_text ? h('p', { class: 'muted' }, c.direction_text) : null,
      h('div', { class: 'panel', style: 'margin-top:14px' }, stats([
        ['Presença', presence(n.presence)],
        ['Usual', presence(n.presence_usual)],
        ['Contra o usual', momentum(n.momentum) + (n.momentum_word ? ' ' + word(n.momentum_word) : '')],
        ['Share of voice', pct(n.share_pct)],
        ['Vistas', num(n.sightings)],
        ['Publishers', num(n.publishers)],
        ['Primeira vez', day(c.first_seen_at)],
        ['Última vez', when(c.last_seen_at)],
        ['Dias ativos', num(c.active_days)],
        ['Vistas no total', num(c.sightings_total)],
      ])),
      actions));

  const series = out.series || [];
  const trend = h('section', { class: 'panel' }, h('h3', {}, out.window.recent || out.window.hours ? 'Presença por dia, 30 dias' : 'Presença por dia no período'),
    spark(series.map((s) => s.presence), 600, 70),
    h('p', { class: 'faint' }, series.length ? day(series[0].day) + ' a ' + day(series[series.length - 1].day) : ''));

  const hours = drawHours(out.hours, out.hours_state);
  if (out.hours_state !== 'database') waitForHours(hours);

  const pubs = h('section', { class: 'panel' }, h('h3', {}, 'Onde aparece'), table([
    ['Publisher', (r) => link('/spy/publishers/' + r.id + keepRange(), r.name)],
    ['Dispositivo', (r) => r.device],
    ['Vistas', (r) => num(r.sightings), 'num'],
    ['Checagens', (r) => num(r.scrapes), 'num'],
    ['Última vez', (r) => when(r.last_seen_at)],
  ], out.publishers));

  const prices = h('section', { class: 'panel' }, h('h3', {}, 'Preço do leilão'),
    out.prices.length ? table([
      ['Rede', (r) => r.network],
      ['Leilões', (r) => num(r.auctions), 'num'],
      ['Clearing típico', (r) => money(r.clearing_typical), 'num'],
      ['Clearing médio', (r) => money(r.clearing_avg), 'num'],
      ['Lance típico', (r) => money(r.bid_typical), 'num'],
      ['Segundo lugar', (r) => money(r.second_avg), 'num'],
      ['Dias', (r) => num(r.days), 'num'],
    ], out.prices) : h('p', { class: 'empty' }, 'Nenhum leilão visto neste período.'),
    out.prices_by_publisher.length ? h('div', { style: 'margin-top:12px' }, table([
      ['Publisher', (r) => r.name],
      ['Dispositivo', (r) => r.device],
      ['Leilões', (r) => num(r.auctions), 'num'],
      ['Clearing médio', (r) => money(r.clearing_avg), 'num'],
      ['Lance médio', (r) => money(r.bid_avg), 'num'],
      ['Segundo lugar', (r) => money(r.second_avg), 'num'],
    ], out.prices_by_publisher)) : null);

  const ads = h('section', { class: 'panel' }, h('h3', {}, 'Anúncios com este criativo'), table([
    ['Headline', (r) => h('span', {}, r.headline || '–', r.description ? h('div', { class: 'faint' }, r.description) : null)],
    ['Marca', (r) => r.brand || '–'],
    ['Conta', (r) => h('span', { class: 'mono' }, r.account || '–')],
    ['Vistas', (r) => num(r.sightings), 'num'],
    ['Direção', (r) => direction(r.direction, r.direction_text) || '–'],
    ['Última vez', (r) => when(r.last_seen_at)],
  ], out.ads));

  const links = h('section', { class: 'panel' }, h('h3', {}, 'Links'), table([
    ['Destino', (r) => h('span', { class: 'sp-url' }, r.host + (r.path || ''))],
    ['Tracker', (r) => r.tracker || '–'],
    ['Afiliado', (r) => r.affiliate_network || '–'],
    ['Vistas', (r) => num(r.sightings), 'num'],
    ['Exemplo', (r) => r.sample_url ? h('a', { href: r.sample_url, rel: 'noopener noreferrer', target: '_blank' }, 'abrir') : '–'],
  ], out.links));

  const campaigns = h('section', { class: 'panel' }, h('h3', {}, 'Campanhas'), table([
    ['Campanha', (r) => r.name || r.external_id],
    ['Marca', (r) => r.brand || '–'],
    ['Grupo', (r) => r.parent_name || r.parent_external_id || '–'],
    ['Conta', (r) => h('span', { class: 'mono' }, r.account || '–')],
    ['Vistas', (r) => num(r.sightings), 'num'],
    ['Última vez', (r) => when(r.last_seen_at)],
  ], out.campaigns));

  main.querySelector('.sp-window').textContent = windowText(out.window);
  fill(body, hero, trend,
    h('div', { class: 'sp-grid', style: 'margin-top:14px' }, hours, prices),
    h('div', { style: 'margin-top:14px' }, pubs),
    h('div', { style: 'margin-top:14px' }, ads),
    h('div', { class: 'sp-grid', style: 'margin-top:14px' }, links, campaigns),
    raposa.panel ? h('div', { style: 'margin-top:14px' }, raposa.panel) : null);
}

// fixVertical is a button that opens the list of verticals: the one chosen
// replaces the classifier's for this creative (and teaches it), until it is
// given back.
function fixVertical(c) {
  const box = h('span', { class: 'sp-inline' });
  const open = h('button', { type: 'button', class: 'ghost small' }, c.vertical_name ? 'Corrigir vertical' : 'Escolher vertical');
  open.addEventListener('click', async () => {
    open.disabled = true;
    let vs;
    try {
      vs = await api('verticals');
    } catch (err) {
      box.replaceChildren(h('span', { class: 'muted' }, err.message));
      return;
    }
    const sel = h('select', { 'aria-label': 'Vertical' }, h('option', { value: '' }, 'Escolha…'),
      vs.categories.map((cat) => h('optgroup', { label: cat.name },
        cat.verticals.map((v) => h('option', { value: v.id, selected: v.id === c.vertical_id }, v.name)))));
    const msg = h('span', { class: 'muted' });
    const send = async (vertical) => {
      try {
        await post('ads/' + id + '/vertical', { vertical_id: vertical });
        draw();
      } catch (err) {
        msg.textContent = err.message;
      }
    };
    box.replaceChildren(sel,
      h('button', { type: 'button', class: 'small', onclick: () => sel.value && send(sel.value) }, 'Salvar'),
      c.vertical_source === 'hand' ? h('button', { type: 'button', class: 'ghost small', onclick: () => send('') }, 'Voltar ao automático') : null,
      msg);
  });
  box.append(open);
  return box;
}

// drawHours is the hour of day (São Paulo) the ad shows, the last 7 days of
// the range. Hours older than Tracks keeps come back from its archive.
function drawHours(rows, state) {
  const by = new Array(24).fill(0);
  for (const r of rows || []) by[r.hour] += Number(r.sightings) || 0;
  const max = Math.max(1, ...by);
  const devices = {};
  for (const r of rows || []) devices[r.device] = (devices[r.device] || 0) + Number(r.sightings);
  const note = { coming: 'Trazendo as horas destes dias do arquivo; aparecem aqui em um ou dois minutos.',
    archive: 'As horas destes dias estão no arquivo e não voltaram agora. Abra a página de novo mais tarde.' }[state];
  return h('section', { class: 'panel' }, h('h3', {}, 'Hora do dia (São Paulo), últimos 7 dias'),
    note ? h('p', { class: 'muted' }, note) : null,
    h('div', { class: 'sp-hours' }, by.map((v, i) => h('span', { title: i + 'h: ' + num(v), style: `height:${(100 * v / max).toFixed(1)}%` }))),
    h('div', { class: 'sp-hours-axis' }, by.map((_, i) => h('span', {}, i % 6 === 0 ? i + 'h' : ''))),
    Object.keys(devices).length ? h('div', { style: 'margin-top:12px' }, bars(Object.entries(devices).map(([k, v]) => ({ label: k, value: v })))) : null);
}

// waitForHours asks again every 15 s while Tracks brings the hours back,
// and redraws the panel when they are in.
async function waitForHours(panel) {
  for (let i = 0; i < 12; i++) {
    await new Promise((r) => setTimeout(r, 15000));
    if (!panel.isConnected) return;
    let res;
    try {
      res = await api('ads/' + id + '/hours', rangeParams());
    } catch (err) {
      continue;
    }
    if (res.state === 'coming') continue;
    const next = drawHours(res.hours, res.state);
    panel.replaceWith(next);
    return;
  }
}

const STATUS = { waiting: 'esperando', running: 'rodando', completed: 'pronta', failed: 'falhou', stopped: 'parada' };

// drawRaposa is the investigate button and what Raposa found.
function drawRaposa(r) {
  if (!r || !r.available) return { action: null, panel: null };
  const msg = h('span', { class: 'muted' });
  const ask = (mode) => async (e) => {
    e.target.disabled = true;
    try {
      const res = await api('ads/' + id + '/investigate', {}, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ mode }),
      });
      msg.textContent = 'Pedido à Raposa (investigação ' + res.investigation_id + ').';
    } catch (err) {
      msg.textContent = err.message;
      e.target.disabled = false;
    }
  };
  const action = h('div', { class: 'actions' },
    h('button', { type: 'button', class: 'primary', onclick: ask('deep') }, 'Investigar com a Raposa'),
    h('button', { type: 'button', class: 'ghost', onclick: ask('quick') }, 'Checagem rápida'), msg);
  const panel = (r.investigations.length || r.pages.length) ? h('section', { class: 'panel' }, h('h3', {}, 'Raposa'),
    table([
      ['Investigação', (x) => '#' + x.id + ' · ' + x.mode],
      ['Situação', (x) => (STATUS[x.status] || x.status) + (x.stage ? ' · ' + x.stage : '')],
      ['Visitas', (x) => num(x.visits_done) + ' / ' + num(x.visits_target), 'num'],
      ['Cloaking', (x) => x.is_cloaked == null ? '–' : x.is_cloaked ? 'sim' : 'não'],
      ['Pedida', (x) => when(x.requested_at) + (x.requested_by ? ' · ' + x.requested_by : '')],
    ], r.investigations, 'Nenhuma investigação ainda.'),
    r.pages.length ? h('div', { style: 'margin-top:12px' }, table([
      ['Página', (x) => h('span', {}, x.title || x.domain, h('div', { class: 'sp-url' }, x.final_url || ''))],
      ['Tipo', (x) => x.page_kind || '–'],
      ['Checkout', (x) => x.checkout_platform || '–'],
      ['Vendedor', (x) => [x.seller_platform, x.seller_account].filter(Boolean).join(' · ') || '–'],
      ['Visitas', (x) => num(x.visits), 'num'],
    ], r.pages)) : null) : null;
  return { action, panel };
}

main.append(
  h('nav', { class: 'crumbs' }, link('/spy/' + keepRange(), 'Anúncios'), h('span', { class: 'sep' }, '/'), h('span', { 'aria-current': 'page' }, 'Anúncio ' + id)),
  h('div', { class: 'sp-toolbar' }, rangePicker(draw), h('div', { class: 'sp-tools' }, comparePicker(draw), h('span', { class: 'sp-count sp-window' }))),
  h('div', { class: 'sp-body' }));
draw();
