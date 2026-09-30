// The home page (accounts) and the tree: an account's groups with their
// campaigns, or one group's. Campaigns are picked with their pair and then
// moved, copied, paused or changed together.
import { api, h, note, money, badge, link, crumbs, field, input, select, segmented, busy, numberOf, plural, DEVICES, store } from './lib.js';
import { groupForm } from './presets.js';

const NETS = { taboola: 'Taboola', newsbreak: 'NewsBreak' };

export async function home({ main, status }) {
  main.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Campanhas'),
    h('p', { class: 'lead muted' }, 'Escolha a conta. Tudo o que o Launch cria nasce pausado: você liga no próprio Taboola.')),
  h('div', { class: 'actions' }, h('a', { class: 'button primary', href: '/launch/new' }, 'Novo par'))));
  const all = [];
  for (const n of status.networks || []) {
    if (!n.connected) {
      main.append(note('warn', h('b', {}, (NETS[n.name] || n.name) + ' desligado. '), n.reason || '',
        ' Em Novo par você ainda monta os anúncios e baixa a planilha para subir à mão.'));
      continue;
    }
    try {
      const { accounts } = await api(`accounts/${n.name}`);
      for (const a of accounts) all.push({ net: n.name, ...a });
    } catch (e) {
      main.append(note('fail', `${NETS[n.name] || n.name}: ${e.message}`));
    }
  }
  if (all.length === 1 && !status.error) {
    location.replace(link(all[0].net, all[0].id));
    return;
  }
  if (!all.length) {
    if (!(status.networks || []).length) main.append(note('', 'Nenhuma rede ligada ao Launch.'));
    return;
  }
  const last = store('launch.account');
  main.append(h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
    h('thead', {}, h('tr', {}, h('th', {}, 'Conta'), h('th', {}, 'Rede'), h('th', {}, 'Id'))),
    h('tbody', {}, all.map((a) => h('tr', {},
      h('td', {}, h('a', { href: link(a.net, a.id) }, a.name || a.id), last === a.net + '/' + a.id ? h('span', { class: 'faint' }, '  · a última aberta') : null),
      h('td', {}, NETS[a.net] || a.net),
      h('td', { class: 'mono' }, a.id)))))));
}

// status groups for the filter.
const SHOW = [
  ['all', 'Todas'],
  ['running', 'Rodando'],
  ['paused', 'Pausadas'],
  ['other', 'Outras'],
];

function shows(c, which) {
  if (which === 'all') return true;
  if (which === 'running') return c.status === 'RUNNING';
  if (which === 'paused') return c.status === 'PAUSED';
  return c.status !== 'RUNNING' && c.status !== 'PAUSED';
}

