// Pedidos: changes another service asked for (Desk, for the person it works
// for). Nothing is sent until someone here confirms; then Launch makes the
// change like any other, recorded in History as asked by the request.
import { api, h, note, link, crumbs, date, money, badge, busy, plural } from './lib.js';
import { doneNote } from './tree.js';

const KINDS = {
  pause: 'Pausar campanhas',
  pause_ads: 'Pausar anúncios',
  change: 'Mudar campanhas',
  duplicate: 'Duplicar campanhas',
  move: 'Mudar de grupo',
};
const STATES = { waiting: ['esperando', 'paused'], confirmed: ['enviando', 'review'], sent: ['feito', 'running'], refused: ['recusado', ''], failed: ['falhou', 'rejected'] };
const ORIGINALS = { when_started: 'pausar a original quando a cópia começar', now: 'pausar a original agora', leave: 'deixar a original como está' };

function asker(origin) {
  const [who] = (origin || '').split(':');
  return { desk: 'Desk', intel: 'Intel' }[who] || origin;
}

function stateBadge(state) {
  const [label, cls] = STATES[state] || [state, ''];
  return h('span', { class: 'badge ' + cls }, label);
}

// what says in one line what a request asks.
function what(r) {
  const i = r.input || {};
  const n = (i.campaigns || []).length;
  switch (r.kind) {
    case 'pause_ads': return `Pausar ${plural((i.ads || []).length, 'anúncio', 'anúncios')} da campanha ${(i.campaigns || [])[0] || ''}`;
    case 'change': return `Mudar ${plural(n, 'campanha', 'campanhas')}: ${changeText(i.change || {})}`;
    default: return `${KINDS[r.kind] || r.kind}: ${plural(n, 'campanha', 'campanhas')}`;
  }
}

function changeText(c) {
  return [c.cpc && 'CPC ' + money(c.cpc), c.daily_cap && 'teto diário ' + money(c.daily_cap), c.spending_limit && 'limite total ' + money(c.spending_limit), c.name && 'nome “' + c.name + '”'].filter(Boolean).join(', ') || 'nada';
}

export async function requests({ main }) {
  main.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Pedidos'),
    h('p', { class: 'lead muted' }, 'Mudanças que o Desk pediu. Nada é enviado até alguém confirmar aqui.'))));
  const { requests: list } = await api('requests');
  if (!list.length) {
    main.append(h('p', { class: 'empty' }, 'Nenhum pedido ainda.'));
    return;
  }
  main.append(h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
    h('thead', {}, h('tr', {}, h('th', {}, 'Quando'), h('th', {}, 'O quê'), h('th', {}, 'Para'), h('th', {}, 'Estado'))),
    h('tbody', {}, list.map((r) => h('tr', {},
      h('td', { class: 'muted nowrap' }, date(r.made_at)),
      h('td', {}, h('a', { href: '/launch/requests/' + r.id }, what(r)), h('div', { class: 'faint' }, 'pedido pelo ' + asker(r.origin) + ' · ' + r.origin)),
      h('td', { class: 'muted' }, (r.requested_by || '—').split('@')[0]),
      h('td', {}, stateBadge(r.state), r.confirmed_by ? h('div', { class: 'faint' }, r.confirmed_by.split('@')[0]) : null)))))));
}

