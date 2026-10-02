// Campanhas: Launch's one table (Draw Designer batch 126). Accounts, their
// groups, the groups' campaigns and the campaigns' ads are rows of one
// table that open in place; a campaign opens on the right (campaign.js)
// without leaving the list, and ↑ ↓ move to the next one. The numbers are Intel's (intel_api),
// never asked of Taboola here; the lists of groups, campaigns and ads are
// Taboola's, kept by the server for a moment.
//
// The address holds what the page shows: /launch/campaigns?account=<id>
// &w=<today|yesterday|7d|30d>&group=<id>&open=<campaign id>, so a link or a
// reload shows the same thing. Every account is always in the table;
// account= and group= say which one opens (the others stay closed). A
// campaign's own address (/launch/taboola/<account>/g/<group>/c/<id>,
// Intel's and Desk's links) is this page with that campaign open.
import { api, h, note, money, badge, link, select, input, field, plural, store, busy, numberOf, segmented, doneNote, DEVICES } from './lib.js';
import { campaignView } from './campaign.js';
import { byAccount, inState, standIns } from './rows.js';

export const WINDOWS = [['today', 'Hoje'], ['yesterday', 'Ontem'], ['7d', 'Últimos 7 dias'], ['30d', 'Últimos 30 dias']];

const NET = 'taboola';

// where reads the address into what the page shows. campaign= is the old
// Ads table's narrowing, now the campaign that opens.
export function where(search) {
  const q = new URLSearchParams(search);
  const w = WINDOWS.some(([v]) => v === q.get('w')) ? q.get('w') : store('launch.window') || '7d';
  const account = q.get('account') === 'all' ? '' : q.get('account') || '';
  return { account, group: q.get('group') || '', open: q.get('open') || q.get('campaign') || '', w };
}

