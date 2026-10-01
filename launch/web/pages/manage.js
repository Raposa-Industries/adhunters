// The three main pages, like Realize's: Campaign Groups, Campaigns and Ads,
// each a table with the numbers of the period picked. Clicking a group opens
// its campaigns; clicking a campaign opens its ads. Each narrowing shows as
// a chip (× takes it off, back to all) and in the trail at the top. The
// numbers are Intel's (intel_api), never asked of Taboola here; the lists of
// groups, campaigns and ads are Taboola's, kept by the server for a moment.
//
// The address holds everything: /launch/<groups|campaigns|ads>?account=<id|all>
// &group=<id>&campaign=<id>&w=<today|yesterday|7d|30d>, so a link or a reload
// shows the same table.
import { api, h, note, money, badge, link, crumbs, select, input, field, plural, store, busy, numberOf, segmented, doneNote, DEVICES } from './lib.js';

export const LEVELS = {
  groups: 'Grupos de campanha',
  campaigns: 'Campanhas',
  ads: 'Anúncios',
};

export const WINDOWS = [['today', 'Hoje'], ['yesterday', 'Ontem'], ['7d', 'Últimos 7 dias'], ['30d', 'Últimos 30 dias']];

const NET = 'taboola';

// where reads the address into what the page shows.
export function where(search) {
  const q = new URLSearchParams(search);
  const w = WINDOWS.some(([v]) => v === q.get('w')) ? q.get('w') : store('launch.window') || '7d';
  return { account: q.get('account') || store('launch.acct') || 'all', group: q.get('group') || '', campaign: q.get('campaign') || '', w };
}

// href builds a page address, keeping the account and the period.
export function href(level, at, more = {}) {
  const q = new URLSearchParams();
  const v = { account: at.account, w: at.w, ...more };
  for (const k of ['account', 'group', 'campaign', 'w']) if (v[k]) q.set(k, v[k]);
  return `/launch/${level}?${q}`;
}

// ---- numbers ----

const int = new Intl.NumberFormat('pt-BR');
const pct = new Intl.NumberFormat('pt-BR', { style: 'percent', minimumFractionDigits: 2, maximumFractionDigits: 2 });
const usd = (v) => (v ? money(v) : '—');

function zero() {
  return { impressions: 0, clicks: 0, spent: 0, sales: 0, revenue: 0, profit: 0, has: false };
}

function add(sum, n) {
  if (!n) return sum;
  for (const k of ['impressions', 'clicks', 'spent', 'sales', 'revenue', 'profit']) sum[k] += n[k] || 0;
  sum.has = true;
  return sum;
}

// COLUMNS are the number columns every table shares, in Realize's order.
const COLUMNS = [
  ['spent', 'Gasto', (n) => usd(n.spent)],
  ['impressions', 'Impressões', (n) => (n.has ? int.format(n.impressions) : '—')],
  ['clicks', 'Cliques', (n) => (n.has ? int.format(n.clicks) : '—')],
  ['ctr', 'CTR', (n) => (n.impressions ? pct.format(n.clicks / n.impressions) : '—')],
  ['cpc', 'CPC médio', (n) => (n.clicks ? money(n.spent / n.clicks) : '—')],
  ['sales', 'Vendas', (n) => (n.has ? int.format(n.sales) : '—')],
  ['cpa', 'CPA', (n) => (n.sales ? money(Math.round((n.spent / n.sales) * 100) / 100) : '—')],
  ['revenue', 'Receita', (n) => usd(n.revenue)],
  ['profit', 'Lucro', (n) => (n.has ? h('span', { class: n.profit < 0 ? 'down' : n.profit > 0 ? 'up' : '' }, money(n.profit) || 'US$ 0') : '—')],
  ['roi', 'ROI', (n) => (n.spent && n.has ? pct.format(n.profit / n.spent) : '—')],
];

function sortValue(n, key) {
  switch (key) {
    case 'ctr': return n.impressions ? n.clicks / n.impressions : -1;
    case 'cpc': return n.clicks ? n.spent / n.clicks : -1;
    case 'cpa': return n.sales ? n.spent / n.sales : Infinity;
    case 'roi': return n.spent ? n.profit / n.spent : -Infinity;
    default: return n[key] ?? 0;
  }
}

const STATES = [['all', 'Todos'], ['running', 'Rodando'], ['paused', 'Pausados'], ['other', 'Outros']];
const RUNNING = new Set(['RUNNING', 'APPROVED']);
const PAUSED = new Set(['PAUSED', 'STOPPED']);

