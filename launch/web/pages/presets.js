// Presets: settings people save and reuse, at two levels. A group preset
// holds a new group's budget and objective; a campaign preset holds a new
// campaign's settings (bid, budget, excluded cities, delivery, link and
// tracking code, brand, description). Nobody fixes them for the team: each person
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

const MODELS = [['', 'Por campanha'], ['MONTHLY', 'Por mês'], ['ENTIRE', 'Total']];

// The team's defaults for a new campaign (2026-09-30; the budget from the
// owner's rule of 2026-10-01: a campaign never spends more than $20 in all).
export const TEAM = {
  // Maximize conversions needs a conversion objective; the team's campaigns
  // are Online Purchases (like campaign 50549004, made in Realize).
  objective: 'ONLINE_PURCHASES',
  daily_cap: 20,
  spending_limit: 20,
  bid_strategy: 'MAX_CONVERSIONS',
  ad_delivery: 'OPTIMIZED',
  countries: ['US'],
  // Cities the team never shows in, by their id in Taboola's US city
  // dictionary (resources/countries/US/cities, read 2026-09-30).
  exclude_cities: ['3', '2132', '2038', '995', '140', '57', '125', '189', '847'],
  tracking_code: 'src={site}&utm_medium={thumbnail}&utm_content={title}&utm_term={cachebuster}&click_id={click_id}&cpc={cpc}&sub1={campaign_id}&utm_source=Taboola&utm_campaign={campaign_name}&sub4={campaign_item_id}&sub5={site}&sub6={title}&sub7={platform}&sub8={site_id}&sub9={thumbnail}&sub10={timestamp}&ref_id={click_id}',
};

// CITY names the dictionary ids the team uses.
export const CITY = { 3: 'Atlanta (Georgia)', 2132: 'Bend (Oregon)', 2038: 'Biloxi (Mississippi)', 995: 'Idaho Falls (Idaho)', 140: 'Indianapolis (Indiana)', 57: 'New York (New York)', 125: 'Rochester (New York)', 189: 'Shreveport (Louisiana)', 847: 'Tallahassee (Florida)' };
const cityLines = (ids) => (ids || []).map((id) => (CITY[id] ? `${id} ${CITY[id]}` : id)).join('\n');
const cityIds = (text) => text.split(/\r?\n/).map((l) => (l.trim().match(/^\d+/) || [''])[0]).filter(Boolean);

// CONVERSIONS are the objectives Taboola allows Maximize conversions on.
const CONVERSIONS = { LEADS_GENERATION: true, ONLINE_PURCHASES: true };
const BIDS = [['MAX_CONVERSIONS', 'Maximizar conversões (CPA)'], ['FIXED', 'CPC fixo'], ['SMART', 'CPC Smart']];
const DELIVERY = [['OPTIMIZED', 'Priorizar os melhores anúncios'], ['EVEN', 'Teste A/B (igual para todos)']];

// groupFields is the part of a form that describes a new group. An empty
// name gets the account's next number (01, 02…) when the group is made.
export function groupFields(values = {}) {
  const name = input({ placeholder: 'próximo número', value: values.name || '' });
  const budget = input({ inputmode: 'decimal', placeholder: '500', value: values.budget || '' });
  const model = select(MODELS, values.budget_model ?? '');
  const objective = select(OBJECTIVES, values.objective || TEAM.objective);
  const budgetBox = field('Orçamento do grupo (US$)', budget, 'as campanhas do grupo gastam deste orçamento');
  const showBudget = () => { budgetBox.hidden = !model.value; };
  model.addEventListener('change', showBudget);
  showBudget();
  const el = h('div', { class: 'fields' },
    field('Nome do grupo', name, 'vazio: o próximo número da conta'),
    field('Objetivo', objective, 'as campanhas do grupo têm o mesmo'),
    field('Orçamento', model, 'por campanha: cada campanha tem o seu (padrão)'),
    budgetBox);
  return {
    el,
    name,
    // preset is what a group preset keeps: everything but the name.
    preset: () => ({ budget: model.value ? numberOf(budget.value) || 0 : 0, budget_model: model.value, objective: objective.value }),
    set: (f) => {
      if (f.budget !== undefined) budget.value = f.budget || '';
      if (f.budget_model !== undefined) model.value = f.budget_model;
      if (f.objective) objective.value = f.objective;
      showBudget();
    },
    get: () => ({ name: name.value.trim(), budget: model.value ? numberOf(budget.value) || 0 : 0, budget_model: model.value, objective: objective.value }),
    problem: () => {
      if (model.value && !(numberOf(budget.value) > 0)) return 'Diga o orçamento do grupo, ou escolha "Por campanha".';
      return '';
    },
  };
}