// href builds the page's address, keeping the account and the period.
export function href(at, more = {}) {
  const q = new URLSearchParams();
  const v = { account: at.account, w: at.w, ...more };
  for (const k of ['account', 'group', 'open', 'w']) if (v[k]) q.set(k, v[k]);
  return '/launch/campaigns?' + q;
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

// tracked is false while Intel has no sales from the tracker in the period
// shown: sales, CPA, revenue, profit and ROI then show "—" instead of a
// loss as big as the spend.
let tracked = true;
const sold = (show) => (n) => (tracked ? show(n) : '—');

// COLUMNS are the number columns, in Realize's order. The table shows the
// ones picked in Colunas (DEFAULT_COLUMNS until someone picks); the strip
// above it shows the totals of the main ones.
export const COLUMNS = [
  ['spent', 'Gasto', (n) => usd(n.spent)],
  ['impressions', 'Impressões', (n) => (n.has ? int.format(n.impressions) : '—')],
  ['clicks', 'Cliques', (n) => (n.has ? int.format(n.clicks) : '—')],
  ['ctr', 'CTR', (n) => (n.impressions ? pct.format(n.clicks / n.impressions) : '—')],
  ['cpc', 'CPC médio', (n) => (n.clicks ? money(n.spent / n.clicks) : '—')],
  ['sales', 'Vendas', sold((n) => (n.has ? int.format(n.sales) : '—'))],
  ['cpa', 'CPA', sold((n) => (n.sales ? money(Math.round((n.spent / n.sales) * 100) / 100) : '—'))],
  ['revenue', 'Receita', sold((n) => usd(n.revenue))],
  ['profit', 'Lucro', sold((n) => (n.has ? h('span', { class: n.profit < 0 ? 'down' : n.profit > 0 ? 'up' : '' }, money(n.profit) || 'US$ 0') : '—'))],
  ['roi', 'ROI', sold((n) => (n.spent && n.has ? pct.format(n.profit / n.spent) : '—'))],
];
export const DEFAULT_COLUMNS = ['spent', 'impressions', 'clicks', 'sales'];
const STRIP = ['spent', 'clicks', 'ctr', 'sales', 'cpa', 'revenue', 'profit', 'roi'];

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

// ---- the page ----

export async function manage(ctx) {
  const { main, aside, route, status } = ctx;
  const at = where(location.search);
  // A campaign's own address opens it here, asked actions (?do=) and all.
  const asked = route.page === 'campaign' ? location.search : '';
  if (route.page === 'campaign') {
    at.account = route.account;
    at.open = route.campaign;
  }
  store('launch.window', at.w);
  const taboola = (status.networks || []).find((n) => n.name === NET);
  if (!taboola?.connected) {
    main.append(h('h1', {}, 'Campanhas'), note('warn', h('b', {}, 'Taboola desligado. '), taboola?.reason || '',
      ' Em Novo › Campanha você ainda monta os anúncios e baixa a planilha para subir à mão.'));
    return;
  }

  // Every account, its groups and campaigns, and Intel's numbers.
  const accounts = (await api(`accounts/${NET}`)).accounts;
  if (at.account && !accounts.some((a) => a.id === at.account)) at.account = '';
  if (at.account) store('launch.acct', at.account);
  const acctName = new Map(accounts.map((a) => [a.id, a.name || a.id]));
  const loading = note('', 'Lendo o Taboola…');
  main.append(loading);
  const trees = await Promise.all(accounts.map((a) => api(`${NET}/${encodeURIComponent(a.id)}/tree`).then((t) => ({ a, t }), (e) => ({ a, error: e.message }))));
  const nums = await api(`numbers?window=${at.w}`).catch(() => ({ available: false, campaigns: {}, ads: {} }));
  loading.remove();
  tracked = Object.values(nums.campaigns || {}).some((n) => n.sales || n.revenue);

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
  // A campaign whose group was deleted stays in Taboola's list (Realize
  // shows "Campaign Group Was Deleted"). It gets a stand-in group, so it can
  // be found and opened, and that state instead of its own. Campaigns with
  // no group at all get "Sem grupo".
  const extra = standIns(groups, campaigns);
  groups.push(...extra);
  // realGroups are the groups a campaign can go to or start in.
  const realGroups = groups.filter((g) => !g.gone && !g.none);
  for (const c of campaigns) {
    if (!extra.some((g) => g.gone && g.account === c.account && g.id === c.group_id)) continue;
    c.taboola_status = c.status;
    c.status = 'GROUP_DELETED';
  }
  const campById = new Map(campaigns.map((c) => [c.id, c]));
  const groupOf = (c) => groups.find((g) => g.account === c.account && (g.id || '') === (c.group_id || ''));
  const numsOf = (id) => add(zero(), nums.campaigns?.[id]);
  const adNums = (a) => add(zero(), nums.ads?.[a.id]);

  // ---- left column: search, state, device, period, groups, campaigns ----
  const f = { state: store('launch.state') || 'all', device: store('launch.device') || 'all', text: '', groups: new Set(), camps: new Set() };
  const groupList = h('div', { class: 'check-list' });
  const campList = h('div', { class: 'check-list' });
  const listsFull = { groups: false, camps: false };
  const chips = (name, list, now, set) => h('div', { class: 'chips' }, list.map(([v, label]) => h('label', { class: 'chip' },
    h('input', { type: 'radio', name, value: v, checked: now === v, onchange: () => set(v) }), h('span', {}, label))));
  aside.append(
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Busca'),
      input({ type: 'search', placeholder: 'nome ou id', 'aria-label': 'Buscar na tabela', oninput: (e) => { f.text = e.target.value.trim().toLowerCase(); draw(); } })),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Estado'),
      chips('state', STATES, f.state, (v) => { f.state = v; store('launch.state', v); draw(); })),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Dispositivo'),
      chips('device', [['all', 'Todos'], ['desktop', 'Desktop'], ['mobile', 'Mobile']], f.device, (v) => { f.device = v; store('launch.device', v); draw(); })),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Período'),
      select(WINDOWS, at.w, { 'aria-label': 'Período', onchange: (e) => location.assign(href({ ...at, w: e.target.value }, { group: at.group })) })),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Grupo de campanha'), groupList),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Campanha'), campList));

  // Grupo de campanha and Campanha: tick some to see only those. The
  // campaign list follows the ticked groups; each list shows a few and
  // "+N" for the rest, and a ticked one always shows.
  const gKey = (acct, id) => acct + '/' + (id || '-');
  const inCount = new Map();
  for (const c of campaigns) inCount.set(gKey(c.account, c.group_id), (inCount.get(gKey(c.account, c.group_id)) || 0) + 1);
  const check = (label, count, on, set, title) => h('label', { class: 'check-row', title: title || null },
    h('input', { type: 'checkbox', checked: on, onchange: (e) => set(e.target.checked) }),
    h('span', { class: 'check-name' }, label), count == null ? null : h('span', { class: 'faint num' }, String(count)));
  const few = (list, full, on, n) => (full ? list : list.filter((x, i) => i < n || on(x)));
  const more = (left, one, many, which) => (left > 0 ? h('button', { type: 'button', class: 'link-button more',
    onclick: () => { listsFull[which] = true; drawLists(); } }, '+' + plural(left, one, many)) : null);
  function drawLists() {
    const gs = few(groups, listsFull.groups, (g) => f.groups.has(gKey(g.account, g.id)), 6);
    groupList.replaceChildren(...[...gs.map((g) => {
      const k = gKey(g.account, g.id);
      return check(g.name || g.id, inCount.get(k) || 0, f.groups.has(k), (on) => {
        on ? f.groups.add(k) : f.groups.delete(k);
        if (f.groups.size) for (const id of f.camps) if (!f.groups.has(gKey(campById.get(id)?.account, campById.get(id)?.group_id))) f.camps.delete(id);
        drawLists();
        draw();
      }, accounts.length > 1 ? acctName.get(g.account) : '');
    }), more(groups.length - gs.length, 'grupo', 'grupos', 'groups')].filter(Boolean));
    const all = campaigns.filter((c) => !f.groups.size || f.groups.has(gKey(c.account, c.group_id)));
    const cs = few(all, listsFull.camps, (c) => f.camps.has(c.id), 4);
    campList.replaceChildren(...[...cs.map((c) => check(c.name || c.id, null, f.camps.has(c.id), (on) => {
      on ? f.camps.add(c.id) : f.camps.delete(c.id);
      draw();
    }, c.id)), more(all.length - cs.length, 'campanha', 'campanhas', 'camps')].filter(Boolean));
    if (!all.length) campList.replaceChildren(h('span', { class: 'faint' }, 'Nenhuma campanha.'));
    if (!groups.length) groupList.replaceChildren(h('span', { class: 'faint' }, 'Nenhum grupo.'));
  }
  drawLists();

  // ---- head ----
  main.append(h('div', { class: 'page-head camp-head' },
    h('div', {}, h('h1', {}, 'Campanhas'), h('p', { class: 'muted numbers-line' }, numbersLine(nums))),
    h('div', { class: 'actions' }, newMenu({ at, group: at.group ? realGroups.find((g) => g.id === at.group && (!at.account || g.account === at.account)) : null }))));
  for (const p of problems) main.append(note('fail', p));
  const result = h('div', { class: 'result' });
  const totals = h('div', { class: 'totals' });
  const card = h('section', { class: 'table-card', 'aria-label': 'Grupos, campanhas e anúncios' });
  const bar = h('div', { class: 'select-bar', hidden: true });
  main.append(result, totals, card, bar);
  drawMoves(main, totals, moves, campById, groups);

  // ---- what is open, picked, sorted and shown ----
  const sort = { key: store('launch.sort') || 'spent', down: store('launch.sort.down') ?? true };
  let cols = (store('launch.cols') || DEFAULT_COLUMNS).filter((k) => COLUMNS.some(([c]) => c === k));
  const openAccts = new Set();
  const openGroups = new Set();
  const openCamps = new Set();
  // adsOf holds each opened campaign's ads: a list, or {error}, or 'loading'.
  const adsOf = new Map();
  const pickC = new Set();
  const pickA = new Set(); // "<campaign>/<ad>"
  const together = { on: store('launch.together') ?? true };
  let shown = []; // the campaigns in the order the table shows them, for ↑ ↓
  // Every account and group opens, unless the address names a group (then
  // only it and its account do) or an account (then only it does).
  const named = at.group ? groups.filter((g) => g.id === at.group && (!at.account || g.account === at.account)) : [];
  for (const g of named.length ? named : groups) openGroups.add(g.account + '/' + (g.id || '-'));
  for (const a of accounts) {
    if (named.length ? named.some((g) => g.account === a.id) : !at.account || a.id === at.account) openAccts.add(a.id);
  }

  const byNumbers = (list, numbers, name) => list.sort((a, b) => {
    const d = sort.key === 'name' ? name(a).localeCompare(name(b), 'pt-BR') : sortValue(numbers(a), sort.key) - sortValue(numbers(b), sort.key);
    return sort.down ? -d : d;
  });

  async function loadAds(list) {
    const want = list.filter((c) => !adsOf.has(c.id));
    if (!want.length) return;
    for (const c of want) adsOf.set(c.id, 'loading');
    draw();
    const byAcct = new Map();
    for (const c of want) {
      if (!byAcct.has(c.account)) byAcct.set(c.account, []);
      byAcct.get(c.account).push(c.id);
    }
    await Promise.all([...byAcct].flatMap(([acct, ids]) => {
      const parts = [];
      for (let i = 0; i < ids.length; i += 25) parts.push(ids.slice(i, i + 25));
      return parts.map(async (part) => {
        try {
          const res = await api(`${NET}/${encodeURIComponent(acct)}/ads?campaigns=${part.join(',')}`);
          for (const id of part) adsOf.set(id, (res.ads[id] || []).map((a) => ({ ...a, campaign: id, account: acct })));
          for (const [id, why] of Object.entries(res.errors || {})) adsOf.set(id, { error: why });
        } catch (e) {
          for (const id of part) adsOf.set(id, { error: e.message });
        }
      });
    }));
    draw();
  }

  const toggle = (set, key) => { set.has(key) ? set.delete(key) : set.add(key); draw(); };
  const toggleCamp = (c) => {
    if (openCamps.has(c.id)) openCamps.delete(c.id);
    else { openCamps.add(c.id); loadAds([c]); }
    draw();
  };
  // Abrir tudo opens every account and group, and every campaign when there
  // are few enough to read their ads at once.
  const openAll = () => {
    const accts = byAccount(accounts, groups, campaigns, f);
    for (const x of accts) openAccts.add(x.key);
    const rows = accts.flatMap((x) => x.rows);
    for (const r of rows) openGroups.add(r.key);
    const cs = rows.flatMap((r) => r.cs);
    if (cs.length <= 25) {
      for (const c of cs) openCamps.add(c.id);
      loadAds(cs);
    }
    draw();
  };
  const closeAll = () => { openAccts.clear(); openGroups.clear(); openCamps.clear(); draw(); };

  // A pair's two campaigns are picked together unless the person says not to.
  const chooseCamp = (id, on) => {
    const p = pairOf.get(id);
    const ids = [id];
    if (together.on && p) for (const x of [p.desktop_id, p.mobile_id]) if (x && x !== id && campById.has(x)) ids.push(x);
    for (const x of ids) on ? pick(pickC, x) : pickC.delete(x);
    draw();
  };
  const pick = (set, x) => set.add(x);

  const sortHead = (key, label, cls) => h('th', { class: cls || null, 'aria-sort': key === sort.key ? (sort.down ? 'descending' : 'ascending') : null },
    h('button', { type: 'button', class: 'sort', onclick: () => {
      sort.down = sort.key === key ? !sort.down : key !== 'name';
      sort.key = key;
      store('launch.sort', key);
      store('launch.sort.down', sort.down);
      draw();
    } }, label, key === sort.key ? h('span', { class: 'arrow', 'aria-hidden': 'true' }, sort.down ? ' ↓' : ' ↑') : null));

  function drawTotals(sum, rows, openAny) {
    totals.replaceChildren(h('div', { class: 'kpis' },
      h('div', { class: 'kpi' }, h('span', { class: 'fr-label' }, openAny ? 'Linhas' : 'Grupos'), h('b', { class: 'num' }, int.format(openAny ? rows.cs : rows.g))),
      COLUMNS.filter(([k]) => STRIP.includes(k)).map(([k, label, show]) => h('div', { class: 'kpi' + (k === 'profit' && sum.profit > 0 && tracked ? ' good' : '') }, h('span', { class: 'fr-label' }, label), h('b', { class: 'num' }, show(sum))))));
  }

  function draw() {
    const accts = byAccount(accounts, groups, campaigns, f);
    const rows = accts.flatMap((x) => x.rows);
    const gNums = (r) => r.cs.reduce((s, c) => add(s, nums.campaigns?.[c.id]), zero());
    const aNums = (x) => x.rows.reduce((s, r) => { const n = gNums(r); return n.has ? add(s, n) : s; }, zero());
    byNumbers(accts, aNums, (x) => x.a.name || x.a.id);
    const visCols = COLUMNS.filter(([k]) => cols.includes(k));
    const nCols = 4 + visCols.length;
    const sum = rows.reduce((s, r) => add(s, gNums(r)), zero());
    const nCamps = rows.reduce((n, r) => n + r.cs.length, 0);
    const openAny = accts.some((x) => openAccts.has(x.key) && x.rows.some((r) => openGroups.has(r.key)));
    drawTotals(sum, { g: rows.length, cs: nCamps }, openAny);
    shown = [];
    const body = [];
    let adCount = 0;
    for (const x of accts) {
      const aOpen = openAccts.has(x.key);
      body.push(accountRow(x, aOpen, aNums(x), visCols));
      if (!aOpen) continue;
      byNumbers(x.rows, gNums, (r) => r.g.name || '');
      for (const r of x.rows) {
        const open = openGroups.has(r.key);
        body.push(groupRow(r, open, gNums(r), visCols));
        if (!open) continue;
        byNumbers(r.cs, (c) => numsOf(c.id), (c) => c.name);
        for (const c of r.cs) {
          shown.push(c);
          body.push(campRow(c, visCols));
          if (!openCamps.has(c.id)) continue;
          const ads = adsOf.get(c.id);
          if (ads === 'loading' || ads === undefined) body.push(h('tr', { class: 'row-ad' }, h('td'), h('td', { colspan: nCols - 1, class: 'faint indent-3' }, 'Lendo os anúncios…')));
          else if (ads.error) body.push(h('tr', { class: 'row-ad' }, h('td'), h('td', { colspan: nCols - 1, class: 'indent-3' }, note('fail', ads.error))));
          else if (!ads.length) body.push(h('tr', { class: 'row-ad' }, h('td'), h('td', { colspan: nCols - 1, class: 'faint indent-3' }, 'Nenhum anúncio. Use Novo › Anúncios.')));
          else {
            const list = byNumbers(ads.filter((a) => inState(a.status, f.state)), adNums, (a) => a.title || '');
            adCount += list.length;
            for (const a of list) body.push(adRow(c, a, visCols));
          }
        }
      }
    }
    const chip = [plural(accts.length, 'conta', 'contas'), plural(rows.length, 'grupo', 'grupos'), openAny ? plural(nCamps, 'campanha', 'campanhas') : null, adCount ? plural(adCount, 'anúncio', 'anúncios') : null].filter(Boolean).join(' · ');
    const allPicked = shown.length > 0 && shown.every((c) => pickC.has(c.id));
    const table = h('table', { class: 'list numbers nested camp-table' },
      h('thead', {}, h('tr', {},
        h('th', { class: 'pick' }, h('input', { type: 'checkbox', 'aria-label': 'Escolher todas as campanhas abertas', checked: allPicked,
          onchange: (e) => { for (const c of shown) e.target.checked ? pickC.add(c.id) : pickC.delete(c.id); draw(); } })),
        sortHead('name', 'Nome', 'name'), h('th', { class: 'state' }, 'Estado'), h('th', { class: 'budget' }, 'Orçamento'),
        visCols.map(([k, label]) => sortHead(k, label, 'num col-' + k)))),
      h('tbody', {}, body.length ? body : h('tr', {}, h('td', { colspan: nCols, class: 'faint empty-row' },
        accounts.length ? 'Nada com esses filtros.' : 'Nenhuma conta. Adicione uma em Contas.'))),
      accts.length ? h('tfoot', {}, h('tr', {}, h('td'), h('td', { colspan: 3 }, h('b', {}, 'Total')), visCols.map(([, , show]) => h('td', { class: 'num' }, show(sum))))) : null);
    const sep = () => h('span', { class: 'faint', 'aria-hidden': 'true' }, '·');
    card.replaceChildren(
      h('div', { class: 'card-head' },
        h('div', { class: 'card-tabs' }, h('span', { class: 'card-tab', 'aria-current': 'true' }, 'Tudo', h('span', { class: 'count-chip' }, chip))),
        h('div', { class: 'card-tools' },
          h('button', { type: 'button', class: 'link-button', onclick: openAll }, 'Abrir tudo'), sep(),
          h('button', { type: 'button', class: 'link-button', onclick: closeAll }, 'Fechar tudo'), sep(),
          columnsMenu())),
      h('div', { class: 'table-scroll' }, table));
    drawBar();
    if (current) card.querySelector(`tr[data-campaign="${CSS.escape(current)}"]`)?.classList.add('on');
  }

  function caret(open, label, onclick) {
    return h('button', { type: 'button', class: 'caret' + (open ? ' open' : ''), 'aria-expanded': String(open), 'aria-label': (open ? 'Fechar ' : 'Abrir ') + label, onclick },
      h('span', { class: 'tri', 'aria-hidden': 'true' }));
  }

  // pickBox ticks (or unticks) a box that stands for several campaigns.
  function pickBox(cs, label) {
    const picked = cs.filter((c) => pickC.has(c.id)).length;
    const box = h('input', { type: 'checkbox', 'aria-label': label, checked: cs.length > 0 && picked === cs.length, disabled: !cs.length,
      onchange: (e) => { for (const c of cs) e.target.checked ? pickC.add(c.id) : pickC.delete(c.id); draw(); } });
    box.indeterminate = picked > 0 && picked < cs.length;
    return box;
  }

  // accountRow is the table's top level: the account's name and id, how
  // many of its groups show, and their numbers summed.
  function accountRow(x, open, n, visCols) {
    const name = x.a.name || x.a.id;
    return h('tr', { class: 'row-account' },
      h('td', { class: 'pick' }, pickBox(x.rows.flatMap((r) => r.cs), 'Escolher as campanhas de ' + name)),
      h('td', { class: 'name' }, h('div', { class: 'name-cell' },
        caret(open, 'a conta ' + name, () => toggle(openAccts, x.key)),
        h('span', { class: 'tag tag-account' }, 'Conta'),
        h('span', { class: 'row-name' }, name),
        x.a.name && x.a.name !== x.a.id ? h('span', { class: 'mono faint' }, x.a.id) : null)),
      h('td', { class: 'groups-count' }, plural(x.rows.length, 'grupo', 'grupos')),
      h('td', { class: 'budget' }),
      visCols.map(([, , show]) => h('td', { class: 'num' }, show(n))));
  }

  function groupRow(r, open, n, visCols) {
    const { g, cs } = r;
    return h('tr', { class: 'row-group' },
      h('td', { class: 'pick' }, pickBox(cs, 'Escolher as campanhas de ' + (g.name || g.id))),
      h('td', { class: 'name' }, h('div', { class: 'name-cell indent-1' },
        caret(open, 'o grupo ' + (g.name || g.id), () => toggle(openGroups, r.key)),
        h('span', { class: 'tag' }, 'Grupo'),
        h('span', { class: 'row-name' }, g.name || g.id),
        g.id ? h('span', { class: 'mono faint' }, g.id) : null)),
      h('td', {}, g.status ? badge(g.status) : '—'),
      h('td', { class: 'mono budget' }, g.gone || g.none ? '—' : budget(g)),
      visCols.map(([, , show]) => h('td', { class: 'num' }, show(n))));
  }

  function campRow(c, visCols) {
    const p = pairOf.get(c.id);
    const open = openCamps.has(c.id);
    return h('tr', { class: 'row-camp' + (pickC.has(c.id) ? ' chosen' : ''), 'data-campaign': c.id },
      h('td', { class: 'pick' }, h('input', { type: 'checkbox', 'aria-label': 'Escolher ' + c.name, checked: pickC.has(c.id), onchange: (e) => chooseCamp(c.id, e.target.checked) })),
      h('td', { class: 'name' }, h('div', { class: 'name-cell indent-2' },
        caret(open, 'os anúncios de ' + c.name, () => toggleCamp(c)),
        h('span', { class: 'tag' }, 'Camp'),
        h('a', { class: 'row-name', href: link(NET, c.account, c.group_id || '-', c.id), title: 'Abrir a campanha ao lado',
          onclick: (e) => { if (e.metaKey || e.ctrlKey || e.shiftKey || e.button) return; e.preventDefault(); openCampaign(c.id); } }, c.name),
        p ? h('span', { class: 'badge pair', title: 'Par criado pelo Launch: ' + p.name }, 'par') : null,
        h('span', { class: 'mono faint' }, DEVICES[c.device] || ''))),
      h('td', {}, badge(c.status)),
      h('td', { class: 'mono budget' }, c.settings?.daily_cap ? money(c.settings.daily_cap) + ' / dia' : '—'),
      visCols.map(([, , show]) => h('td', { class: 'num' }, show(numsOf(c.id)))));
  }

  function adRow(c, a, visCols) {
    const key = c.id + '/' + a.id;
    return h('tr', { class: 'row-ad' + (pickA.has(key) ? ' chosen' : '') },
      h('td', { class: 'pick' }, h('input', { type: 'checkbox', 'aria-label': 'Escolher ' + (a.title || a.id), checked: pickA.has(key),
        onchange: (e) => { e.target.checked ? pickA.add(key) : pickA.delete(key); draw(); } })),
      h('td', { class: 'name' }, h('div', { class: 'name-cell indent-3' },
        h('span', { class: 'tag' }, 'Ad'),
        a.image_url ? h('img', { class: 'ad-thumb', src: a.image_url, alt: '', loading: 'lazy', referrerpolicy: 'no-referrer' }) : h('span', { class: 'ad-thumb empty', 'aria-hidden': 'true' }, '–'),
        h('span', { class: 'row-name', title: [a.title, a.description, a.url].filter(Boolean).join('\n') }, a.title || a.id))),
      h('td', {}, badge(a.approval && a.approval !== 'APPROVED' ? a.approval : a.status)),
      h('td', { class: 'mono budget' }, '—'),
      visCols.map(([, , show]) => h('td', { class: 'num' }, show(adNums(a)))));
  }

  function columnsMenu() {
    const menu = h('details', { class: 'cols-menu' }, h('summary', { class: 'link-button' }, 'Colunas', h('span', { class: 'tri', 'aria-hidden': 'true' })),
      h('div', { class: 'menu', role: 'group', 'aria-label': 'Colunas da tabela' },
        COLUMNS.map(([k, label]) => h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: cols.includes(k), onchange: (e) => {
          cols = COLUMNS.map(([c]) => c).filter((c) => (c === k ? e.target.checked : cols.includes(c)));
          store('launch.cols', cols);
          draw();
          card.querySelector('.cols-menu').open = true;
        } }), label)),
        h('button', { type: 'button', class: 'small ghost', onclick: () => { cols = DEFAULT_COLUMNS.slice(); store('launch.cols', cols); draw(); } }, 'Voltar ao padrão')));
    return menu;
  }
  document.addEventListener('click', (e) => { const m = card.querySelector('.cols-menu'); if (m && !m.contains(e.target)) m.open = false; });

  // ---- what to do with the chosen campaigns and ads ----
  const actPanel = h('div', { class: 'act-panel' });
  function drawBar() {
    bar.hidden = pickC.size === 0 && pickA.size === 0;
    if (bar.hidden) { actPanel.replaceChildren(); return; }
    const parts = [];
    if (pickC.size) {
      const ids = [...pickC];
      const accts = new Set(ids.map((id) => campById.get(id)?.account));
      const one = accts.size === 1 ? [...accts][0] : '';
      parts.push(h('span', { class: 'count' }, plural(pickC.size, 'campanha escolhida', 'campanhas escolhidas')),
        h('label', { class: 'together' }, h('input', { type: 'checkbox', checked: together.on, onchange: (e) => { together.on = e.target.checked; store('launch.together', together.on); } }), ' escolher o par junto'),
        one ? h('div', { class: 'actions' },
          h('a', { class: 'button', href: `/launch/new?make=ads&account=${encodeURIComponent(one)}&to=${ids.join(',')}` }, 'Adicionar anúncios'),
          h('button', { type: 'button', onclick: () => ask('pause', one, ids) }, 'Pausar'),
          h('button', { type: 'button', onclick: () => ask('change', one, ids) }, 'Mudar orçamento e lance'),
          h('button', { type: 'button', onclick: () => ask('duplicate', one, ids) }, 'Duplicar'),
          h('button', { type: 'button', onclick: () => ask('move', one, ids) }, 'Mudar de grupo'),
          h('button', { type: 'button', class: 'ghost', onclick: () => { pickC.clear(); draw(); } }, 'Limpar'))
          : h('span', { class: 'muted' }, 'Escolha campanhas de uma conta só para agir nelas.'));
    }
    if (pickA.size) {
      const out = h('div');
      const byCamp = new Map();
      for (const k of pickA) {
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
      }) }, 'Pausar ' + plural(pickA.size, 'anúncio', 'anúncios'));
      parts.push(h('span', { class: 'count' }, plural(pickA.size, 'anúncio escolhido', 'anúncios escolhidos')),
        h('div', { class: 'actions' }, b, h('button', { type: 'button', class: 'ghost', onclick: () => { pickA.clear(); draw(); } }, 'Limpar')), out);
    }
    bar.replaceChildren(...parts, actPanel);
  }

  // ask shows what will happen and waits for the person's click.
  function ask(what, account, ids) {
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
      const to = select([['', 'Escolha o grupo…'], ...realGroups.filter((g) => g.account === account).map((g) => [g.id, g.name || g.id])], '', { 'aria-label': 'Grupo de destino' });
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
    actPanel.replaceChildren(h('div', { class: 'panel' }, ...body, out));
  }

  // ---- the campaign on the right ----
  let current = null;
  const shade = h('div', { class: 'side-shade', hidden: true, onclick: () => closeCampaign() });
  const side = h('aside', { class: 'side', hidden: true, 'aria-label': 'Campanha', tabindex: '-1' });
  document.body.append(shade, side);
  let opened = 0;
  async function openCampaign(id, first = false) {
    const c = campById.get(id);
    if (!c && !first) return;
    current = id;
    const run = ++opened;
    for (const tr of card.querySelectorAll('tr.on')) tr.classList.remove('on');
    const row = card.querySelector(`tr[data-campaign="${CSS.escape(id)}"]`);
    row?.classList.add('on');
    row?.scrollIntoView({ block: 'nearest' });
    side.hidden = false;
    shade.hidden = false;
    document.body.classList.add('side-open');
    if (!first) history.replaceState(null, '', href(at, { group: at.group, open: id }));
    side.replaceChildren(note('', 'Lendo a campanha…'));
    const account = c?.account || at.account;
    const g = c ? groupOf(c) : null;
    try {
      const view = await campaignView({
        net: NET, account, id, status, asked: first ? asked : '',
        accountName: acctName.get(account) || account,
        group: g && !g.gone && !g.none ? g : null,
        groups: realGroups.filter((x) => x.account === account),
        numbers: c ? numsOf(id) : null, tracked,
        open: (other) => openCampaign(other),
        close: () => closeCampaign(),
        settle: () => history.replaceState(null, '', href(at, { group: at.group, open: id })),
      });
      if (run === opened) side.replaceChildren(view);
    } catch (e) {
      if (run === opened) side.replaceChildren(h('div', { class: 'side-head' }, h('span'), closeButton()), note('fail', e.message));
    }
    if (run === opened) side.focus({ preventScroll: true });
  }
  const closeButton = () => h('button', { type: 'button', class: 'side-close', 'aria-label': 'Fechar (Esc)', onclick: () => closeCampaign() }, '×');
  function closeCampaign() {
    if (!current) return;
    const row = card.querySelector(`tr[data-campaign="${CSS.escape(current)}"]`);
    current = null;
    opened++;
    side.hidden = true;
    shade.hidden = true;
    document.body.classList.remove('side-open');
    for (const tr of card.querySelectorAll('tr.on')) tr.classList.remove('on');
    history.replaceState(null, '', href(at, { group: at.group }));
    row?.querySelector('a.row-name')?.focus({ preventScroll: true });
  }
  document.addEventListener('keydown', (e) => {
    if (!current || e.metaKey || e.ctrlKey || e.altKey) return;
    const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName) || e.target.isContentEditable;
    if (e.key === 'Escape' && !typing) { e.preventDefault(); closeCampaign(); return; }
    if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && !typing) {
      const i = shown.findIndex((c) => c.id === current);
      const next = shown[i + (e.key === 'ArrowDown' ? 1 : -1)];
      if (next) { e.preventDefault(); openCampaign(next.id); }
    }
  });

  draw();
  if (at.group) card.querySelector('tr.row-group')?.scrollIntoView({ block: 'nearest' });
  if (at.open) {
    const c = campById.get(at.open);
    if (c) {
      openAccts.add(c.account);
      openGroups.add(c.account + '/' + (c.group_id || '-'));
      draw();
    }
    openCampaign(at.open, true);
  }
  newKeys({ at, group: at.group ? realGroups.find((g) => g.id === at.group && (!at.account || g.account === at.account)) : null });
}

