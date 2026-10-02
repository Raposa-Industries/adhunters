// One operator: its numbers, its volume day by day, the creatives with the
// most presence, its campaigns with their brands, why each account and site
// is in it, and publishers. From here a person watches it (a notice when it
// rises or scales), hides it from the lists, or gives it a nickname.

import { frame, h, api, post, rangeParams, rangePicker, comparePicker, load, presence, momentum, num, pct, when, day, word, direction, dayBars, table, stats, link, windowText, keepRange, fill, kind } from './spy.js';

frame('operators');

const main = document.querySelector('main');
const id = location.pathname.split('/').pop();

// why says, in Portuguese, the rule that put an account or site here.
function why(reason, agency) {
  if (!reason) return agency ? 'agência ' + agency : '–';
  let m;
  if ((m = reason.match(/^same account name root "(.*)"$/))) return 'mesmo nome de conta "' + m[1] + '"';
  if ((m = reason.match(/^same email in account name "(.*)"$/))) return 'mesmo e-mail no nome da conta "' + m[1] + '"';
  if (reason === "buys ads only for this operator's sites") return 'compra anúncios só para os sites deste operador';
  if ((m = reason.match(/^fixed by hand: ?(.*)$/))) return 'juntado à mão' + (m[1] ? ': ' + m[1] : '');
  if ((m = reason.match(/^split by hand: ?(.*)$/))) return 'separado à mão' + (m[1] ? ': ' + m[1] : '');
  return 'mesmo ' + reason + ' nas páginas';
}

const NOTICE = { sent: 'enviado', skipped: 'só aqui (sem Pushcut)', failed: 'falhou', pending: 'enviando' };