// settingsForm is a new campaign's settings (both campaigns share them),
// starting from the team's defaults.
export function settingsForm(values = {}, limits = {}) {
  const s = { ...TEAM, ...(values.settings || {}) };
  const brand = input({ placeholder: 'Health Tips Daily', value: s.brand || '' });
  let bidNow = s.bid_strategy || 'MAX_CONVERSIONS';
  const cpc = input({ inputmode: 'decimal', placeholder: '0,35', value: s.cpc || '' });
  const cpa = input({ inputmode: 'decimal', placeholder: 'sem alvo', value: s.target_cpa || '' });
  const cpcBox = field('CPC (US$)', cpc, limits.max_cpc ? 'até ' + money(limits.max_cpc) : null);
  const cpaBox = field('CPA alvo (US$)', cpa, 'opcional: vazio deixa o Taboola maximizar');
  const showBid = () => { cpcBox.hidden = bidNow === 'MAX_CONVERSIONS'; cpaBox.hidden = bidNow !== 'MAX_CONVERSIONS'; };
  const bid = segmented('bid-' + Math.random().toString(36).slice(2, 7), BIDS, bidNow, (v) => { bidNow = v; showBid(); });
  const cap = input({ inputmode: 'decimal', placeholder: '20', value: s.daily_cap || '' });
  const limit = input({ inputmode: 'decimal', placeholder: limits.max_spend_limit ? String(limits.max_spend_limit) : 'nenhum', value: s.spending_limit || '' });
  let deliveryNow = s.ad_delivery || 'OPTIMIZED';
  const delivery = segmented('delivery-' + Math.random().toString(36).slice(2, 7), DELIVERY, deliveryNow, (v) => { deliveryNow = v; });
  const cities = h('textarea', { rows: 5, spellcheck: 'false', 'aria-label': 'Cidades fora' }, cityLines(s.exclude_cities));
  const objective = select(OBJECTIVES, s.objective || TEAM.objective);
  const start = input({ type: 'date', value: s.start_date || '' });
  const end = input({ type: 'date', value: s.end_date || '' });
  let trackerId = values.tracker || 'redtrack';
  const linkBox = input({ type: 'url', placeholder: 'https://…', value: values.link || values.url || '' });
  const tracking = h('textarea', { rows: 3, placeholder: 'sub1={campaign_id}&…', spellcheck: 'false' }, s.tracking_code || '');
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
    h('h3', {}, 'Configuração'),
    h('div', { class: 'fields' },
      field('Marca', brand, 'o nome que aparece no anúncio'),
      field('Objetivo', objective),
      field('Começa em', start, 'vazio: quando for ligada'), field('Termina em', end, 'vazio: sem fim')),
    h('h3', {}, 'Onde'),
    h('p', { class: 'muted' }, 'Estados Unidos, menos estas cidades (uma por linha, começando pelo número da cidade no Taboola):'),
    cities,
    h('h3', {}, 'Orçamento e lance'),
    h('div', { class: 'fields' },
      field('Orçamento diário (US$)', cap, limits.max_daily_cap ? 'até ' + money(limits.max_daily_cap) : 'por campanha'),
      field('Limite total da campanha (US$)', limit, limits.max_spend_limit ? `no máximo ${money(limits.max_spend_limit)}: a campanha nunca gasta mais que isso` : 'vazio: nenhum')),
    h('span', { class: 'field' }, 'Otimizar para'), bid,
    h('div', { class: 'fields' }, cpcBox, cpaBox),
    h('span', { class: 'field' }, 'Entrega dos anúncios'), delivery,
    h('h3', {}, 'Página e tracking'),
    h('div', { class: 'link-part' },
      h('span', { class: 'field' }, 'Tracker'), trackers,
      h('label', { class: 'field' }, 'Página (landing page)', linkBox, hint),
      warnBox,
      field('Tracking code da campanha', tracking, 'o Taboola junta ao link de cada anúncio e preenche os {macros}')),
    h('div', { class: 'fields' }, field('Descrição', description, 'vai em todos os anúncios')));
  showBid();
  warn();

  const settings = () => {
    const maxConv = bidNow === 'MAX_CONVERSIONS';
    return {
      brand: brand.value.trim(),
      cpc: maxConv ? 0 : numberOf(cpc.value) || 0,
      target_cpa: maxConv ? numberOf(cpa.value) || 0 : 0,
      bid_strategy: bidNow,
      daily_cap: numberOf(cap.value) || 0,
      spending_limit: numberOf(limit.value) || 0,
      countries: ['US'],
      exclude_cities: cityIds(cities.value),
      ad_delivery: deliveryNow,
      tracking_code: tracking.value.trim(),
      objective: objective.value,
      start_date: start.value,
      end_date: end.value,
    };
  };
  const radio = (name, v) => { const r = el.querySelector(`input[name^="${name}"][value="${v}"]`); if (r) r.checked = true; };
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
      if (s.target_cpa !== undefined) cpa.value = s.target_cpa || '';
      if (s.bid_strategy) { bidNow = s.bid_strategy; radio('bid-', bidNow); showBid(); }
      if (s.ad_delivery) { deliveryNow = s.ad_delivery; radio('delivery-', deliveryNow); }
      if (s.daily_cap !== undefined) cap.value = s.daily_cap || '';
      if (s.spending_limit !== undefined) limit.value = s.spending_limit || '';
      if (s.exclude_cities) cities.value = cityLines(s.exclude_cities);
      if (s.tracking_code !== undefined) tracking.value = s.tracking_code;
      if (s.objective) objective.value = s.objective;
      if (s.start_date !== undefined) start.value = s.start_date;
      if (s.end_date !== undefined) end.value = s.end_date;
      if (f.url !== undefined) linkBox.value = f.url;
      if (f.description !== undefined) description.value = f.description;
      if (f.tracker && TRACKERS[f.tracker]) {
        trackerId = f.tracker;
        radio('tracker-', f.tracker);
        hint.textContent = TRACKERS[f.tracker].hint;
      }
      warn();
    },
    problem: () => {
      for (const [box, name] of [[cpc, 'O CPC'], [cpa, 'O CPA alvo'], [cap, 'O orçamento diário'], [limit, 'O limite de gasto']]) {
        if (Number.isNaN(numberOf(box.value))) return name + ' é um número, como 0,35.';
      }
      if (bidNow !== 'MAX_CONVERSIONS' && !numberOf(cpc.value)) return 'Diga o CPC.';
      if (bidNow === 'MAX_CONVERSIONS' && !CONVERSIONS[objective.value]) return 'Maximizar conversões pede o objetivo Leads ou Compras.';
      if (!numberOf(cap.value)) return 'Diga o orçamento diário.';
      if (limits.max_daily_cap && numberOf(cap.value) > limits.max_daily_cap) return `O orçamento diário vai até ${money(limits.max_daily_cap)}.`;
      if (limits.max_spend_limit && numberOf(limit.value) > limits.max_spend_limit) return `O limite total vai até ${money(limits.max_spend_limit)}.`;
      const total = numberOf(limit.value) || limits.max_spend_limit || 0;
      if (total && numberOf(cap.value) > total) return `O orçamento diário (${money(numberOf(cap.value))}) passa do limite total (${money(total)}): o Taboola recusa.`;
      if (!brand.value.trim()) return 'Escreva a marca.';
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

// presets is the Presets page.
export async function presets({ main, status }) {
  main.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Presets'),
    h('p', { class: 'lead muted' }, 'Configurações que você salva e reaproveita. Salve um preset em Novo par ou aqui.'))));
  const list = await loadPresets('taboola', '');
  for (const [level, title, about] of [
    ['group', 'Grupos', 'Orçamento e objetivo de um grupo novo.'],
    ['campaign', 'Campanhas', 'Lance, orçamento, cidades fora, entrega, link, tracking code, marca e descrição de uma campanha nova.'],
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
    const per = { DAILY: '/dia', MONTHLY: '/mês', ENTIRE: ' total' }[f.budget_model];
    return [per && f.budget ? money(f.budget) + per : 'orçamento por campanha', OBJECTIVES.find(([k]) => k === f.objective)?.[1]].filter(Boolean).join(' · ');
  }
  const s = f.settings || {};
  const bid = s.bid_strategy === 'MAX_CONVERSIONS' ? 'Max. conversões' + (s.target_cpa ? ' CPA ' + money(s.target_cpa) : '') : s.cpc ? 'CPC ' + money(s.cpc) : '';
  return [bid, s.daily_cap ? money(s.daily_cap) + '/dia' : '', (s.exclude_cities || []).length ? plural(s.exclude_cities.length, 'cidade fora', 'cidades fora') : '', f.tracker ? TRACKERS[f.tracker]?.name : ''].filter(Boolean).join(' · ');
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