// drawMoves lists group changes waiting for their copy to start: the
// original is paused then, unless the person says not to.
function drawMoves(main, before, moves, campById, groups) {
  if (!moves.length) return;
  main.insertBefore(h('div', { class: 'panel moves-panel' }, h('h3', {}, 'Mudanças de grupo esperando'),
    h('p', { class: 'muted' }, 'A cópia já está no grupo novo, pausada. Quando alguém ligar a cópia no Taboola, o Launch pausa a original.'),
    h('ul', { class: 'moves' }, moves.map((m) => {
      const from = campById.get(m.from_campaign);
      const to = campById.get(m.to_campaign);
      const out = h('span');
      const btn = h('button', { type: 'button', class: 'small ghost', onclick: () => busy(btn, out, async () => { await api(`moves/${m.id}/cancel`, { method: 'POST' }); location.reload(); }) }, 'Não pausar a original');
      return h('li', {}, (from?.name || m.from_campaign) + ' → ' + (groups.find((g) => g.id === m.to_group)?.name || m.to_group) + ' ',
        h('a', { href: link(NET, m.account, to?.group_id || m.to_group || '-', m.to_campaign) }, '(cópia ' + m.to_campaign + ')'), ' ', btn, out);
    }))), before);
}

function numbersLine(nums) {
  if (!nums.available) return 'Os números aparecem quando o Intel estiver ligado.';
  const at = nums.refreshed_at ? new Date(nums.refreshed_at) : null;
  const when = at && !isNaN(at) ? ', atualizados ' + at.toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' }) : '';
  if (!Object.keys(nums.campaigns || {}).length) return 'O Intel ainda não tem números destas campanhas neste período' + when + '.';
  return 'Números do Intel' + when + '. ' + (tracked ? 'Vendas e receita do RedTrack.' : 'Ainda sem vendas do RedTrack neste período: vendas, receita e lucro aparecem quando chegarem.');
}