// drawMarks is the watch, hide and nickname controls, and the notices.
function drawMarks(mark, notices) {
  const m = mark || { hidden: false, watched: false, nickname: null };
  const msg = h('span', { class: 'muted' });
  const save = async (change, btn) => {
    if (btn) btn.disabled = true;
    try {
      const res = await post('operators/' + id + '/mark', change);
      draw();
      return res;
    } catch (err) {
      msg.textContent = err.message;
      if (btn) btn.disabled = false;
    }
  };
  const nick = h('input', { type: 'text', maxlength: 60, value: m.nickname || '', placeholder: 'Apelido', 'aria-label': 'Apelido' });
  const watch = h('button', { type: 'button', class: m.watched ? 'ghost' : 'primary', title: 'Um aviso quando ele subir ou escalar' },
    m.watched ? 'Deixar de vigiar' : 'Vigiar');
  watch.addEventListener('click', () => save({ watched: !m.watched }, watch));
  const hide = h('button', { type: 'button', class: 'ghost', title: 'Tira o operador e os anúncios dele das listas' },
    m.hidden ? 'Mostrar nas listas' : 'Ocultar das listas');
  hide.addEventListener('click', () => save({ hidden: !m.hidden }, hide));
  const name = h('button', { type: 'button', class: 'ghost small' }, 'Salvar apelido');
  name.addEventListener('click', () => save({ nickname: nick.value }, name));
  const controls = h('div', { class: 'sp-marks' }, watch, hide, nick, name,
    m.nickname ? h('span', { class: 'faint' }, 'Apague o apelido e salve para voltar ao nome do agrupamento.') : null, msg);
  const state = [m.watched ? 'Vigiado desde ' + when(m.watched_since) + '.' : '', m.hidden ? 'Oculto das listas de anúncios e operadores.' : '']
    .filter(Boolean).join(' ');
  const list = notices && notices.length ? h('div', { style: 'margin-top:12px' }, table([
    ['Aviso', (r) => h('span', {}, r.title, h('div', { class: 'faint' }, r.body))],
    ['Quando', (r) => when(r.at)],
    ['Celular', (r) => h('span', { title: r.error || '' }, NOTICE[r.status] || r.status)],
  ], notices)) : null;
  return h('section', { class: 'panel' }, h('h3', {}, 'Acompanhar'),
    state ? h('p', { class: 'muted' }, state) : null, controls, list);
}

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
      out.mark && out.mark.watched ? h('span', { class: 'badge running' }, 'vigiado') : null,
      out.mark && out.mark.hidden ? h('span', { class: 'badge' }, 'oculto') : null,
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
      ['Primeira vez', when(o.first_seen_at)],
    ]),
    o.direction_text ? h('p', { class: 'muted' }, o.direction_text) : null);

  const series = out.series || [];
  const volume = h('section', { class: 'panel' },
    h('h3', {}, out.window.recent || out.window.hours ? 'Volume por dia, 30 dias' : 'Volume por dia no período'),
    h('p', { class: 'faint' }, 'Presença por dia (vistas por 100 checagens da rede); passe o mouse para ver as vistas. Dias UTC.'),
    dayBars(series.map((r) => ({ day: r.day, value: r.presence,
      title: day(r.day) + ': presença ' + presence(r.presence) + ' · ' + num(r.sightings) + ' vistas · ' + num(r.creatives) + ' criativos' }))));

  const creatives = h('section', { class: 'panel' }, h('h3', {}, 'Criativos com mais presença'),
    out.creatives.length ? h('div', { class: 'sp-cards' }, out.creatives.map((c) => h('a', { class: 'sp-card', href: '/spy/ads/' + c.id + keepRange() },
      c.image_url ? h('img', { src: c.image_url, alt: '', loading: 'lazy', referrerpolicy: 'no-referrer' }) : h('div', { class: 'sp-noimg' }),
      h('div', { class: 'sp-card-body' },
        h('div', { class: 'sp-card-head' }, c.headline || 'Sem headline'),
        h('div', { class: 'sp-card-sub' }, [c.vertical_name, c.running ? 'no ar' : 'saiu'].filter(Boolean).join(' · ')),
        h('div', { class: 'sp-card-foot' }, h('span', {}, 'Presença ', h('b', { class: 'num' }, presence(c.presence))), h('span', { class: 'num' }, momentum(c.momentum))))))) : h('p', { class: 'empty' }, 'Nenhum criativo neste período.'));

  const campaigns = h('section', { class: 'panel' }, h('h3', {}, 'Campanhas e marcas'), table([
    ['Campanha', (r) => h('span', {}, r.name || r.external_id, h('div', { class: 'faint mono' }, r.account || ''))],
    ['Marcas', (r) => (r.brands || []).join(', ') || '–'],
    ['Criativos', (r) => num(r.creatives), 'num'],
    ['Vistas', (r) => num(r.sightings), 'num'],
    ['Última vez', (r) => when(r.last_seen_at)],
  ], out.campaigns, 'Nenhuma campanha vista neste período.'));
  const accounts = h('section', { class: 'panel' }, h('h3', {}, 'Contas'), table([
    ['Conta', (r) => h('span', { class: 'mono' }, r.external_id)],
    ['Por que está aqui', (r) => why(r.reason, r.agency)],
    ['Organização', (r) => h('span', { class: 'mono' }, r.org_external_id || '–')],
    ['Última vez', (r) => when(r.last_seen_at)],
  ], out.accounts, 'Nenhuma conta ligada.'));
  const sites = h('section', { class: 'panel' }, h('h3', {}, 'Sites'), table([
    ['Site', (r) => h('span', { class: 'mono' }, r.domain || '–')],
    ['Por que está aqui', (r) => why(r.reason)],
  ], out.sites, 'Nenhum site de landing page ligado ainda.'));
  const brands = h('section', { class: 'panel' }, h('h3', {}, 'Marcas'), table([
    ['Marca', (r) => r.name], ['Anúncios', (r) => num(r.ads), 'num'], ['Vistas', (r) => num(r.sightings), 'num'],
  ], out.brands));
  const pubs = h('section', { class: 'panel' }, h('h3', {}, 'Publishers'), table([
    ['Publisher', (r) => link('/spy/publishers/' + r.id + keepRange(), r.name)], ['Dispositivo', (r) => r.device], ['Vistas', (r) => num(r.sightings), 'num'],
  ], out.publishers));
  fill(body, head, h('div', { style: 'margin-top:14px' }, drawMarks(out.mark, out.notices)),
    h('div', { style: 'margin-top:14px' }, volume),
    h('div', { style: 'margin-top:14px' }, creatives),
    h('div', { style: 'margin-top:14px' }, campaigns),
    h('div', { class: 'sp-grid', style: 'margin-top:14px' }, pubs, brands),
    h('div', { class: 'sp-grid', style: 'margin-top:14px' }, accounts, sites));
}

main.append(
  h('nav', { class: 'crumbs' }, link('/spy/operators/' + keepRange(), 'Operadores'), h('span', { class: 'sep' }, '/'), h('span', { 'aria-current': 'page' }, 'Operador')),
  h('div', { class: 'page-head' }, h('h1', {}, 'Operador')),
  h('div', { class: 'sp-toolbar' }, rangePicker(draw), h('div', { class: 'sp-tools' }, comparePicker(draw), h('span', { class: 'sp-count sp-window' }))),
  h('div', { class: 'sp-body' }));
draw();