function inState(s, which) {
  if (which === 'all') return true;
  if (which === 'running') return RUNNING.has(s);
  if (which === 'paused') return PAUSED.has(s);
  return !RUNNING.has(s) && !PAUSED.has(s);
}

// ---- the page ----

export async function manage(ctx) {
  const { main, aside, route, status } = ctx;
  const level = route.level;
  const at = where(location.search);
  store('launch.window', at.w);
  const taboola = (status.networks || []).find((n) => n.name === NET);
  if (!taboola?.connected) {
    main.append(h('h1', {}, LEVELS[level]), note('warn', h('b', {}, 'Taboola desligado. '), taboola?.reason || '',
      ' Em Novo › Campanha você ainda monta os anúncios e baixa a planilha para subir à mão.'));
    return;
  }

  // Accounts, then every chosen account's groups and campaigns, and Intel's numbers.
  const accounts = (await api(`accounts/${NET}`)).accounts;
  if (at.account !== 'all' && !accounts.some((a) => a.id === at.account)) at.account = 'all';
  store('launch.acct', at.account);
  const chosen = at.account === 'all' ? accounts : accounts.filter((a) => a.id === at.account);
  const acctName = new Map(accounts.map((a) => [a.id, a.name || a.id]));
  const loading = note('', 'Lendo o Taboola…');
  main.append(loading);
  const trees = await Promise.all(chosen.map((a) => api(`${NET}/${encodeURIComponent(a.id)}/tree`).then((t) => ({ a, t }), (e) => ({ a, error: e.message }))));
  const nums = await api(`numbers?window=${at.w}` + (at.account === 'all' ? '' : '&accounts=' + encodeURIComponent(at.account))).catch(() => ({ available: false, campaigns: {}, ads: {} }));
  loading.remove();

  const groups = [];
  const campaigns = [];
  const pairOf = new Map();
  const moves = [];
  const problems = [];
  for (const { a, t, error } of trees) {
    if (error) { problems.push(`${a.name || a.id}: ${error}`); continue; }
    for (const g of t.groups) groups.push({ ...g, account: a.id });
    for (const c of t.campaigns) campaigns.push({ ...c, account: a.id });
    for (const m of t.moves || []) moves.push({ ...m, account: a.id });
    for (const p of t.pairs) {
      if (p.desktop_id) pairOf.set(p.desktop_id, p);
      if (p.mobile_id) pairOf.set(p.mobile_id, p);
    }
  }
  const groupById = new Map(groups.map((g) => [g.id, g]));
  const campById = new Map(campaigns.map((c) => [c.id, c]));
  const group = at.group ? groupById.get(at.group) || { id: at.group, name: at.group === '-' ? 'Sem grupo' : 'Grupo ' + at.group, account: at.account } : null;
  const campaign = at.campaign ? campById.get(at.campaign) || { id: at.campaign, name: 'Campanha ' + at.campaign, account: at.account } : null;
  const numsOf = (id) => add(zero(), nums.campaigns?.[id]);

  // ---- left column: account, period, state, device, search ----
  const f = { state: store('launch.state') || 'all', device: 'all', text: '' };
  const go = (more) => location.assign(href(level, { ...at, ...more }, { group: at.group, campaign: at.campaign, ...more }));
  aside.append(
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Conta'),
      select([['all', `Todas as contas (${accounts.length})`], ...accounts.map((a) => [a.id, a.name || a.id])], at.account,
        { 'aria-label': 'Conta', onchange: (e) => location.assign(href(level, { ...at, account: e.target.value })) })),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Período'),
      select(WINDOWS, at.w, { 'aria-label': 'Período', onchange: (e) => go({ w: e.target.value }) })),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Busca'),
      input({ type: 'search', placeholder: 'nome ou id', 'aria-label': 'Buscar na tabela', oninput: (e) => { f.text = e.target.value.trim().toLowerCase(); draw(); } })),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Estado'),
      h('div', { class: 'chips' }, STATES.map(([v, label]) => h('label', { class: 'chip' },
        h('input', { type: 'radio', name: 'state', value: v, checked: f.state === v, onchange: () => { f.state = v; store('launch.state', v); draw(); } }), h('span', {}, label))))),
  );
  if (level !== 'groups') {
    aside.append(h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Dispositivo'),
      h('div', { class: 'chips' }, [['all', 'Todos'], ['desktop', 'Desktop'], ['mobile', 'Mobile']].map(([v, label]) => h('label', { class: 'chip' },
        h('input', { type: 'radio', name: 'device', value: v, checked: f.device === v, onchange: () => { f.device = v; draw(); } }), h('span', {}, label))))));
  }

  // ---- head: trail, title, Novo ▾ ----
  const trail = [['Launch', href('campaigns', { ...at, account: 'all' })]];
  trail.push([at.account === 'all' ? 'Todas as contas' : acctName.get(at.account) || at.account, href('groups', at)]);
  if (group) trail.push([group.name || group.id, href('campaigns', at, { group: group.id })]);
  if (campaign) trail.push([campaign.name, href('ads', at, { group: at.group, campaign: campaign.id })]);
  trail[trail.length - 1] = [trail[trail.length - 1][0]];
  main.append(crumbs(trail), h('div', { class: 'page-head' },
    h('div', {}, h('h1', {}, LEVELS[level]), h('p', { class: 'muted numbers-line' }, numbersLine(nums))),
    h('div', { class: 'actions' }, newMenu({ at, group, campaign, accounts }))));
  for (const p of problems) main.append(note('fail', p));

  // ---- chips: what the table is narrowed to ----
  // Groups shows every group, so only Campaigns and Ads are narrowed.
  if (level !== 'groups' && (group || campaign)) {
    const all = level === 'ads' ? 'Ver todos os anúncios' : 'Ver todas as campanhas';
    main.append(h('div', { class: 'scope' }, h('span', { class: 'fr-label' }, 'Mostrando só'),
      group ? scopeChip('Grupo', group.name || group.id, href(level, at, { campaign: at.campaign })) : null,
      campaign ? scopeChip('Campanha', campaign.name, href(level, at, { group: at.group })) : null,
      h('a', { class: 'scope-all', href: href(level, at) }, all)));
  }

  const totals = h('div', { class: 'totals' });
  const table = h('div', { class: 'table-wrap' });
  const result = h('div', { class: 'result' });
  const bar = h('div', { class: 'select-bar', hidden: true });
  main.append(result, totals, table, bar);

  const sort = { key: store('launch.sort.' + level) || 'spent', down: true };
  const heads = (cols) => cols.map(([key, label, cls]) => h('th', { class: (cls || '') + (key ? ' sortable' : ''), 'aria-sort': key && key === sort.key ? (sort.down ? 'descending' : 'ascending') : null },
    key ? h('button', { type: 'button', class: 'sort', onclick: () => { sort.down = sort.key === key ? !sort.down : true; sort.key = key; store('launch.sort.' + level, key); draw(); } }, label, key === sort.key ? (sort.down ? ' ↓' : ' ↑') : '') : label));
  const header = (cols) => h('thead', {}, h('tr', {}, heads(cols)));
  const byNumbers = (list, numbers, name) => list.sort((a, b) => {
    const k = sort.key;
    const d = k === 'name' ? name(a).localeCompare(name(b)) : sortValue(numbers(a), k) - sortValue(numbers(b), k);
    return sort.down ? -d : d;
  });
  const drawTotals = (sum, count) => totals.replaceChildren(h('div', { class: 'kpis' },
    h('div', { class: 'kpi' }, h('span', { class: 'fr-label' }, 'Linhas'), h('b', { class: 'num' }, int.format(count))),
    COLUMNS.filter(([k]) => ['spent', 'clicks', 'ctr', 'sales', 'cpa', 'revenue', 'profit', 'roi'].includes(k))
      .map(([, label, show]) => h('div', { class: 'kpi' }, h('span', { class: 'fr-label' }, label), h('b', { class: 'num' }, show(sum))))));

  const text = (s) => !f.text || s.toLowerCase().includes(f.text);
  let draw = () => {};

  // ---- Campaign Groups ----
  if (level === 'groups') {
    const inGroup = new Map();
    for (const c of campaigns) {
      const g = c.group_id || '-';
      if (!inGroup.has(g)) inGroup.set(g, []);
      inGroup.get(g).push(c);
    }
    const rows = [...groups];
    if (inGroup.has('-')) rows.push({ id: '-', name: 'Sem grupo', status: '', account: at.account === 'all' ? '' : at.account });
    const gNums = (g) => (inGroup.get(g.id) || []).reduce((s, c) => add(s, nums.campaigns?.[c.id]), zero());
    draw = () => {
      const list = byNumbers(rows.filter((g) => inState(g.status, f.state) && (text(g.name || '') || text(g.id))), gNums, (g) => g.name || '');
      const sum = list.reduce((s, g) => add(s, gNums(g)), zero());
      drawTotals(sum, list.length);
      table.replaceChildren(h('table', { class: 'list numbers' },
        header([['name', 'Grupo'], ['', 'Estado'], ['', 'Campanhas', 'num'], ['', 'Orçamento'], ...COLUMNS.map(([k, l]) => [k, l, 'num'])]),
        h('tbody', {}, list.length ? list.map((g) => {
          const cs = inGroup.get(g.id) || [];
          const running = cs.filter((c) => RUNNING.has(c.status)).length;
          const n = gNums(g);
          return h('tr', {},
            h('td', {}, h('a', { href: href('campaigns', { ...at, account: g.account || at.account }, { group: g.id }) }, g.name || g.id),
              h('div', { class: 'faint mono' }, (at.account === 'all' && g.account ? acctName.get(g.account) + ' · ' : '') + (g.id === '-' ? '' : g.id))),
            h('td', {}, g.status ? badge(g.status) : '—'),
            h('td', { class: 'num' }, cs.length ? `${cs.length}${running ? ` (${running} rodando)` : ''}` : '0'),
            h('td', { class: 'muted' }, g.id === '-' ? '—' : budget(g)),
            COLUMNS.map(([, , show]) => h('td', { class: 'num' }, show(n))));
        }) : h('tr', {}, h('td', { colspan: 4 + COLUMNS.length, class: 'faint' }, rows.length ? 'Nenhum grupo com esses filtros.' : 'Nenhum grupo nesta conta. Use Novo › Grupo de campanha.')))));
    };
  }

  // ---- Campaigns ----
  if (level === 'campaigns') {
    const pick = new Set();
    const together = { on: store('launch.together') ?? true };
    // A pair's two campaigns are picked together unless the person says not to.
    const choose = (id, on) => {
      const p = pairOf.get(id);
      const ids = [id];
      if (together.on && p) for (const x of [p.desktop_id, p.mobile_id]) if (x && x !== id && campById.has(x)) ids.push(x);
      for (const x of ids) on ? pick.add(x) : pick.delete(x);
      draw();
    };
    drawMoves(main, totals, moves.filter((m) => !group || campById.get(m.to_campaign)?.group_id === group.id || campById.get(m.from_campaign)?.group_id === group.id), campById, groupById);
    const rows = campaigns.filter((c) => !group || (c.group_id || '-') === group.id);
    draw = () => {
      const list = byNumbers(rows.filter((c) => inState(c.status, f.state) && (f.device === 'all' || c.device === f.device || c.device === 'both') &&
        (text(c.name) || text(c.id))), (c) => numsOf(c.id), (c) => c.name);
      const sum = list.reduce((s, c) => add(s, nums.campaigns?.[c.id]), zero());
      drawTotals(sum, list.length);
      const all = h('input', { type: 'checkbox', 'aria-label': 'Escolher todas', checked: list.length > 0 && list.every((c) => pick.has(c.id)),
        onchange: (e) => { for (const c of list) e.target.checked ? pick.add(c.id) : pick.delete(c.id); draw(); } });
      table.replaceChildren(h('table', { class: 'list numbers tree' },
        h('thead', {}, h('tr', {}, h('th', { class: 'pick' }, all), ...heads([['name', 'Campanha'], ['', 'Grupo'], ['', 'Dispositivo'], ['', 'Estado'], ['', 'Lance'], ['', 'Orçamento diário', 'num'], ...COLUMNS.map(([k, l]) => [k, l, 'num'])]))),
        h('tbody', {}, list.length ? list.map((c) => {
          const g = groupById.get(c.group_id);
          const p = pairOf.get(c.id);
          return h('tr', { class: pick.has(c.id) ? 'chosen' : '' },
            h('td', { class: 'pick' }, h('input', { type: 'checkbox', 'aria-label': 'Escolher ' + c.name, checked: pick.has(c.id), onchange: (e) => choose(c.id, e.target.checked) })),
            h('td', {}, h('a', { href: href('ads', { ...at, account: c.account }, { group: c.group_id || '-', campaign: c.id }) }, c.name),
              p ? h('span', { class: 'badge pair', title: 'Par criado pelo Launch: ' + p.name }, 'par') : null,
              h('div', { class: 'faint mono' }, (at.account === 'all' ? acctName.get(c.account) + ' · ' : '') + c.id, ' · ',
                h('a', { class: 'faint', href: link(NET, c.account, c.group_id || '-', c.id) }, 'detalhes'))),
            h('td', {}, group ? h('span', { class: 'muted' }, g?.name || '—') : h('a', { href: href('campaigns', { ...at, account: c.account }, { group: c.group_id || '-' }) }, g?.name || (c.group_id ? c.group_id : 'Sem grupo'))),
            h('td', {}, DEVICES[c.device] || '—'),
            h('td', {}, badge(c.status)),
            h('td', { class: 'muted' }, bidName(c.settings)),
            h('td', { class: 'num' }, money(c.settings.daily_cap)),
            COLUMNS.map(([, , show]) => h('td', { class: 'num' }, show(numsOf(c.id)))));
        }) : h('tr', {}, h('td', { colspan: 7 + COLUMNS.length, class: 'faint' }, rows.length ? 'Nenhuma campanha com esses filtros.' : group ? 'Nenhuma campanha neste grupo. Use Novo › Campanha.' : 'Nenhuma campanha.')))));
      drawCampaignBar();
    };
    const panel = h('div', { class: 'act-panel' });
    const drawCampaignBar = () => {
      bar.hidden = pick.size === 0;
      if (!pick.size) { panel.replaceChildren(); return; }
      const ids = [...pick];
      const accts = new Set(ids.map((id) => campById.get(id)?.account));
      const one = accts.size === 1 ? [...accts][0] : '';
      bar.replaceChildren(h('span', { class: 'count' }, plural(pick.size, 'campanha escolhida', 'campanhas escolhidas')),
        h('label', { class: 'together' }, h('input', { type: 'checkbox', checked: together.on, onchange: (e) => { together.on = e.target.checked; store('launch.together', together.on); } }), ' escolher o par junto'),
        one ? h('div', { class: 'actions' },
          h('a', { class: 'button', href: href('ads', { ...at, account: one }, { group: at.group, campaign: ids.length === 1 ? ids[0] : '' }) }, 'Ver anúncios'),
          h('a', { class: 'button', href: `/launch/new?make=ads&account=${encodeURIComponent(one)}&to=${ids.join(',')}` }, 'Adicionar anúncios'),
          h('button', { type: 'button', onclick: () => ask('pause', one, ids) }, 'Pausar'),
          h('button', { type: 'button', onclick: () => ask('change', one, ids) }, 'Mudar orçamento e lance'),
          h('button', { type: 'button', onclick: () => ask('duplicate', one, ids) }, 'Duplicar'),
          h('button', { type: 'button', onclick: () => ask('move', one, ids) }, 'Mudar de grupo'),
          h('button', { type: 'button', class: 'ghost', onclick: () => { pick.clear(); draw(); } }, 'Limpar'))
          : h('span', { class: 'muted' }, 'Escolha campanhas de uma conta só para agir nelas.'),
        panel);
    };
    // ask shows what will happen and waits for the person's click.
    const ask = (what, account, ids) => {
      const out = h('div');
      const names = ids.map((id) => campById.get(id)?.name || id).join(' · ');
      const sendIt = (button, path, body, say) => busy(button, out, async () => {
        const res = await api(`${NET}/${encodeURIComponent(account)}/${path}`, { method: 'POST', body });
        result.replaceChildren(doneNote(res.done, say, campById));
        setTimeout(() => location.reload(), 1500);
      });
      const n = plural(ids.length, 'campanha', 'campanhas');
      let body;
      if (what === 'pause') {
        const b = h('button', { type: 'button', class: 'primary', onclick: () => sendIt(b, 'pause', { campaigns: ids }, ['pausada', 'pausadas']) }, 'Pausar ' + n);
        body = [h('h3', {}, 'Pausar'), h('p', { class: 'muted' }, 'Para de gastar na hora. Para ligar de novo, use o próprio Taboola.'), h('p', { class: 'faint' }, names), h('div', { class: 'actions' }, b)];
      } else if (what === 'duplicate') {
        const b = h('button', { type: 'button', class: 'primary', onclick: () => sendIt(b, 'duplicate', { campaigns: ids }, ['duplicada, pausada', 'duplicadas, pausadas']) }, 'Duplicar ' + n);
        body = [h('h3', {}, 'Duplicar'), h('p', { class: 'muted' }, 'Cada cópia nasce pausada no mesmo grupo, com os mesmos anúncios.'), h('p', { class: 'faint' }, names), h('div', { class: 'actions' }, b)];
      } else if (what === 'move') {
        const to = select([['', 'Escolha o grupo…'], ...groups.filter((g) => g.account === account).map((g) => [g.id, g.name || g.id])], '', { 'aria-label': 'Grupo de destino' });
        let originals = 'when_started';
        const b = h('button', { type: 'button', class: 'primary', onclick: () => {
          if (!to.value) { out.replaceChildren(note('fail', 'Escolha o grupo de destino.')); return; }
          sendIt(b, 'move', { campaigns: ids, to_group: to.value, originals }, ['copiada para o grupo novo, pausada', 'copiadas para o grupo novo, pausadas']);
        } }, 'Mudar ' + n);
        body = [h('h3', {}, 'Mudar de grupo'),
          h('p', { class: 'muted' }, 'O Taboola não muda o grupo de uma campanha: o Launch cria uma cópia pausada, com os mesmos anúncios, no grupo novo (outro id).'),
          h('div', { class: 'fields' }, field('Para o grupo', to)), h('p', {}, 'E a original?'),
          segmented('originals', [['when_started', 'Pausar quando a cópia começar'], ['now', 'Pausar agora'], ['leave', 'Deixar como está']], originals, (v) => { originals = v; }),
          h('p', { class: 'faint' }, names), h('div', { class: 'actions' }, b)];
      } else {
        const lim = status.limits || {};
        const cap = input({ inputmode: 'decimal', placeholder: 'fica igual' });
        const cpc = input({ inputmode: 'decimal', placeholder: 'fica igual' });
        const b = h('button', { type: 'button', class: 'primary', onclick: () => {
          const change = { daily_cap: numberOf(cap.value), cpc: numberOf(cpc.value) };
          if (Object.values(change).some(Number.isNaN)) { out.replaceChildren(note('fail', 'Use só números, como 0,35.')); return; }
          if (!change.daily_cap && !change.cpc) { out.replaceChildren(note('fail', 'Diga o que mudar.')); return; }
          for (const k of Object.keys(change)) if (!change[k]) delete change[k];
          sendIt(b, 'change', { campaigns: ids, change }, ['mudada', 'mudadas']);
        } }, 'Mudar ' + n);
        body = [h('h3', {}, 'Mudar orçamento e lance'), h('div', { class: 'fields' },
          field('Orçamento diário (US$)', cap, lim.max_daily_cap ? 'até ' + money(lim.max_daily_cap) : null),
          field('CPC (US$)', cpc, 'só para CPC fixo ou Smart')), h('p', { class: 'faint' }, names), h('div', { class: 'actions' }, b)];
      }
      panel.replaceChildren(h('div', { class: 'panel' }, ...body, out));
    };
  }

  // ---- Ads ----
  if (level === 'ads') {
    let rows = campaigns.filter((c) => (!group || (c.group_id || '-') === group.id) && (!campaign || c.id === campaign.id));
    if (campaign && !rows.length) rows = [campaign];
    // Ads are read per campaign: the ones with the most spend first, 25 at most.
    rows.sort((a, b) => numsOf(b.id).spent - numsOf(a.id).spent || (RUNNING.has(b.status) ? 1 : 0) - (RUNNING.has(a.status) ? 1 : 0));
    const some = rows.slice(0, 25);
    const ads = [];
    const failed = [];
    const byAcct = new Map();
    for (const c of some) {
      if (!byAcct.has(c.account)) byAcct.set(c.account, []);
      byAcct.get(c.account).push(c.id);
    }
    const reading = note('', 'Lendo os anúncios…');
    table.append(reading);
    await Promise.all([...byAcct].map(async ([acct, ids]) => {
      try {
        const res = await api(`${NET}/${encodeURIComponent(acct)}/ads?campaigns=${ids.join(',')}`);
        for (const [cid, list] of Object.entries(res.ads)) for (const ad of list) ads.push({ ...ad, campaign: cid, account: acct });
        for (const [cid, why] of Object.entries(res.errors || {})) failed.push(`${campById.get(cid)?.name || cid}: ${why}`);
      } catch (e) {
        failed.push(`${acctName.get(acct)}: ${e.message}`);
      }
    }));
    reading.remove();
    if (rows.length > some.length) {
      main.insertBefore(note('', `Mostrando os anúncios das ${some.length} campanhas que mais gastaram de ${rows.length}. Escolha um grupo ou uma campanha para ver os outros.`), totals);
    }
    for (const p of failed) main.insertBefore(note('fail', p), totals);
    const adNums = (a) => add(zero(), nums.ads?.[a.id]);
    const pick = new Set();
    draw = () => {
      const list = byNumbers(ads.filter((a) => inState(a.status, f.state) && (text(a.title || '') || text(a.id))), adNums, (a) => a.title || '');
      const sum = list.reduce((s, a) => add(s, nums.ads?.[a.id]), zero());
      drawTotals(sum, list.length);
      table.replaceChildren(h('table', { class: 'list numbers tree' },
        h('thead', {}, h('tr', {}, h('th', { class: 'pick' }, ''), ...heads([['name', 'Anúncio'], ['', 'Campanha'], ['', 'Estado'], ['', 'Revisão'], ['', 'Intel'], ...COLUMNS.map(([k, l]) => [k, l, 'num'])]))),
        h('tbody', {}, list.length ? list.map((a) => {
          const c = campById.get(a.campaign) || { id: a.campaign, name: a.campaign };
          const key = a.campaign + '/' + a.id;
          return h('tr', { class: pick.has(key) ? 'chosen' : '' },
            h('td', { class: 'pick' }, h('input', { type: 'checkbox', 'aria-label': 'Escolher ' + (a.title || a.id), checked: pick.has(key), onchange: (e) => { e.target.checked ? pick.add(key) : pick.delete(key); draw(); } })),
            h('td', {}, h('div', { class: 'ad-cell' }, a.image_url ? h('img', { class: 'mini', src: a.image_url, alt: '', loading: 'lazy' }) : h('span', { class: 'mini' }),
              h('div', {}, h('div', { class: 'wrap' }, a.title || '—'), h('div', { class: 'faint mono' }, a.id, a.ai ? ' · IA' : '')))),
            h('td', {}, campaign ? h('span', { class: 'muted' }, c.name) : h('a', { href: href('ads', { ...at, account: a.account }, { group: c.group_id || '-', campaign: c.id }) }, c.name)),
            h('td', {}, badge(a.status)),
            h('td', {}, a.approval ? badge(a.approval) : '—'),
            h('td', {}, word(nums.ads?.[a.id])),
            COLUMNS.map(([, , show]) => h('td', { class: 'num' }, show(adNums(a)))));
        }) : h('tr', {}, h('td', { colspan: 6 + COLUMNS.length, class: 'faint' }, ads.length ? 'Nenhum anúncio com esses filtros.' : campaign ? 'Nenhum anúncio nesta campanha. Use Novo › Anúncios.' : 'Nenhum anúncio.')))));
      drawAdBar();
    };
    const drawAdBar = () => {
      bar.hidden = pick.size === 0;
      if (!pick.size) return;
      const out = h('div');
      const byCamp = new Map();
      for (const k of pick) {
        const [cid, aid] = k.split('/');
        if (!byCamp.has(cid)) byCamp.set(cid, []);
        byCamp.get(cid).push(aid);
      }
      const b = h('button', { type: 'button', class: 'primary', onclick: () => busy(b, out, async () => {
        const lines = [];
        for (const [cid, list] of byCamp) {
          const c = campById.get(cid);
          const res = await api(`${NET}/${encodeURIComponent(c?.account || at.account)}/campaigns/${cid}/pause-ads`, { method: 'POST', body: { ads: list } });
          lines.push(`${c?.name || cid}: ${res.summary || plural(list.length, 'anúncio pausado', 'anúncios pausados')}`);
        }
        result.replaceChildren(note('ok', lines.join(' · ')));
        setTimeout(() => location.reload(), 1500);
      }) }, 'Pausar ' + plural(pick.size, 'anúncio', 'anúncios'));
      bar.replaceChildren(h('span', { class: 'count' }, plural(pick.size, 'anúncio escolhido', 'anúncios escolhidos')),
        h('div', { class: 'actions' }, b, h('button', { type: 'button', class: 'ghost', onclick: () => { pick.clear(); draw(); } }, 'Limpar')), out);
    };
  }

  draw();
}

