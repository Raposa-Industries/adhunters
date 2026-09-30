// Presets: settings people save and reuse, at two levels. A group preset
// holds a new group's budget and objective; a campaign preset holds a
// pair's settings (bid, caps, countries, link and tracking code, brand,
// description, AI label). Nobody fixes them for the team: each person
// saves their own, for one account or for all.
import { api, h, note, field, input, select, segmented, busy, numberOf, money, date, plural } from './lib.js';
import { TRACKERS, splitLink } from '/launch/_ads/tracking.js';
import { urlWarnings } from '/launch/_ads/checks.js';

export const OBJECTIVES = [
  ['DRIVE_WEBSITE_TRAFFIC', 'Tráfego para o site'],
  ['LEADS_GENERATION', 'Leads'],
  ['ONLINE_PURCHASES', 'Compras'],
  ['BRAND_AWARENESS', 'Marca'],
];

const MODELS = [['MONTHLY', 'Por mês'], ['ENTIRE', 'Total'], ['', 'Sem orçamento próprio']];

// groupFields is the part of a form that describes a new group.
export function groupFields(values = {}) {
  const name = input({ placeholder: 'Memory Loss US · outubro', value: values.name || '' });
  const budget = input({ inputmode: 'decimal', placeholder: '0 = sem orçamento', value: values.budget || '' });
  const model = select(MODELS, values.budget_model ?? 'MONTHLY');
  const objective = select(OBJECTIVES, values.objective || 'DRIVE_WEBSITE_TRAFFIC');
  const el = h('div', { class: 'fields' },
    field('Nome do grupo', name),
    field('Orçamento (US$)', budget, 'as campanhas do grupo gastam deste orçamento'),
    field('Orçamento é', model),
    field('Objetivo', objective, 'as campanhas do grupo têm o mesmo'));
  return {
    el,
    name,
    // preset is what a group preset keeps: everything but the name.
    preset: () => ({ budget: numberOf(budget.value) || 0, budget_model: model.value, objective: objective.value }),
    set: (f) => {
      if (f.budget !== undefined) budget.value = f.budget || '';
      if (f.budget_model !== undefined) model.value = f.budget_model;
      if (f.objective) objective.value = f.objective;
    },
    get: () => ({ name: name.value.trim(), budget: numberOf(budget.value) || 0, budget_model: model.value, objective: objective.value }),
    problem: () => {
      if (!name.value.trim()) return 'Dê um nome ao grupo.';
      if (Number.isNaN(numberOf(budget.value))) return 'O orçamento é um número, como 50 ou 12,50.';
      return '';
    },
  };
}