export async function tree(ctx) {
  const { main, aside, route, status } = ctx;
  const { net, account } = route;
  const accounts = await api(`accounts/${net}`).then((r) => r.accounts).catch(() => []);
  const acct = accounts.find((a) => a.id === account) || { id: account, name: account };
  store('launch.account', net + '/' + account);
  const data = await api(`${net}/${encodeURIComponent(account)}/tree`);
  const groups = new Map(data.groups.map((g) => [g.id, g]));
  const pairOf = new Map();
  for (const p of data.pairs) {
    if (p.desktop_id) pairOf.set(p.desktop_id, p);
    if (p.mobile_id) pairOf.set(p.mobile_id, p);
  }
  const byId = new Map(data.campaigns.map((c) => [c.id, c]));
  const one = route.group !== undefined ? groups.get(route.group) || { id: route.group, name: route.group ? 'Grupo ' + route.group : 'Sem grupo' } : null;

  // ---- filters ----
  const f = { show: store('launch.show') || 'all', device: 'all', text: '' };
  const textBox = input({ type: 'search', placeholder: 'nome ou id', 'aria-label': 'Filtrar campanhas', oninput: (e) => { f.text = e.target.value.trim().toLowerCase(); draw(); } });
  aside.append(
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Conta'),
      select(accounts.map((a) => [a.id, a.name || a.id]), account, { 'aria-label': 'Conta', onchange: (e) => location.assign(link(net, e.target.value)) })),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Busca'), textBox),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Estado'),
      h('div', { class: 'chips' }, SHOW.map(([v, label]) => h('label', { class: 'chip' },
        h('input', { type: 'radio', name: 'show', value: v, checked: f.show === v, onchange: () => { f.show = v; store('launch.show', v); draw(); } }), h('span', {}, label))))),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Dispositivo'),
      h('div', { class: 'chips' }, [['all', 'Todos'], ['desktop', 'Desktop'], ['mobile', 'Mobile']].map(([v, label]) => h('label', { class: 'chip' },
        h('input', { type: 'radio', name: 'device', value: v, checked: f.device === v, onchange: () => { f.device = v; draw(); } }), h('span', {}, label))))),
  );
  if (!one) {
    aside.append(h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Grupos'),
      h('ul', { class: 'group-links' }, data.groups.map((g) => h('li', {}, h('a', { href: link(net, account, g.id) }, g.name || g.id))))));
  }

  // ---- head ----
  const trail = [['Launch', '/launch/'], [NETS[net] || net, '/launch/'], [acct.name || account, one ? link(net, account) : null]];
  if (one) trail.push([one.name || one.id]);
  const newHref = `/launch/new?net=${net}&account=${encodeURIComponent(account)}` + (one?.id ? `&group=${encodeURIComponent(one.id)}` : '');
  const groupBox = h('div');
  main.append(crumbs(trail), h('div', { class: 'page-head' },
    h('div', {}, h('h1', {}, one ? one.name || one.id : acct.name || account),
      one ? h('p', { class: 'muted' }, one.status ? badge(one.status) : null, ' ', budgetLine(one)) : h('p', { class: 'muted' }, plural(data.groups.length, 'grupo', 'grupos') + ' · ' + plural(data.campaigns.length, 'campanha', 'campanhas'))),
    h('div', { class: 'actions' },
      one ? null : h('button', { type: 'button', class: 'ghost', onclick: () => { groupBox.replaceChildren(groupForm({ net, account, onMade: (g) => location.assign(link(net, account, g.id)) })); } }, 'Novo grupo'),
      h('a', { class: 'button primary', href: newHref }, one ? 'Nova campanha neste grupo' : 'Nova campanha'))),
  groupBox);

  // ---- moves waiting for their copies to start ----
  const movesBox = h('div');
  main.append(movesBox);
  function drawMoves() {
    if (!data.moves.length) {
      movesBox.replaceChildren();
      return;
    }
    movesBox.replaceChildren(h('div', { class: 'panel moves-panel' }, h('h3', {}, 'Mudanças de grupo esperando'),
      h('p', { class: 'muted' }, 'A cópia já está no grupo novo, pausada. Quando alguém ligar a cópia no Taboola, o Launch pausa a original.'),
      h('ul', { class: 'moves' }, data.moves.map((m) => {
        const from = byId.get(m.from_campaign);
        const to = byId.get(m.to_campaign);
        const out = h('span');
        const btn = h('button', { type: 'button', class: 'small ghost', onclick: () => busy(btn, out, async () => { await api(`moves/${m.id}/cancel`, { method: 'POST' }); location.reload(); }) }, 'Não pausar a original');
        return h('li', {}, (from?.name || m.from_campaign) + ' → ' + (groups.get(m.to_group)?.name || m.to_group) + ' ',
          to ? h('a', { href: link(net, account, to.group_id || '-', to.id) }, '(cópia ' + to.id + ')') : '(cópia ' + m.to_campaign + ')', ' ', btn, out);
      }))));
  }

  // ---- the table ----
  const chosen = new Set();
  const together = { on: store('launch.together') ?? true };
  const table = h('div', { class: 'table-wrap' });
  const bar = h('div', { class: 'select-bar', hidden: true });
  const result = h('div', { class: 'result' });
  main.append(result, table, bar);

  const visible = (c) => shows(c, f.show) && (f.device === 'all' || c.device === f.device || c.device === 'both') &&
    (!f.text || c.name.toLowerCase().includes(f.text) || c.id.includes(f.text) || (pairOf.get(c.id)?.name || '').toLowerCase().includes(f.text));

  function draw() {
    const inGroup = new Map();
    for (const c of data.campaigns) {
      const g = c.group_id || '';
      if (one && g !== one.id) continue;
      if (!inGroup.has(g)) inGroup.set(g, []);
      inGroup.get(g).push(c);
    }
    const order = one ? [one.id] : [...data.groups.map((g) => g.id), ''];
    const rows = [];
    for (const gid of order) {
      const list = (inGroup.get(gid) || []).filter(visible);
      const g = groups.get(gid);
      if (!one) {
        if (!list.length && (f.text || f.show !== 'all' || f.device !== 'all' || !g)) continue;
        rows.push(h('tr', { class: 'group-row' }, h('td', {}),
          h('td', { colspan: 5 }, h('a', { href: link(net, account, gid || '-') }, g ? g.name || g.id : 'Sem grupo'), ' ',
            g?.status ? badge(g.status) : null, ' ', h('span', { class: 'faint' }, g ? budgetLine(g) : '', ' · ', plural((inGroup.get(gid) || []).length, 'campanha', 'campanhas')))));
      }
      if (!list.length) {
        rows.push(h('tr', { class: 'sub' }, h('td', {}), h('td', { colspan: 5, class: 'faint' }, one && !(inGroup.get(gid) || []).length ? 'Nenhuma campanha neste grupo.' : 'Nenhuma campanha com esses filtros.')));
        continue;
      }
      for (const c of sortPairs(list, pairOf)) rows.push(campaignRow(c));
    }
    table.replaceChildren(h('table', { class: 'list tree' },
      h('thead', {}, h('tr', {}, h('th', {}, ''), h('th', {}, 'Campanha'), h('th', {}, 'Dispositivo'), h('th', {}, 'Estado'), h('th', { class: 'num' }, 'CPC'), h('th', { class: 'num' }, 'Teto diário'))),
      h('tbody', {}, rows)));
    drawBar();
  }

  function campaignRow(c) {
    const p = pairOf.get(c.id);
    const box = h('input', { type: 'checkbox', 'aria-label': 'Escolher ' + c.name, checked: chosen.has(c.id), onchange: (e) => pick(c, e.target.checked) });
    return h('tr', { class: chosen.has(c.id) ? 'chosen' : '' },
      h('td', { class: 'pick' }, box),
      h('td', {}, h('a', { href: link(net, account, c.group_id || '-', c.id) }, c.name), p ? h('span', { class: 'badge pair', title: 'Par criado pelo Launch: ' + p.name }, 'par') : null,
        h('div', { class: 'faint mono' }, c.id)),
      h('td', {}, DEVICES[c.device] || '—'),
      h('td', {}, badge(c.status)),
      h('td', { class: 'num' }, money(c.settings.cpc)),
      h('td', { class: 'num' }, money(c.settings.daily_cap)));
  }

  function twin(id) {
    const p = pairOf.get(id);
    if (!p) return '';
    return p.desktop_id === id ? p.mobile_id : p.desktop_id;
  }

  function pick(c, on) {
    const ids = [c.id];
    if (together.on && twin(c.id) && byId.has(twin(c.id))) ids.push(twin(c.id));
    for (const id of ids) on ? chosen.add(id) : chosen.delete(id);
    panel.replaceChildren();
    draw();
  }

  // ---- what to do with the chosen ones ----
  const panel = h('div', { class: 'act-panel' });
  function drawBar() {
    bar.hidden = chosen.size === 0;
    if (!chosen.size) {
      panel.replaceChildren();
      return;
    }
    bar.replaceChildren(
      h('span', { class: 'count' }, plural(chosen.size, 'campanha escolhida', 'campanhas escolhidas')),
      h('label', { class: 'together' }, h('input', { type: 'checkbox', checked: together.on, onchange: (e) => { together.on = e.target.checked; store('launch.together', together.on); } }), ' escolher o par junto'),
      h('div', { class: 'actions' },
        h('button', { type: 'button', onclick: () => showMove() }, 'Mudar de grupo'),
        h('button', { type: 'button', onclick: () => showDuplicate() }, 'Duplicar'),
        h('a', { class: 'button', href: `/launch/new?net=${net}&account=${encodeURIComponent(account)}&to=${ids().join(',')}` }, 'Adicionar anúncios'),
        h('button', { type: 'button', onclick: () => showChange() }, 'Mudar lance e tetos'),
        h('button', { type: 'button', onclick: () => showPause() }, 'Pausar'),
        h('button', { type: 'button', class: 'ghost', onclick: () => { chosen.clear(); draw(); } }, 'Limpar')),
      panel);
  }

  const ids = () => [...chosen];
  const names = () => ids().map((id) => byId.get(id)?.name || id);

  async function send(button, out, path, body, say) {
    await busy(button, out, async () => {
      const res = await api(`${net}/${encodeURIComponent(account)}/${path}`, { method: 'POST', body });
      result.replaceChildren(doneNote(res.done, say, byId));
      chosen.clear();
      // The tree changed: read it again.
      const fresh = await api(`${net}/${encodeURIComponent(account)}/tree`);
      data.campaigns = fresh.campaigns;
      data.pairs = fresh.pairs;
      data.moves = fresh.moves;
      byId.clear();
      for (const c of data.campaigns) byId.set(c.id, c);
      pairOf.clear();
      for (const p of data.pairs) {
        if (p.desktop_id) pairOf.set(p.desktop_id, p);
        if (p.mobile_id) pairOf.set(p.mobile_id, p);
      }
      draw();
      drawMoves();
      result.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    });
  }

  function showMove() {
    const out = h('div');
    const to = select([['', 'Escolha o grupo…'], ...data.groups.map((g) => [g.id, g.name || g.id])], '', { 'aria-label': 'Grupo de destino' });
    let originals = 'when_started';
    const go = h('button', { type: 'button', class: 'primary', onclick: () => {
      if (!to.value) { out.replaceChildren(note('fail', 'Escolha o grupo de destino.')); return; }
      send(go, out, 'move', { campaigns: ids(), to_group: to.value, originals }, ['copiada para o grupo novo, pausada', 'copiadas para o grupo novo, pausadas']);
    } }, 'Mudar ' + plural(chosen.size, 'campanha', 'campanhas'));
    panel.replaceChildren(h('div', { class: 'panel' }, h('h3', {}, 'Mudar de grupo'),
      h('p', { class: 'muted' }, 'O Taboola não muda o grupo de uma campanha. O Launch cria uma cópia pausada, com os mesmos anúncios, no grupo novo. A cópia tem outro id.'),
      h('div', { class: 'fields' }, field('Para o grupo', to)),
      h('p', {}, 'E a original?'),
      segmented('originals', [['when_started', 'Pausar quando a cópia começar'], ['now', 'Pausar agora'], ['leave', 'Deixar como está']], originals, (v) => { originals = v; }),
      h('p', { class: 'faint' }, names().join(' · ')),
      h('div', { class: 'actions' }, go), out));
  }

  function showDuplicate() {
    const out = h('div');
    const go = h('button', { type: 'button', class: 'primary', onclick: () => send(go, out, 'duplicate', { campaigns: ids() }, ['duplicada, pausada', 'duplicadas, pausadas']) }, 'Duplicar ' + plural(chosen.size, 'campanha', 'campanhas'));
    panel.replaceChildren(h('div', { class: 'panel' }, h('h3', {}, 'Duplicar'),
      h('p', { class: 'muted' }, 'Cada cópia nasce pausada no mesmo grupo, com os mesmos anúncios, e o nome ganha “(cópia)”.'),
      h('p', { class: 'faint' }, names().join(' · ')), h('div', { class: 'actions' }, go), out));
  }

  function showPause() {
    const out = h('div');
    const go = h('button', { type: 'button', class: 'primary', onclick: () => send(go, out, 'pause', { campaigns: ids() }, ['pausada', 'pausadas']) }, 'Pausar ' + plural(chosen.size, 'campanha', 'campanhas'));
    panel.replaceChildren(h('div', { class: 'panel' }, h('h3', {}, 'Pausar'),
      h('p', { class: 'muted' }, 'Para de gastar na hora. Para ligar de novo, use o próprio Taboola.'),
      h('p', { class: 'faint' }, names().join(' · ')), h('div', { class: 'actions' }, go), out));
  }

  function showChange() {
    const out = h('div');
    const lim = status.limits || {};
    const cpc = input({ inputmode: 'decimal', placeholder: 'fica igual' });
    const cap = input({ inputmode: 'decimal', placeholder: 'fica igual' });
    const limit = input({ inputmode: 'decimal', placeholder: 'fica igual' });
    const go = h('button', { type: 'button', class: 'primary', onclick: () => {
      const change = { cpc: numberOf(cpc.value), daily_cap: numberOf(cap.value), spending_limit: numberOf(limit.value) };
      if ([change.cpc, change.daily_cap, change.spending_limit].some(Number.isNaN)) { out.replaceChildren(note('fail', 'Use só números, como 0,35.')); return; }
      if (!change.cpc && !change.daily_cap && !change.spending_limit) { out.replaceChildren(note('fail', 'Diga o que mudar.')); return; }
      for (const k of Object.keys(change)) if (!change[k]) delete change[k];
      send(go, out, 'change', { campaigns: ids(), change }, ['mudada', 'mudadas']);
    } }, 'Mudar ' + plural(chosen.size, 'campanha', 'campanhas'));
    panel.replaceChildren(h('div', { class: 'panel' }, h('h3', {}, 'Mudar lance e tetos'),
      h('div', { class: 'fields' },
        field('CPC (US$)', cpc, lim.max_cpc ? 'até ' + money(lim.max_cpc) : null),
        field('Teto diário (US$)', cap, lim.max_daily_cap ? 'até ' + money(lim.max_daily_cap) : null),
        field('Limite total (US$)', limit)),
      h('p', { class: 'faint' }, names().join(' · ')), h('div', { class: 'actions' }, go), out));
  }

  draw();
  drawMoves();
}

