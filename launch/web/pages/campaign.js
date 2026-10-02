// One campaign, as it opens on the right of Campanhas (manage.js): where it
// is, its state and pair, what can be done to it, its numbers, and its
// settings, ads and what Launch did to it. Its actions take the pair along
// unless the person unticks "com o par".
import { api, h, note, money, badge, crumbs, field, input, select, segmented, busy, numberOf, plural, DEVICES, doneNote, budgetLine } from './lib.js';
import { historyTable } from './history.js';
import { OBJECTIVES } from './presets.js';

const int = new Intl.NumberFormat('pt-BR');
const pct = new Intl.NumberFormat('pt-BR', { style: 'percent', minimumFractionDigits: 2, maximumFractionDigits: 2 });

// campaignView builds the panel. o: net, account, id, status, accountName,
// group (or null), groups (the account's, for Mudar de grupo), numbers
// (Intel's, or null), tracked, asked (the address's ?do=… from Intel or
// Desk, or ''), open(id) for the pair's other half, close(), and settle()
// to clean the address once an asked action is done or set aside.
export async function campaignView(o) {
  const { net, account, status } = o;
  const base = `${net}/${encodeURIComponent(account)}`;
  const data = await api(`${base}/campaigns/${encodeURIComponent(o.id)}`);
  const c = data.campaign;
  const s = c.settings || {};
  const g = o.group;
  const el = h('div', { class: 'side-body' });

  // Intel's and Desk's one-tap links open here with the change filled in
  // (?do=pause-ads&ads=11,12&from=intel:311). Nothing is sent until a
  // person confirms, and then only to this campaign, not its pair.
  const q = new URLSearchParams(o.asked || '');
  const asked = q.get('do') || '';
  const from = /^(intel|desk):[\w:-]{1,60}$/.test(q.get('from') || '') ? q.get('from') : '';
  let together = !!data.twin && !asked;
  const ids = () => (together && data.twin ? [c.id, data.twin.id] : [c.id]);
  const byId = new Map([[c.id, c], ...(data.twin ? [[data.twin.id, data.twin]] : [])]);
  const out = h('div', { class: 'side-out' });
  const panel = h('div');

  async function post(button, path, body, say, by = '') {
    await busy(button, out, async () => {
      const res = await api(`${base}/${path}` + (by ? '?from=' + encodeURIComponent(by) : ''), { method: 'POST', body: { campaigns: ids(), ...body } });
      out.replaceChildren(say ? doneNote(res.done, say, byId) : adsNote(res.done),
        h('div', { class: 'actions' }, h('button', { type: 'button', class: 'small', onclick: () => location.reload() }, 'Atualizar a lista')));
      panel.replaceChildren();
      if (by) o.settle?.();
    });
  }

  // ---- head ----
  const twinDevice = data.twin ? DEVICES[data.twin.device] || data.twin.name : '';
  el.append(h('div', { class: 'side-head' },
    crumbs([[o.accountName], [g ? g.name || g.id : c.group_id ? 'Grupo ' + c.group_id : 'Sem grupo']]),
    h('button', { type: 'button', class: 'side-close', 'aria-label': 'Fechar (Esc)', title: 'Fechar (Esc)', onclick: () => o.close?.() }, '×')),
  h('h2', { class: 'side-title' }, c.name),
  h('p', { class: 'side-meta' }, badge(c.status), h('span', { class: 'muted' }, [DEVICES[c.device], c.id].filter(Boolean).join(' · ')),
    data.pair ? h('span', { class: 'badge pair', title: 'Par criado pelo Launch: ' + data.pair.name }, 'par') : null,
    data.twin ? h('span', { class: 'muted' }, 'com ', h('a', { href: '#', class: 'twin', onclick: (e) => { e.preventDefault(); o.open?.(data.twin.id); } }, twinDevice)) : null));

  // ---- actions ----
  const pauseBtn = h('button', { type: 'button', onclick: () => post(pauseBtn, 'pause', {}, ['pausada', 'pausadas']) });
  const dupBtn = h('button', { type: 'button', onclick: () => post(dupBtn, 'duplicate', {}, ['duplicada, pausada', 'duplicadas, pausadas']) }, 'Duplicar');
  const label = () => { pauseBtn.textContent = together && data.twin ? 'Pausar par' : 'Pausar'; };
  label();
  el.append(h('div', { class: 'actions side-actions' },
    pauseBtn,
    h('button', { type: 'button', onclick: () => showChange() }, 'Mudar'),
    h('a', { class: 'button', href: `/launch/new?make=ads&account=${encodeURIComponent(account)}&to=${c.id}` }, 'Adicionar anúncios'),
    dupBtn,
    h('button', { type: 'button', onclick: () => showMove() }, 'Mudar de grupo'),
    data.twin ? h('label', { class: 'check together', title: 'Desmarque para agir só nesta metade' },
      h('input', { type: 'checkbox', checked: together, onchange: (e) => { together = e.target.checked; label(); } }), 'com o par') : null),
  panel, out);

  // ---- numbers ----
  const n = o.numbers;
  const sold = (v) => (o.tracked ? v : '—');
  if (n) {
    el.append(h('div', { class: 'kpis side-kpis' },
      kpi('Gasto', n.spent ? money(n.spent) : '—'),
      kpi('Cliques', n.has ? int.format(n.clicks) : '—'),
      kpi('CTR', n.impressions ? pct.format(n.clicks / n.impressions) : '—'),
      kpi('Vendas', sold(n.has ? int.format(n.sales) : '—'))));
  }

  // ---- tabs: settings, ads, history ----
  const obj = OBJECTIVES.find(([k]) => k === (s.objective || c.objective))?.[1] || s.objective || c.objective || '—';
  const settings = h('dl', { class: 'summary' },
    h('dt', {}, 'Lance'), h('dd', {}, bidLine(s)),
    h('dt', {}, 'Teto diário'), h('dd', {}, money(s.daily_cap)),
    h('dt', {}, 'Limite total'), h('dd', {}, money(s.spending_limit)),
    h('dt', {}, 'Grupo'), h('dd', {}, g ? (g.name || g.id) + ' · ' + budgetLine(g) : c.group_id || '—'),
    h('dt', {}, 'Países'), h('dd', {}, (s.countries || []).join(', ') || 'todos', s.exclude_cities?.length ? ', menos ' + plural(s.exclude_cities.length, 'cidade', 'cidades') : ''),
    h('dt', {}, 'Objetivo'), h('dd', {}, obj),
    h('dt', {}, 'Marca'), h('dd', {}, s.brand || '—'),
    h('dt', {}, 'Tracking code'), h('dd', { class: 'mono wrap' }, s.tracking_code || '—'),
    h('dt', {}, 'Datas'), h('dd', {}, [s.start_date, s.end_date].filter(Boolean).join(' até ') || 'sem datas'));
  const ads = data.ads.length ? h('div', { class: 'side-ads' }, data.ads.map((a) => h('div', { class: 'side-ad' },
    a.image_url ? h('img', { class: 'mini', src: a.image_url, alt: '', loading: 'lazy', referrerpolicy: 'no-referrer' }) : h('span', { class: 'mini empty' }),
    h('div', { class: 'side-ad-text' }, h('b', {}, a.title, a.ai ? h('span', { class: 'badge' }, 'IA') : null),
      a.description ? h('span', { class: 'faint' }, a.description) : null,
      h('span', { class: 'meta' }, badge(a.status), a.approval ? badge(a.approval) : null, a.cta ? h('span', { class: 'faint' }, a.cta.toLowerCase().replace(/_/g, ' ')) : null,
        h('span', { class: 'mono faint' }, a.ad_id || a.id)))))) : h('p', { class: 'faint' }, 'Sem anúncios.');
  const past = data.history.length ? historyTable(data.history, false) : h('p', { class: 'faint' }, 'Nada feito pelo Launch nesta campanha ainda.');
  const TABS = [['settings', 'Configurações', settings], ['ads', 'Anúncios · ' + data.ads.length, ads], ['history', 'Histórico', past]];
  const body = h('div', { class: 'side-tab-body' });
  const tabBar = h('div', { class: 'side-tabs', role: 'tablist' });
  const showTab = (id) => {
    tabBar.replaceChildren(...TABS.map(([k, label]) => h('button', { type: 'button', role: 'tab', class: 'side-tab', 'aria-selected': String(k === id), onclick: () => showTab(k) }, label)));
    body.replaceChildren(TABS.find(([k]) => k === id)[2]);
  };
  showTab(asked === 'pause-ads' ? 'ads' : 'settings');
  el.append(tabBar, body, h('p', { class: 'faint side-foot' }, 'Abrir a campanha não sai da lista: Esc fecha, ↑ ↓ passa para a próxima.'));
  if (asked) showAsked();
  return el;

  function showMove() {
    const msg = h('div');
    const to = select([['', 'Escolha o grupo…'], ...o.groups.filter((x) => x.id !== c.group_id).map((x) => [x.id, x.name || x.id])], '', { 'aria-label': 'Grupo de destino' });
    let originals = 'when_started';
    const go = h('button', { type: 'button', class: 'primary', onclick: () => {
      if (!to.value) { msg.replaceChildren(note('fail', 'Escolha o grupo de destino.')); return; }
      post(go, 'move', { to_group: to.value, originals }, ['copiada para o grupo novo, pausada', 'copiadas para o grupo novo, pausadas']);
    } }, 'Mudar de grupo');
    panel.replaceChildren(h('div', { class: 'panel inner' }, h('h3', {}, 'Mudar de grupo'),
      h('p', { class: 'muted' }, 'O Launch cria uma cópia pausada, com os mesmos anúncios, no grupo novo. A cópia tem outro id.'),
      h('div', { class: 'fields' }, field('Para o grupo', to)),
      h('p', {}, 'E a original?'),
      segmented('originals', [['when_started', 'Pausar quando a cópia começar'], ['now', 'Pausar agora'], ['leave', 'Deixar como está']], originals, (v) => { originals = v; }),
      h('div', { class: 'actions' }, go, cancel()), msg));
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
        const v = numberOf(box.value);
        if (Number.isNaN(v)) { msg.replaceChildren(note('fail', 'Use só números, como 0,35.')); return; }
        if (v && v !== was) change[k] = v;
      }
      if (!Object.keys(change).length) { msg.replaceChildren(note('fail', 'Nada mudou.')); return; }
      if (change.name && together && data.twin) { msg.replaceChildren(note('fail', 'Para mudar o nome, desmarque “com o par”: cada metade tem o seu.')); return; }
      post(go, 'change', { change }, ['mudada', 'mudadas']);
    } }, 'Salvar');
    panel.replaceChildren(h('div', { class: 'panel inner' }, h('h3', {}, 'Mudar'),
      h('p', { class: 'muted' }, 'Mudar um anúncio faz o Taboola revisá-lo de novo; aqui só mudam o nome, o lance e os tetos da campanha.'),
      h('div', { class: 'fields' }, field('Nome', name), field('CPC (US$)', cpc, lim.max_cpc ? 'até ' + money(lim.max_cpc) : 'só com CPC fixo ou Smart'),
        field('Teto diário (US$)', cap, lim.max_daily_cap ? 'até ' + money(lim.max_daily_cap) : null), field('Limite total (US$)', limit)),
      h('div', { class: 'actions' }, go, cancel()), msg));
  }

  function cancel() {
    return h('button', { type: 'button', class: 'ghost', onclick: () => panel.replaceChildren() }, 'Cancelar');
  }

  function showAsked() {
    const who = from.startsWith('desk:') ? 'pelo Desk' : from.startsWith('intel:') ? 'pelo Intel' : 'por um link';
    const msg = h('div');
    const skip = h('button', { type: 'button', onclick: () => { o.settle?.(); panel.replaceChildren(); } }, 'Ignorar');
    const box = (title, text, inner, go) => panel.replaceChildren(h('div', { class: 'panel inner asked' },
      h('h3', {}, title), h('p', { class: 'muted' }, 'Sugerido ' + who + (from ? ' (' + from + ')' : '') + '. ', text, ' Nada muda até você confirmar.'),
      inner, h('div', { class: 'actions' }, go, skip), msg));
    const lim = status.limits || {};
    if (asked === 'pause-ads') {
      const want = new Set((q.get('ads') || '').split(',').map((x) => x.trim()).filter(Boolean));
      const picks = data.ads.filter((a) => want.has(a.id)).map((a) => {
        const cb = h('input', { type: 'checkbox', checked: a.active !== false, value: a.id });
        return [cb, h('label', { class: 'check' }, cb, a.title, ' ', badge(a.status), ' ', h('span', { class: 'mono faint' }, a.id))];
      });
      const gone = [...want].filter((id) => !data.ads.some((a) => a.id === id));
      const go = h('button', { type: 'button', class: 'primary', onclick: () => {
        const chosen = picks.filter(([cb]) => cb.checked).map(([cb]) => cb.value);
        if (!chosen.length) { msg.replaceChildren(note('fail', 'Marque ao menos um anúncio.')); return; }
        post(go, `campaigns/${encodeURIComponent(c.id)}/pause-ads`, { campaigns: [c.id], ads: chosen }, null, from);
      } }, 'Pausar os marcados');
      box('Pausar ' + plural(want.size, 'anúncio', 'anúncios'), 'Só nesta campanha.',
        h('div', {}, picks.map(([, l]) => l), gone.length ? note('warn', 'Não estão mais nesta campanha: ' + gone.join(', ')) : null,
          picks.length ? null : note('fail', 'Nenhum dos anúncios sugeridos está nesta campanha.')), go);
    } else if (asked === 'pause-campaign') {
      const go = h('button', { type: 'button', class: 'primary', onclick: () => { post(go, 'pause', { campaigns: [c.id] }, ['pausada', 'pausadas'], from); } }, 'Pausar a campanha');
      box('Pausar esta campanha', data.twin ? 'Só esta metade do par.' : '', null, go);
    } else if (asked === 'set-daily-cap' || asked === 'set-bid') {
      const cap = asked === 'set-daily-cap';
      const [k, was, label, max] = cap ? ['daily_cap', s.daily_cap, 'Teto diário (US$)', lim.max_daily_cap] : ['cpc', s.cpc, 'CPC (US$)', lim.max_cpc];
      const box1 = input({ inputmode: 'decimal', value: q.get(cap ? 'cap' : 'cpc') || '' });
      const go = h('button', { type: 'button', class: 'primary', onclick: () => {
        const v = numberOf(box1.value);
        if (!v || Number.isNaN(v)) { msg.replaceChildren(note('fail', 'Use só números, como 0,35.')); return; }
        post(go, 'change', { campaigns: [c.id], change: { [k]: v } }, ['mudada', 'mudadas'], from);
      } }, 'Salvar');
      box(cap ? 'Mudar o teto diário' : 'Mudar o CPC', 'Hoje: ' + money(was) + '.',
        h('div', { class: 'fields' }, field(label, box1, max ? 'até ' + money(max) : null)), go);
    } else {
      panel.replaceChildren(note('warn', 'O link pede “' + asked + '”, que o Launch não conhece. Nada foi feito.'));
    }
  }
}

function kpi(label, value) {
  return h('div', { class: 'kpi' }, h('span', { class: 'fr-label' }, label), h('b', { class: 'num' }, value));
}

const BIDS = { MAX_CONVERSIONS: 'Maximizar conversões', TARGET_CPA: 'CPA alvo', FIXED: 'CPC fixo', SMART: 'CPC Smart' };

// bidLine says how the campaign bids: "CPC fixo · US$ 0,32".
function bidLine(s) {
  const b = BIDS[s.bid_strategy] || s.bid_strategy || '—';
  if (s.bid_strategy === 'FIXED' || s.bid_strategy === 'SMART') return b + ' · ' + money(s.cpc);
  if (s.target_cpa) return b + ' · CPA alvo ' + money(s.target_cpa);
  return b;
}

// adsNote says which ads were paused and which were not.
function adsNote(done) {
  const ok = done.filter((d) => !d.error);
  const bad = done.filter((d) => d.error);
  return h('div', {},
    ok.length ? note('ok', h('b', {}, plural(ok.length, 'anúncio pausado', 'anúncios pausados') + '. '), ok.map((d) => d.ad).join(' · ')) : null,
    ...bad.map((d) => note('fail', h('b', {}, d.ad + ': '), d.error)));
}