// settingsForm is a pair's settings: what both campaigns share.
export function settingsForm(values = {}, limits = {}) {
  const s = values.settings || {};
  const brand = input({ placeholder: 'Health Tips Daily', value: s.brand || '' });
  const cpc = input({ inputmode: 'decimal', placeholder: '0,35', value: s.cpc || '' });
  const bid = select([['FIXED', 'Fixo'], ['SMART', 'Smart (o Taboola ajusta)']], s.bid_strategy || 'FIXED');
  const cap = input({ inputmode: 'decimal', placeholder: '20', value: s.daily_cap || '' });
  const limit = input({ inputmode: 'decimal', placeholder: 'sem limite', value: s.spending_limit || '' });
  const countries = input({ placeholder: 'US', value: (s.countries || ['US']).join(', ') });
  const objective = select(OBJECTIVES, s.objective || 'DRIVE_WEBSITE_TRAFFIC');
  const start = input({ type: 'date', value: s.start_date || '' });
  const end = input({ type: 'date', value: s.end_date || '' });
  let trackerId = values.tracker || 'redtrack';
  const linkBox = input({ type: 'url', placeholder: 'https://…', value: values.link || values.url || '' });
  const tracking = h('textarea', { rows: 2, placeholder: 'sub1={campaign_id}&…', spellcheck: 'false' }, s.tracking_code || '');
  const description = input({ placeholder: 'opcional, em inglês', value: values.description || '' });
  const warnBox = h('div', { class: 'warnings' });
  const hint = h('span', { class: 'hint' }, TRACKERS[trackerId].hint);

  // A pasted tracker link is split: the address goes in every ad, the
  // {macro} parameters in the campaign's tracking code (Taboola escapes
  // macros in an ad's URL).
  function splitNow() {
    const { url, tracking: t } = splitLink(linkBox.value);
    if (t) {
      linkBox.value = url;
      tracking.value = t;
    } else if (!tracking.value && TRACKERS[trackerId].template && url) {
      tracking.value = TRACKERS[trackerId].template;
    }
    warn();
  }
  function warn() {
    const w = urlWarnings(linkBox.value.trim());
    warnBox.replaceChildren(...w.map((x) => h('p', { class: 'warn-line' }, x)));
  }
  linkBox.addEventListener('change', splitNow);
  linkBox.addEventListener('paste', () => setTimeout(splitNow));
  linkBox.addEventListener('input', warn);

  const trackers = segmented('tracker-' + Math.random().toString(36).slice(2, 7),
    Object.entries(TRACKERS).map(([k, t]) => [k, t.name]), trackerId, (v) => { trackerId = v; hint.textContent = TRACKERS[v].hint; splitNow(); });

  const el = h('div', { class: 'settings-form' },
    h('div', { class: 'fields' },
      field('CPC (US$)', cpc, limits.max_cpc ? 'até ' + money(limits.max_cpc) : null),
      field('Lance', bid),
      field('Teto diário (US$)', cap, limits.max_daily_cap ? 'até ' + money(limits.max_daily_cap) : 'por campanha'),
      field('Limite total (US$)', limit),
      field('Países', countries, 'códigos, separados por vírgula'),
      field('Objetivo', objective),
      field('Marca', brand, 'o nome que aparece no anúncio'),
      field('Descrição', description, 'vai em todos os anúncios')),
    h('div', { class: 'link-part' },
      h('span', { class: 'field' }, 'Tracker'), trackers,
      h('label', { class: 'field' }, 'Link', linkBox, hint),
      warnBox,
      field('Tracking code da campanha', tracking, 'o Taboola junta ao link de cada anúncio e preenche os {macros}')),
    h('div', { class: 'fields' }, field('Começa em', start, 'vazio: quando for ligada'), field('Termina em', end, 'vazio: sem fim')));
  warn();

  const settings = () => ({
    brand: brand.value.trim(),
    cpc: numberOf(cpc.value) || 0,
    bid_strategy: bid.value,
    daily_cap: numberOf(cap.value) || 0,
    spending_limit: numberOf(limit.value) || 0,
    countries: countries.value.split(/[\s,;]+/).map((c) => c.trim().toUpperCase()).filter(Boolean),
    tracking_code: tracking.value.trim(),
    objective: objective.value,
    start_date: start.value,
    end_date: end.value,
  });
  return {
    el,
    settings,
    url: () => linkBox.value.trim(),
    description: () => description.value.trim(),
    tracker: () => trackerId,
    // preset is what a campaign preset keeps: everything but the dates.
    preset: () => {
      const s = settings();
      delete s.start_date;
      delete s.end_date;
      return { settings: s, url: linkBox.value.trim(), tracker: trackerId, description: description.value.trim() };
    },
    set: (f) => {
      const s = f.settings || {};
      if (s.brand !== undefined) brand.value = s.brand;
      if (s.cpc !== undefined) cpc.value = s.cpc || '';
      if (s.bid_strategy) bid.value = s.bid_strategy;
      if (s.daily_cap !== undefined) cap.value = s.daily_cap || '';
      if (s.spending_limit !== undefined) limit.value = s.spending_limit || '';
      if (s.countries) countries.value = s.countries.join(', ');
      if (s.tracking_code !== undefined) tracking.value = s.tracking_code;
      if (s.objective) objective.value = s.objective;
      if (s.start_date !== undefined) start.value = s.start_date;
      if (s.end_date !== undefined) end.value = s.end_date;
      if (f.url !== undefined) linkBox.value = f.url;
      if (f.description !== undefined) description.value = f.description;
      if (f.tracker && TRACKERS[f.tracker]) {
        trackerId = f.tracker;
        const r = el.querySelector(`input[value="${f.tracker}"]`);
        if (r) r.checked = true;
        hint.textContent = TRACKERS[f.tracker].hint;
      }
      warn();
    },
    problem: () => {
      for (const [box, name] of [[cpc, 'O CPC'], [cap, 'O teto diário'], [limit, 'O limite total']]) {
        if (Number.isNaN(numberOf(box.value))) return name + ' é um número, como 0,35.';
      }
      if (!numberOf(cpc.value)) return 'Diga o CPC.';
      if (!linkBox.value.trim()) return 'Falta o link da página.';
      return '';
    },
  };
}