// drawMoves lists group changes waiting for their copy to start: the
// original is paused then, unless the person says not to.
function drawMoves(main, before, moves, campById, groupById) {
  if (!moves.length) return;
  main.insertBefore(h('div', { class: 'panel moves-panel' }, h('h3', {}, 'Mudanças de grupo esperando'),
    h('p', { class: 'muted' }, 'A cópia já está no grupo novo, pausada. Quando alguém ligar a cópia no Taboola, o Launch pausa a original.'),
    h('ul', { class: 'moves' }, moves.map((m) => {
      const from = campById.get(m.from_campaign);
      const to = campById.get(m.to_campaign);
      const out = h('span');
      const btn = h('button', { type: 'button', class: 'small ghost', onclick: () => busy(btn, out, async () => { await api(`moves/${m.id}/cancel`, { method: 'POST' }); location.reload(); }) }, 'Não pausar a original');
      return h('li', {}, (from?.name || m.from_campaign) + ' → ' + (groupById.get(m.to_group)?.name || m.to_group) + ' ',
        h('a', { href: link(NET, m.account, to?.group_id || m.to_group || '-', m.to_campaign) }, '(cópia ' + m.to_campaign + ')'), ' ', btn, out);
    }))), before);
}

function numbersLine(nums) {
  if (!nums.available) return 'Os números aparecem quando o Intel estiver ligado.';
  const at = nums.refreshed_at ? new Date(nums.refreshed_at) : null;
  return 'Números do Intel' + (at && !isNaN(at) ? ', atualizados ' + at.toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' }) : '') + '. Vendas e receita do RedTrack.';
}

function scopeChip(kind, name, without) {
  return h('span', { class: 'scope-chip' }, h('span', { class: 'faint' }, kind + ': '), h('b', {}, name),
    h('a', { href: without, class: 'x', 'aria-label': `Tirar o filtro ${kind}`, title: 'Ver todos' }, '×'));
}

const BID = { MAX_CONVERSIONS: 'Maximizar conversões', TARGET_CPA: 'CPA alvo', FIXED: 'CPC fixo', SMART: 'CPC Smart' };

function bidName(s) {
  const b = BID[s?.bid_strategy] || s?.bid_strategy || '—';
  if (s?.bid_strategy === 'FIXED' || s?.bid_strategy === 'SMART') return b + ' ' + money(s.cpc);
  if (s?.target_cpa) return 'CPA alvo ' + money(s.target_cpa);
  return b;
}

function budget(g) {
  const per = { DAILY: ' por dia', MONTHLY: ' por mês', ENTIRE: ' no total' }[g.budget_model];
  return per && g.budget ? money(g.budget) + per : 'por campanha';
}

const WORDS = { better: ['melhor', 'up'], worse: ['pior', 'down'], usual: ['normal', ''], unclear: ['incerto', 'faint'], too_little: ['pouco dado', 'faint'] };

function word(n) {
  if (!n?.word) return '—';
  const [label, cls] = WORDS[n.word] || [n.word, ''];
  return h('span', { class: cls, title: n.sureness ? 'certeza: ' + n.sureness : '' }, label);
}

// newMenu is the "Novo ▾" button: group, campaign or ads, starting from
// where the person is (the group or campaign the table is narrowed to).
export function newMenu({ at, group, campaign }) {
  const acct = at.account !== 'all' ? at.account : group?.account || campaign?.account || '';
  const base = (make, more = {}) => {
    const q = new URLSearchParams({ make });
    if (acct) q.set('account', acct);
    for (const [k, v] of Object.entries(more)) if (v) q.set(k, v);
    return '/launch/new?' + q;
  };
  const groupId = group && group.id !== '-' ? group.id : campaign?.group_id || '';
  const items = [
    ['Grupo de campanha', 'Só o grupo; depois, se quiser, uma campanha nele.', base('group')],
    ['Campanha', groupId ? 'Neste grupo, com os anúncios.' : 'Num grupo novo ou num que já existe, com os anúncios.', base('campaign', { group: groupId })],
    ['Anúncios', campaign ? 'Nesta campanha.' : 'Em campanhas que já existem.', base('ads', { to: campaign?.id || '' })],
  ];
  const menu = h('details', { class: 'new-menu' }, h('summary', { class: 'button primary' }, 'Novo ▾'),
    h('div', { class: 'menu', role: 'menu' }, items.map(([label, about, to]) => h('a', { href: to, role: 'menuitem' }, h('b', {}, label), h('small', {}, about)))));
  document.addEventListener('click', (e) => { if (!menu.contains(e.target)) menu.open = false; });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') menu.open = false; });
  return menu;
}