export async function request({ main, route }) {
  const { request: r } = await api('requests/' + encodeURIComponent(route.id));
  const i = r.input || {};
  const base = `${i.network}/${encodeURIComponent(i.account)}`;
  main.append(crumbs([['Pedidos', '/launch/requests'], ['#' + r.id]]));
  main.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, KINDS[r.kind] || r.kind),
    h('p', { class: 'muted' }, 'Pedido pelo ', asker(r.origin), r.requested_by ? ' para ' + r.requested_by : '', ' · ', date(r.made_at), ' · ', stateBadge(r.state)))));

  // The campaigns as they are now, so the person sees what would change.
  const camps = await Promise.all((i.campaigns || []).map((id) =>
    api(`${base}/campaigns/${encodeURIComponent(id)}`).then((d) => d).catch((e) => ({ error: e.message, campaign: { id } }))));
  const byId = new Map(camps.map((d) => [d.campaign.id, d.campaign]));
  const rows = camps.map((d) => {
    const c = d.campaign;
    const s = c.settings || {};
    return h('tr', {},
      h('td', {}, d.error ? h('span', { class: 'mono' }, c.id) : h('a', { href: link(i.network, i.account, c.group_id || '-', c.id) }, c.name), d.error ? h('div', { class: 'warn-line' }, d.error) : null),
      h('td', {}, d.error ? '' : badge(c.status)),
      h('td', {}, money(s.cpc)), h('td', {}, money(s.daily_cap)),
      h('td', { class: 'mono faint' }, c.id));
  });
  main.append(h('section', { class: 'panel' }, h('h2', {}, 'O pedido'),
    h('p', {}, what(r)),
    r.kind === 'move' ? h('p', { class: 'muted' }, 'Para o grupo ', h('span', { class: 'mono' }, i.to_group || '?'), ', ', ORIGINALS[i.originals || 'when_started'] || i.originals, '. O Launch cria uma cópia pausada, com outro id.') : null,
    r.kind === 'duplicate' ? h('p', { class: 'muted' }, 'Cada campanha ganha uma cópia pausada, com os mesmos anúncios.') : null,
    r.kind === 'pause_ads' ? h('ul', { class: 'lib-headlines' }, (i.ads || []).map((id) => {
      const a = (camps[0]?.ads || []).find((x) => x.id === id);
      return h('li', {}, a ? a.title + ' ' : 'não está mais na campanha ', a ? badge(a.status) : null, ' ', h('span', { class: 'mono faint' }, id));
    })) : null,
    h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
      h('thead', {}, h('tr', {}, h('th', {}, 'Campanha'), h('th', {}, 'Estado'), h('th', {}, 'CPC hoje'), h('th', {}, 'Teto diário hoje'), h('th', {}, 'Id'))),
      h('tbody', {}, rows)))));

  const out = h('div');
  if (r.state === 'waiting') {
    const decide = (button, how) => busy(button, out, async () => {
      const res = await api(`requests/${r.id}/${how}`, { method: 'POST' });
      box.remove();
      out.replaceChildren(how === 'refuse' ? note('ok', 'Pedido recusado. Nada foi enviado.') :
        res.done?.length ? (r.kind === 'pause_ads' ? adsDone(res.done) : doneNote(res.done, ['feita', 'feitas'], byId)) : note('ok', 'Pedido enviado.'));
    });
    const yes = h('button', { type: 'button', class: 'primary', onclick: () => decide(yes, 'confirm') }, 'Confirmar e enviar');
    const no = h('button', { type: 'button', onclick: () => decide(no, 'refuse') }, 'Recusar');
    const box = h('div', { class: 'actions' }, yes, no);
    main.append(box);
  } else {
    main.append(note(r.state === 'failed' ? 'fail' : '', r.confirmed_by ? (r.state === 'refused' ? 'Recusado por ' : 'Confirmado por ') + r.confirmed_by : '',
      r.decided_at ? ' · ' + date(r.decided_at) : '', r.result?.error ? ' · ' + r.result.error : ''));
    if (r.result?.done?.length) main.append(r.kind === 'pause_ads' ? adsDone(r.result.done) : doneNote(r.result.done, ['feita', 'feitas'], byId));
  }
  main.append(out);
}

function adsDone(done) {
  const ok = done.filter((d) => !d.error);
  return h('div', {},
    ok.length ? note('ok', plural(ok.length, 'anúncio pausado', 'anúncios pausados') + ': ' + ok.map((d) => d.ad).join(', ')) : null,
    ...done.filter((d) => d.error).map((d) => note('fail', d.ad + ': ' + d.error)));
}