let cache = null;

export async function loadPresets(net, account) {
  const q = new URLSearchParams({ network: net || 'taboola' });
  if (account) q.set('account', account);
  cache = (await api('presets?' + q)).presets;
  return cache;
}

// presetBar lets a person fill a form from a preset, or save the form as
// one. form has preset() and set(fields).
export function presetBar({ level, net, account, form, list, onUse }) {
  const mine = () => (list || []).filter((p) => p.level === level);
  const out = h('div', { class: 'preset-out' });
  const pick = select([['', mine().length ? 'Usar um preset…' : 'Nenhum preset salvo ainda'], ...mine().map((p) => [p.id, p.name + (p.account ? '' : ' · todas as contas')])], '', {
    'aria-label': level === 'group' ? 'Preset de grupo' : 'Preset de campanha',
    disabled: !mine().length,
    onchange: () => {
      const p = mine().find((x) => String(x.id) === pick.value);
      if (!p) return;
      form.set(p.fields);
      onUse?.(p);
      out.replaceChildren(note('ok', `Preset “${p.name}” aplicado. Você ainda pode mudar qualquer campo.`));
    },
  });
  const save = h('button', { type: 'button', class: 'small ghost', onclick: () => {
    const name = input({ placeholder: 'nome do preset', 'aria-label': 'Nome do preset' });
    const onlyHere = h('input', { type: 'checkbox' });
    const ok = h('button', { type: 'button', class: 'small primary', onclick: () => busy(ok, msg, async () => {
      const body = { level, network: net || 'taboola', account: onlyHere.checked ? account : '', name: name.value.trim(), fields: form.preset() };
      const taken = (list || []).find((p) => p.level === level && p.name === body.name && p.account === body.account);
      if (taken && !confirm(`Já existe “${body.name}”. Trocar pelo que está no formulário?`)) return;
      const res = await api(taken ? 'presets/' + taken.id : 'presets', { method: taken ? 'PUT' : 'POST', body });
      const saved = { ...body, id: res.id };
      list?.push(saved);
      pick.append(h('option', { value: res.id }, body.name));
      pick.disabled = false;
      out.replaceChildren(note('ok', `Preset “${body.name}” salvo.`));
    }) }, 'Salvar');
    const msg = h('div');
    out.replaceChildren(h('div', { class: 'preset-save' }, name, account ? h('label', { class: 'check' }, onlyHere, 'só nesta conta') : null, ok), msg);
    name.focus();
  } }, 'Salvar como preset');
  return h('div', { class: 'preset-bar' }, pick, save, out);
}

// groupForm makes a new group, alone (from the tree).
export function groupForm({ net, account, onMade }) {
  const g = groupFields();
  const out = h('div');
  const box = h('div', { class: 'panel' }, h('h3', {}, 'Novo grupo'),
    h('p', { class: 'muted' }, 'Nasce pausado. As campanhas de um grupo dividem o orçamento dele.'));
  const go = h('button', { type: 'button', class: 'primary', onclick: () => {
    const p = g.problem();
    if (p) { out.replaceChildren(note('fail', p)); return; }
    busy(go, out, async () => onMade(await api(`${net}/${encodeURIComponent(account)}/groups`, { method: 'POST', body: g.get() })));
  } }, 'Criar grupo');
  loadPresets(net, account).then((list) => box.insertBefore(presetBar({ level: 'group', net, account, form: g, list }), g.el)).catch(() => {});
  box.append(g.el, h('div', { class: 'actions' }, go), out);
  return box;
}

