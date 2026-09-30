// One campaign: its settings, its ads, its pair's other half and what
// Launch did to it. Its actions take the pair along when asked.
import { api, h, note, money, badge, link, crumbs, field, input, select, segmented, busy, numberOf, plural, DEVICES, stateName } from './lib.js';
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
  // Intel's and Desk's one-tap links open here with the change filled in
  // (?do=pause-ads&ads=11,12&from=intel:311). Nothing is sent until a
  // person confirms, and then only to this campaign, not its pair.
  const q = new URLSearchParams(location.search);
  const asked = q.get('do') || '';
  const from = /^(intel|desk):[\w-]{1,40}$/.test(q.get('from') || '') ? q.get('from') : '';
  let together = !!data.twin && !asked;
  const ids = () => (together && data.twin ? [c.id, data.twin.id] : [c.id]);
  const byId = new Map([[c.id, c], ...(data.twin ? [[data.twin.id, data.twin]] : [])]);

  async function post(button, path, body, say, by = '') {
    await busy(button, out, async () => {
      const res = await api(`${base}/${path}` + (by ? '?from=' + encodeURIComponent(by) : ''), { method: 'POST', body: { campaigns: ids(), ...body } });
      out.replaceChildren(say ? doneNote(res.done, say, byId) : adsNote(res.done), h('p', {}, h('a', { href: location.pathname }, 'Recarregar a campanha')));
      panel.replaceChildren();
      if (by) history.replaceState(null, '', location.pathname);
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
  if (asked) showAsked();

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

  function showAsked() {
    const who = from.startsWith('desk:') ? 'pelo Desk' : from.startsWith('intel:') ? 'pelo Intel' : 'por um link';
    const msg = h('div');
    const skip = h('button', { type: 'button', onclick: () => { history.replaceState(null, '', location.pathname); panel.replaceChildren(); } }, 'Ignorar');
    const box = (title, text, body, go) => panel.replaceChildren(h('div', { class: 'panel asked' },
      h('h3', {}, title), h('p', { class: 'muted' }, 'Sugerido ' + who + (from ? ' (' + from + ')' : '') + '. ', text, ' Nada muda até você confirmar.'),
      body, h('div', { class: 'actions' }, go, skip), msg));
    const lim = status.limits || {};
    if (asked === 'pause-ads') {
      const want = new Set((q.get('ads') || '').split(',').map((x) => x.trim()).filter(Boolean));
      const picks = data.ads.filter((a) => want.has(a.id)).map((a) => {
        const cb = h('input', { type: 'checkbox', checked: a.active !== false, value: a.id });
        return [cb, h('label', { class: 'check' }, cb, a.title, ' ', badge(a.status), ' ', h('span', { class: 'mono faint' }, a.id))];
      });
      const gone = [...want].filter((id) => !data.ads.some((a) => a.id === id));
      const go = h('button', { type: 'button', class: 'primary', onclick: () => {
        const ads = picks.filter(([cb]) => cb.checked).map(([cb]) => cb.value);
        if (!ads.length) { msg.replaceChildren(note('fail', 'Marque ao menos um anúncio.')); return; }
        post(go, `campaigns/${encodeURIComponent(c.id)}/pause-ads`, { campaigns: [c.id], ads }, null, from);
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
        const n = numberOf(box1.value);
        if (!n || Number.isNaN(n)) { msg.replaceChildren(note('fail', 'Use só números, como 0,35.')); return; }
        post(go, 'change', { campaigns: [c.id], change: { [k]: n } }, ['mudada', 'mudadas'], from);
      } }, 'Salvar');
      box(cap ? 'Mudar o teto diário' : 'Mudar o CPC', 'Hoje: ' + money(was) + '.',
        h('div', { class: 'fields' }, field(label, box1, max ? 'até ' + money(max) : null)), go);
    } else {
      panel.replaceChildren(note('warn', 'O link pede “' + asked + '”, que o Launch não conhece. Nada foi feito.'));
    }
  }
}

// adsNote says which ads were paused and which were not.
function adsNote(done) {
  const ok = done.filter((d) => !d.error);
  const bad = done.filter((d) => d.error);
  return h('div', {},
    ok.length ? note('ok', h('b', {}, plural(ok.length, 'anúncio pausado', 'anúncios pausados') + '. '), ok.map((d) => d.ad).join(' · ')) : null,
    ...bad.map((d) => note('fail', h('b', {}, d.ad + ': '), d.error)));
}