// budget is a group's own budget, or "por campanha" when it has none.
export function budget(g) {
  const per = { DAILY: ' / dia', MONTHLY: ' / mês', ENTIRE: ' no total' }[g.budget_model];
  return per && g.budget ? money(g.budget) + per : 'por campanha';
}

// NEW is what the "+ Novo" menu makes: a group, a campaign (a pair) or ads,
// each with its letter (N then the letter opens it).
const NEW = [
  ['group', 'G', 'Grupo de campanha', 'Conta e nome; nasce sem campanhas'],
  ['campaign', 'C', 'Campanha', 'Mobile, desktop ou os dois, num grupo'],
  ['ads', 'A', 'Anúncios', 'Imagens e headlines da biblioteca'],
];

// newHref is where a "+ Novo" item goes, starting from where the person is
// (the account and group the address names).
function newHref(make, { at, group }) {
  const q = new URLSearchParams({ make });
  const acct = at.account || group?.account || '';
  if (acct) q.set('account', acct);
  if (make === 'campaign' && group?.id) q.set('group', group.id);
  return '/launch/new?' + q;
}

// newMenu is the "+ Novo" button and its menu.
export function newMenu(where) {
  const menu = h('details', { class: 'new-menu' }, h('summary', { class: 'button primary' }, '+ Novo'),
    h('div', { class: 'menu', role: 'menu' }, NEW.map(([make, key, label, about]) => h('a', { href: newHref(make, where), role: 'menuitem' },
      h('span', { class: 'new-glyph', 'aria-hidden': 'true' }, key),
      h('span', { class: 'new-text' }, h('b', {}, label), h('small', {}, about)),
      h('kbd', { class: 'new-keys', 'aria-label': 'atalho N ' + key }, 'N ' + key)))));
  document.addEventListener('click', (e) => { if (!menu.contains(e.target)) menu.open = false; });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') menu.open = false; });
  return menu;
}

// newKeys: N then G, C or A opens that "+ Novo" item. It listens before
// the Frame, whose G starts switching apps.
function newKeys(where) {
  let armed = 0;
  window.addEventListener('keydown', (e) => {
    if (e.metaKey || e.ctrlKey || e.altKey || /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName) || e.target.isContentEditable) return;
    const k = e.key.toLowerCase();
    if (armed && performance.now() - armed < 1500) {
      const item = NEW.find(([, key]) => key.toLowerCase() === k);
      armed = 0;
      if (item) {
        e.preventDefault();
        e.stopImmediatePropagation();
        location.assign(newHref(item[0], where));
      }
      return;
    }
    armed = k === 'n' ? performance.now() : 0;
  }, true);
}