// presets is the Presets page.
export async function presets({ main, status }) {
  main.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Presets'),
    h('p', { class: 'lead muted' }, 'Configurações que você salva e reaproveita. Salve um preset em Novo par ou aqui.'))));
  const list = await loadPresets('taboola', '');
  for (const [level, title, about] of [
    ['group', 'Grupos', 'Orçamento e objetivo de um grupo novo.'],
    ['campaign', 'Campanhas', 'Lance, tetos, países, link, tracking code, marca e descrição de um par.'],
  ]) {
    const mine = list.filter((p) => p.level === level);
    const editor = h('div');
    const panel = h('section', { class: 'panel' }, h('div', { class: 'page-head' }, h('div', {}, h('h2', {}, title), h('p', { class: 'muted' }, about)),
      h('div', { class: 'actions' }, h('button', { type: 'button', onclick: () => edit(level, null, editor, status) }, 'Novo preset'))),
    editor,
    mine.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
      h('thead', {}, h('tr', {}, h('th', {}, 'Nome'), h('th', {}, 'Guarda'), h('th', {}, 'Conta'), h('th', {}, 'De'), h('th', { class: 'num' }, 'Usado'), h('th', {}, ''))),
      h('tbody', {}, mine.map((p) => {
        const out = h('span');
        const del = h('button', { type: 'button', class: 'small ghost', onclick: () => {
          if (!confirm(`Apagar o preset “${p.name}”?`)) return;
          busy(del, out, async () => { await api('presets/' + p.id, { method: 'DELETE' }); location.reload(); });
        } }, 'Apagar');
        return h('tr', {},
          h('td', {}, h('a', { href: '#', onclick: (e) => { e.preventDefault(); edit(level, p, editor, status); } }, p.name)),
          h('td', { class: 'muted' }, summary(level, p.fields)),
          h('td', {}, p.account || 'todas'),
          h('td', { class: 'muted' }, (p.changed_by || p.made_by || '').split('@')[0], ' · ', date(p.changed_at || p.made_at)),
          h('td', { class: 'num' }, plural(p.used || 0, 'par', 'pares')),
          h('td', {}, del, out));
      })))) : h('p', { class: 'empty' }, 'Nenhum preset de ' + title.toLowerCase() + ' ainda.'));
    main.append(panel);
  }
}

export function summary(level, f = {}) {
  if (level === 'group') {
    return [f.budget ? money(f.budget) + (f.budget_model === 'MONTHLY' ? '/mês' : ' total') : 'sem orçamento', OBJECTIVES.find(([k]) => k === f.objective)?.[1]].filter(Boolean).join(' · ');
  }
  const s = f.settings || {};
  return [s.cpc ? 'CPC ' + money(s.cpc) : '', s.daily_cap ? 'teto ' + money(s.daily_cap) : '', (s.countries || []).join(' '), f.tracker ? TRACKERS[f.tracker]?.name : ''].filter(Boolean).join(' · ');
}

function edit(level, p, editor, status) {
  const form = level === 'group' ? groupFields() : settingsForm({}, status.limits);
  if (level === 'group') form.name.closest('label').remove();
  if (p) form.set(p.fields);
  const name = input({ value: p?.name || '', placeholder: 'nome do preset' });
  const account = input({ value: p?.account || '', placeholder: 'vazio: todas as contas' });
  const out = h('div');
  const save = h('button', { type: 'button', class: 'primary', onclick: () => busy(save, out, async () => {
    if (!name.value.trim()) throw new Error('Dê um nome ao preset.');
    await api(p ? 'presets/' + p.id : 'presets', { method: p ? 'PUT' : 'POST', body: { level, network: 'taboola', account: account.value.trim(), name: name.value.trim(), fields: form.preset() } });
    location.reload();
  }) }, p ? 'Salvar mudanças' : 'Salvar preset');
  editor.replaceChildren(h('div', { class: 'panel inner' },
    h('div', { class: 'fields' }, field('Nome do preset', name), field('Conta', account, 'id da conta, ou vazio para todas')),
    form.el, h('div', { class: 'actions' }, save, h('button', { type: 'button', class: 'ghost', onclick: () => editor.replaceChildren() }, 'Cancelar')), out));
}