// sortPairs keeps a pair's two campaigns next to each other, desktop first.
export function sortPairs(list, pairOf) {
  const seen = new Set();
  const out = [];
  const byId = new Map(list.map((c) => [c.id, c]));
  for (const c of list) {
    if (seen.has(c.id)) continue;
    const p = pairOf.get(c.id);
    const two = p ? [p.desktop_id, p.mobile_id].map((id) => byId.get(id)).filter(Boolean) : [c];
    if (!two.includes(c)) two.unshift(c);
    for (const x of two) {
      if (seen.has(x.id)) continue;
      seen.add(x.id);
      out.push(x);
    }
  }
  return out;
}

export function budgetLine(g) {
  if (!g) return '';
  const per = { DAILY: ' por dia', MONTHLY: ' por mês', ENTIRE: ' no total' }[g.budget_model];
  return per && g.budget ? 'orçamento ' + money(g.budget) + per : 'orçamento por campanha';
}

// doneNote says what an action did to each campaign.
// say is what happened to one campaign, and to several: ['pausada', 'pausadas'].
export function doneNote(done, say, byId) {
  const ok = done.filter((d) => !d.error);
  const bad = done.filter((d) => d.error);
  return h('div', {},
    ok.length ? note('ok', h('b', {}, plural(ok.length, 'campanha', 'campanhas') + ' ' + say[ok.length === 1 ? 0 : 1] + '. '),
      ok.map((d) => (byId.get(d.campaign)?.name || d.campaign) + (d.copy ? ' → ' + d.copy.id : '')).join(' · ')) : null,
    bad.map((d) => note('fail', h('b', {}, (byId.get(d.campaign)?.name || d.campaign) + ': '), d.error)));
}
