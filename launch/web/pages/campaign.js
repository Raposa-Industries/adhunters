// One campaign: its settings, its ads, its pair's other half and what
// Launch did to it. Its actions take the pair along when asked.
import { api, h, note, money, badge, link, crumbs, field, input, select, segmented, busy, numberOf, DEVICES, stateName } from './lib.js';
import { doneNote, budgetLine } from './tree.js';
import { historyTable } from './history.js';
import { OBJECTIVES } from './presets.js';

export async function campaign({ main, route, status }) {
  const { net, account } = route;
  const base = `${net}/${encodeURIComponent(account)}`;
  const [data, tree, accounts] = await Promise.all([
    api(`${base}/campaigns/${encodeURIComponent(route.campaign)}`),
    api(`${base}/tree`).catch(() => ({ groups: [] })),
    api(`accounts/${net}`).then((r) => r.accounts).catch(() => []),
  ]);
  const c = data.campaign;
  const g = tree.groups.find((x) => x.id === c.group_id);
  const acct = accounts.find((a) => a.id === account) || { id: account, name: account };
  const s = c.settings || {};

  main.append(crumbs([['Launch', '/launch/'], [acct.name || account, link(net, account)], [g ? g.name || g.id : 'Sem grupo', link(net, account, c.group_id || '-')], [c.name]]));
  const out = h('div');
  const panel = h('div');
  let together = !!data.twin;
  const ids = () => (together && data.twin ? [c.id, data.twin.id] : [c.id]);
  const byId = new Map([[c.id, c], ...(data.twin ? [[data.twin.id, data.twin]] : [])]);

  async function post(button, path, body, say) {
    await busy(button, out, async () => {
      const res = await api(`${base}/${path}`, { method: 'POST', body: { campaigns: ids(), ...body } });
      out.replaceChildren(doneNote(res.done, say, byId), h('p', {}, h('a', { href: location.pathname }, 'Recarregar a campanha')));
      panel.replaceChildren();
    });
  }

  const pauseBtn = h('button', { type: 'button', onclick: () => post(pauseBtn, 'pause', {}, ['pausada', 'pausadas']) }, 'Pausar');
  const dupBtn = h('button', { type: 'button', onclick: () => post(dupBtn, 'duplicate', {}, ['duplicada, pausada', 'duplicadas, pausadas']) }, 'Duplicar');
  main.append(h('div', { class: 'page-head' },
    h('div', {}, h('h1', {}, c.name),
      h('p', { class: 'muted' }, badge(c.status), ' ', DEVICES[c.device] || '', ' · ', h('span', { class: 'mono' }, c.id),
        data.pair ? h('span', {}, ' · ', h('span', { class: 'badge pair' }, 'par'), ' ', data.pair.name) : null)),
    h('div', { class: 'actions' },
      data.twin ? h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: together, onchange: (e) => { together = e.target.checked; } }), 'com o par') : null,
      h('button', { type: 'button', onclick: () => showMove() }, 'Mudar de grupo'),
      dupBtn,
      h('button', { type: 'button', onclick: () => showChange() }, 'Mudar'),
      pauseBtn)),
  panel, out);

  if (data.twin) {
    const t = data.twin;
    main.append(note('', 'A outra metade do par: ', h('a', { href: link(net, account, t.group_id || '-', t.id) }, t.name), ' · ', stateName(t.status), ' · ', DEVICES[t.device] || ''));
  }

  const obj = OBJECTIVES.find(([k]) => k === (s.objective || c.objective))?.[1] || s.objective || c.objective || '—';
  main.append(h('section', { class: 'panel' }, h('h2', {}, 'Configurações'),
    h('dl', { class: 'summary' },
      h('dt', {}, 'CPC'), h('dd', {}, money(s.cpc), s.bid_strategy ? ' · ' + (s.bid_strategy === 'SMART' ? 'Smart' : 'fixo') : ''),
      h('dt', {}, 'Teto diário'), h('dd', {}, money(s.daily_cap)),
      h('dt', {}, 'Limite total'), h('dd', {}, money(s.spending_limit)),
      h('dt', {}, 'Grupo'), h('dd', {}, g ? g.name + ' · ' + budgetLine(g) : c.group_id || '—'),
      h('dt', {}, 'Países'), h('dd', {}, (s.countries || []).join(', ') || 'todos'),
      h('dt', {}, 'Objetivo'), h('dd', {}, obj),
      h('dt', {}, 'Marca'), h('dd', {}, s.brand || '—'),
      h('dt', {}, 'Tracking code'), h('dd', { class: 'mono wrap' }, s.tracking_code || '—'),
      h('dt', {}, 'Datas'), h('dd', {}, [s.start_date, s.end_date].filter(Boolean).join(' até ') || 'sem datas'))));

  main.append(h('section', { class: 'panel' }, h('h2', {}, 'Anúncios · ' + data.ads.length),
    data.ads.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
      h('thead', {}, h('tr', {}, h('th', {}, ''), h('th', {}, 'Headline'), h('th', {}, 'Botão'), h('th', {}, 'Estado'), h('th', {}, 'Revisão'), h('th', {}, 'Id'))),
      h('tbody', {}, data.ads.map((a) => h('tr', {},
        h('td', {}, a.image_url ? h('img', { class: 'mini', src: a.image_url, alt: '', loading: 'lazy', referrerpolicy: 'no-referrer' }) : null),
        h('td', {}, a.title, a.ai ? h('span', { class: 'badge' }, 'IA') : null, a.description ? h('div', { class: 'faint' }, a.description) : null),
        h('td', {}, a.cta ? a.cta.toLowerCase().replace(/_/g, ' ') : '—'),
        h('td', {}, badge(a.status)),
        h('td', {}, a.approval ? badge(a.approval) : '—'),
        h('td', { class: 'mono faint' }, a.ad_id || a.id)))))) : h('p', { class: 'empty' }, 'Sem anúncios.')));

  main.append(h('section', { class: 'panel' }, h('h2', {}, 'Histórico'),
    data.history.length ? historyTable(data.history, false) : h('p', { class: 'faint' }, 'Nada feito pelo Launch nesta campanha ainda.')));

  function showMove() {
    const msg = h('div');
    const to = select([['', 'Escolha o grupo…'], ...tree.groups.filter((x) => x.id !== c.group_id).map((x) => [x.id, x.name || x.id])], '', { 'aria-label': 'Grupo de destino' });
    let originals = 'when_started';
    const go = h('button', { type: 'button', class: 'primary', onclick: () => {
      if (!to.value) { msg.replaceChildren(note('fail', 'Escolha o grupo de destino.')); return; }
      post(go, 'move', { to_group: to.value, originals }, ['copiada para o grupo novo, pausada', 'copiadas para o grupo novo, pausadas']);
    } }, 'Mudar de grupo');
    panel.replaceChildren(h('div', { class: 'panel' }, h('h3', {}, 'Mudar de grupo'),
      h('p', { class: 'muted' }, 'O Launch cria uma cópia pausada, com os mesmos anúncios, no grupo novo. A cópia tem outro id.'),
      h('div', { class: 'fields' }, field('Para o grupo', to)),
      h('p', {}, 'E a original?'),
      segmented('originals', [['when_started', 'Pausar quando a cópia começar'], ['now', 'Pausar agora'], ['leave', 'Deixar como está']], originals, (v) => { originals = v; }),
      h('div', { class: 'actions' }, go), msg));
  }

  function showChange() {
    const msg = h('div');
    const lim = status.limits || {};
    const name = input({ value: c.name });
    const cpc = input({ inputmode: 'decimal', value: s.cpc || '' });
    const cap = input({ inputmode: 'decimal', value: s.daily_cap || '' });
    const limit = input({ inputmode: 'decimal', value: s.spending_limit || '' });
    const go = h('button', { type: 'button', class: 'primary', onclick: () => {
      const change = {};
      if (name.value.trim() && name.value.trim() !== c.name) change.name = name.value.trim();
      for (const [k, box, was] of [['cpc', cpc, s.cpc], ['daily_cap', cap, s.daily_cap], ['spending_limit', limit, s.spending_limit]]) {
        const n = numberOf(box.value);
        if (Number.isNaN(n)) { msg.replaceChildren(note('fail', 'Use só números, como 0,35.')); return; }
        if (n && n !== was) change[k] = n;
      }
      if (!Object.keys(change).length) { msg.replaceChildren(note('fail', 'Nada mudou.')); return; }
      if (change.name && together && data.twin) { msg.replaceChildren(note('fail', 'Para mudar o nome, desmarque “com o par”: cada metade tem o seu.')); return; }
      post(go, 'change', { change }, ['mudada', 'mudadas']);
    } }, 'Salvar');
    panel.replaceChildren(h('div', { class: 'panel' }, h('h3', {}, 'Mudar'),
      h('p', { class: 'muted' }, 'Mudar um anúncio faz o Taboola revisá-lo de novo; aqui só mudam o nome, o lance e os tetos da campanha.'),
      h('div', { class: 'fields' }, field('Nome', name), field('CPC (US$)', cpc, lim.max_cpc ? 'até ' + money(lim.max_cpc) : null),
        field('Teto diário (US$)', cap, lim.max_daily_cap ? 'até ' + money(lim.max_daily_cap) : null), field('Limite total (US$)', limit)),
      h('div', { class: 'actions' }, go), msg));
  }
}
