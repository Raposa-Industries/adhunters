// Novo: the steps behind "+ Novo" (Draw Designer batch 126, IMPLEMENT
// 43e7b65f44; laid out again by IMPLEMENT d587e1b829). Each item has its own
// short page, made one at a time: the steps are listed on the left; each
// step is one card with Cancelar and "Próximo: …" under it; on the right,
// "Prévia na tabela" shows the rows Campanhas will show once it is made, the
// new ones marked "novo".
//
// The pages ask only what a person changes in Taboola, the rest keeps
// Taboola's defaults or the team's (presets.js, TEAM):
//
// make=group: Grupo (the account, one radio row per account; the name, the
// account's next number, and how names go down from it) and Revisar e criar.
// make=campaign (the default): Campanha (the group in an account > group
// tree; the devices; when it starts; the daily budget; countries and brand;
// the names, which follow the group; more settings with the tracking code)
// and Revisar e criar. "Os dois" makes one desktop and one mobile campaign.
// The campaigns go up without ads: "Adicionar anúncios" then opens make=ads
// with them picked.
// make=ads: Anúncios (the campaigns, the landing page, a matrix of pictures
// by headlines where each ticked cell is one ad in every chosen campaign,
// and the one button for them all; the library on the right) and Revisar e
// adicionar. Headlines always go in English. Taboola's rules only warn: the
// person decides. Without a connected network, the same ads come out as
// Taboola's bulk sheet.
import { api, h, note, field, input, select, segmented, busy, plural, money, link, store, badge, stateName, numberOf, DEVICES } from './lib.js';
import { groupFields, settingsForm, presetBar, loadPresets, OBJECTIVES } from './presets.js';
import { repeats } from './adset.js';
import { nest, standIns } from './rows.js';
import { clean, headlineWarnings, imageWarnings, looksAIMade, urlWarnings } from '/launch/_ads/checks.js';
import { CTAS, AD_COLUMNS, MAX_ADS, adId, adRows, uniqueNames, campaignIds } from '/launch/_ads/sheet.js';
import { fillTemplate } from '/launch/_ads/template.js';
import { zip } from '/launch/_ads/zip.js';
import { libraryPanel } from './library.js';
import { CHIPS, moreCTAs, letter, cell, matrixAds, toggle, forget, groupPrefix, campaignName, adName, namesLine, sheetName } from './matrix.js';

// Portuguese in a headline: accents Portuguese uses and English does not,
// and a few common words. Headlines always go out in English.
const PT = /[ãõçâêô]|\b(você|para|como|seu|sua|não|mais|de|que|com|uma?)\b/i;

export function portuguese(text) {
  return PT.test(text);
}

// MAKES name each kind of new item: the rail's title.
const MAKES = { campaign: 'Nova campanha', group: 'Novo grupo', ads: 'Novos anúncios' };

// COUNTRIES a campaign can show in, by Taboola's two-letter code. The team
// runs the United States (the default); the excluded cities are US cities.
const COUNTRIES = { US: 'Estados Unidos', CA: 'Canadá', GB: 'Reino Unido', AU: 'Austrália', NZ: 'Nova Zelândia', IE: 'Irlanda' };

const pad = (n) => String(n).padStart(2, '0');

let hid = 0;
// newId gives a headline the id the matrix knows it by.
const newId = () => 'h' + Date.now().toString(36) + '-' + ++hid;

// tomorrow is tomorrow's date here, YYYY-MM-DD.
function tomorrow() {
  const d = new Date();
  d.setDate(d.getDate() + 1);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

// icon draws a device card's small picture.
function icon(kind) {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 28 22');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('class', 'dev-icon');
  const shapes = {
    mobile: [['rect', { x: 3, y: 3, width: 9, height: 16, rx: 2 }], ['rect', { x: 14, y: 6, width: 11, height: 13, rx: 2 }]],
    desktop: [['rect', { x: 3, y: 3, width: 22, height: 13, rx: 2 }], ['path', { d: 'M10 20h8M14 16v4' }]],
    both: [['rect', { x: 2, y: 3, width: 17, height: 11, rx: 2 }], ['path', { d: 'M7 18h7M10.5 14v4' }], ['rect', { x: 18, y: 8, width: 8, height: 12, rx: 2 }]],
  }[kind];
  for (const [tag, attrs] of shapes) {
    const el = document.createElementNS(ns, tag);
    for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
    svg.append(el);
  }
  return svg;
}

const DEVICE_CARDS = [['mobile', 'Mobile', 'celular + tablet'], ['desktop', 'Desktop', 'computador'], ['both', 'Os dois', 'uma campanha de cada']];

// stylesheet loads these steps' own styles once.
function stylesheet() {
  if (document.querySelector('link[href="/launch/newpair.css"]')) return;
  document.head.append(h('link', { rel: 'stylesheet', href: '/launch/newpair.css' }));
}

export async function newPair({ main, status }) {
  stylesheet();
  const q = new URLSearchParams(location.search);
  const connected = (status.networks || []).filter((n) => n.connected);
  const limits = status.limits || {};
  // live: new groups, campaigns and ads go up running (TABOOLA_CREATE_ACTIVE).
  const live = !!limits.create_active;
  // A draft says what it makes, so it is read before the page is built.
  let draft = null;
  if (/^\d+$/.test(q.get('draft') || '')) {
    try {
      draft = await api('drafts/' + q.get('draft'));
    } catch (e) {
      main.append(note('fail', e.message));
    }
  }
  const asked = draft?.body?.make || q.get('make');
  // An address with campaigns (to=) or a library set (set=, from Create) is
  // ads: a new campaign has none.
  const make = ['group', 'campaign', 'ads'].includes(asked) ? asked : q.get('to') || q.get('set') ? 'ads' : 'campaign';
  const s = {
    net: q.get('net') || connected[0]?.name || 'taboola',
    account: q.get('account') || '',
    group: q.get('group') || '',
    newGroup: false,
    draftId: 0,
    devices: 'both',
    start: 'today', // or 'tomorrow'
    countries: ['US'],
    cols: [], // the matrix's pictures: {sha256, name, type, bytes, width, height, ai, library}
    rows: [], // its headlines: {id, text, desc, library, ai}
    ticked: new Set(), // cell(row id, sha256)
    cta: 'Learn More',
    ai: '',
  };
  let next = null; // the names for the chosen group, or the account's new group: actions.NextNames
  const nextNew = new Map(); // each account's new group names, for the tree
  let accounts = [];
  let loaded = false;
  let groups = []; // every account's groups, each with its account
  let campaignList = []; // every account's campaigns, each with its account
  let presetId = null;
  const acctName = (id) => accounts.find((a) => a.id === id)?.name || id;

  const back = '/launch/campaigns?' + new URLSearchParams(Object.entries({ account: s.account, group: s.group }).filter(([, v]) => v));

  // ---- the left rail ----
  const railSteps = h('ol', { class: 'rail-steps' });
  const rail = h('aside', { class: 'steps-rail', 'aria-label': 'Passos' },
    h('div', {}, h('div', { class: 'rail-label' }, MAKES[make]), railSteps));

  const gForm = groupFields();
  const set = settingsForm({}, limits);
  const presetHold = h('div', { class: 'preset-row' });
  const groupPresetHold = h('div', { class: 'preset-row' });
  const fieldHead = (label, right) => h('div', { class: 'field-head' }, h('span', { class: 'field-label' }, label), right ? h('span', { class: 'faint' }, right) : null);

  // ---- Grupo (make=group): the account and the name ----
  const acctList = h('div', { class: 'radio-list', role: 'radiogroup', 'aria-label': 'Conta' });
  const groupName = gForm.name;
  groupName.setAttribute('aria-label', 'Nome do grupo');
  let nameTyped = false;
  groupName.addEventListener('input', () => { nameTyped = !!groupName.value.trim(); drawCascade(); });
  const cascade = h('div', { class: 'cascade' });
  const groupCard = make === 'group' ? h('section', { class: 'form-card' },
    h('div', { class: 'field' }, fieldHead('Conta', 'uma linha por conta adicionada em Contas'), acctList),
    h('div', { class: 'two' },
      h('label', { class: 'field' }, 'Nome', groupName, h('span', { class: 'hint' }, 'o próximo número nesta conta')),
      h('div', { class: 'field' }, 'Os nomes descem assim', cascade)),
    h('details', { class: 'more' }, h('summary', {}, h('b', {}, 'Mais configurações'), h('span', { class: 'faint' }, ' objetivo e orçamento do grupo, para sempre')),
      groupPresetHold, h('div', { class: 'two' }, gForm.parts.objective, h('div', { class: 'stack' }, gForm.parts.model, gForm.parts.budget)))) : null;

  function drawAccounts() {
    if (make !== 'group') return;
    if (!connected.length) { acctList.replaceChildren(h('p', { class: 'faint pad' }, 'Sem conexão com o Taboola: criar precisa dele.')); return; }
    if (!accounts.length) { acctList.replaceChildren(h('p', { class: 'faint pad' }, loaded ? 'Nenhuma conta. Adicione em Contas.' : 'Carregando as contas…')); return; }
    acctList.replaceChildren(...accounts.map((a) => {
      const n = groups.filter((g) => g.account === a.id).length;
      return h('label', { class: 'radio-row' + (a.id === s.account ? ' on' : '') },
        h('input', { type: 'radio', name: 'acct', value: a.id, checked: a.id === s.account, onchange: () => { s.account = a.id; drawAccounts(); accountChanged(); } }),
        h('b', {}, a.name || a.id), h('span', { class: 'mono faint' }, a.id), h('span', { class: 'gap' }),
        h('span', { class: 'faint' }, n ? plural(n, 'grupo', 'grupos') : 'nenhum grupo'));
    }));
  }

  // drawCascade shows how the names go down from the group's.
  function drawCascade() {
    const name = groupName.value.trim() || next?.group || 'GRP01';
    const prefix = groupPrefix(name);
    const line = (tag, level, rest) => h('div', { class: 'cascade-row level-' + level }, h('span', { class: 'tag' }, tag),
      h('span', { class: 'mono' }, h('b', { class: 'accent' }, prefix), rest));
    cascade.replaceChildren(line('Grupo', 0, ''),
      line('Camp', 1, campaignName(prefix, 1, 'desktop').slice(prefix.length)),
      line('Ad', 2, adName(campaignName(prefix, 1, 'desktop'), 1).slice(prefix.length)));
  }

  // ---- Campanha (make=campaign) ----
  const tree = h('div', { class: 'radio-list tree', role: 'radiogroup', 'aria-label': 'Grupo' });
  const openAccts = new Set();
  function drawTree() {
    if (make !== 'campaign') return;
    if (!connected.length) { tree.replaceChildren(h('p', { class: 'faint pad' }, 'Sem conexão com o Taboola: criar precisa dele.')); return; }
    if (!accounts.length) { tree.replaceChildren(h('p', { class: 'faint pad' }, loaded ? 'Nenhuma conta. Adicione em Contas.' : 'Carregando os grupos…')); return; }
    tree.replaceChildren(...accounts.flatMap((a) => {
      const mine = groups.filter((g) => g.account === a.id);
      const open = openAccts.has(a.id);
      const head = h('button', { type: 'button', class: 'tree-acct', 'aria-expanded': String(open), onclick: () => { open ? openAccts.delete(a.id) : openAccts.add(a.id); drawTree(); } },
        h('span', { class: 'tree-caret' + (open ? ' open' : ''), 'aria-hidden': 'true' }, '›'), h('b', {}, a.name || a.id), h('span', { class: 'mono faint' }, a.id), h('span', { class: 'gap' }),
        h('span', { class: 'faint' }, mine.length ? plural(mine.length, 'grupo', 'grupos') : 'nenhum grupo'));
      if (!open) return [head];
      if (!nextNew.has(a.id)) {
        nextNew.set(a.id, null);
        api(`${s.net}/${encodeURIComponent(a.id)}/next`).then((n) => { nextNew.set(a.id, n); drawTree(); }, () => {});
      }
      const row = (value, label, sub, on) => h('label', { class: 'radio-row in' + (on ? ' on' : '') },
        h('input', { type: 'radio', name: 'grp', value, checked: on, onchange: () => pickGroup(value) }), h('b', {}, label), h('span', { class: 'gap' }), sub ? h('span', { class: 'faint' }, sub) : null);
      return [head, ...mine.map((g) => {
        const n = campaignList.filter((c) => c.account === a.id && c.group_id === g.id).length;
        return row(a.id + '|' + g.id, g.name || g.id, `${plural(n, 'campanha', 'campanhas')} · ${stateName(g.status)}`, !s.newGroup && s.account === a.id && s.group === g.id);
      }), row('new:' + a.id, '+ Grupo novo' + (nextNew.get(a.id)?.group ? ' ' + nextNew.get(a.id).group : ''), 'criado junto, sem campanhas antes', s.newGroup && s.account === a.id)];
    }));
  }
  // pickGroup chooses the group (account|id, or new:account) and reads the
  // names the campaigns get in it.
  async function pickGroup(v) {
    if (v.startsWith('new:')) {
      s.newGroup = true;
      s.account = v.slice(4);
      s.group = '';
    } else {
      s.newGroup = false;
      [s.account, s.group] = v.split('|');
    }
    openAccts.add(s.account);
    // The campaigns take their group's objective (Maximize conversions needs one of conversions).
    const g = groups.find((x) => x.account === s.account && x.id === s.group);
    const objective = s.newGroup ? gForm.get().objective : g?.objective;
    if (objective && OBJECTIVES.some(([v2]) => v2 === objective)) set.set({ settings: { objective } });
    next = null;
    drawTree();
    drawNames();
    update();
    const acct = s.account;
    const group = s.newGroup ? '' : s.group;
    const [n, p] = await Promise.all([
      api(`${s.net}/${encodeURIComponent(acct)}/next` + (group ? '?group=' + encodeURIComponent(group) : '')).catch(() => null), // worked out again when sent
      loadPresets(s.net, acct).catch(() => []),
    ]);
    if (acct !== s.account || group !== (s.newGroup ? '' : s.group)) return;
    next = n;
    presetHold.replaceChildren(presetBar({ level: 'campaign', net: s.net, account: acct, form: set, list: p, onUse: (x) => { presetId = x.id; brandAndCap(); update(); } }));
    drawNames();
    update();
  }

  const devicesNow = () => (s.devices === 'both' ? ['desktop', 'mobile'] : [s.devices]);
  const deviceBox = h('div', { class: 'device-cards', role: 'radiogroup', 'aria-label': 'Dispositivo' }, DEVICE_CARDS.map(([v, label, sub]) =>
    h('label', { class: 'device-card' }, h('input', { type: 'radio', name: 'devices', value: v, checked: s.devices === v, onchange: () => { s.devices = v; drawNames(); update(); } }),
      icon(v), h('span', { class: 'dev-text' }, h('b', {}, label), h('small', {}, sub)))));
  const startBox = segmented('start', [['today', 'Hoje'], ['tomorrow', 'Amanhã']], s.start, (v) => { s.start = v; update(); });
  const startDate = () => (s.start === 'tomorrow' ? tomorrow() : ''); // empty: Taboola starts it today

  // The brand and the daily budget are the settings form's own fields, laid
  // out here, so a preset fills them too.
  const brandIn = set.parts.brand.querySelector('input');
  brandIn.setAttribute('aria-label', 'Marca');
  brandIn.placeholder = 'Nerve Health Report';
  const capIn = set.parts.cap.querySelector('input');
  capIn.setAttribute('aria-label', 'Orçamento diário');
  const brandAndCap = () => { if (numberOf(capIn.value)) capIn.value = brl(numberOf(capIn.value)); };
  const capHint = [limits.max_daily_cap ? `até ${money(limits.max_daily_cap)} por dia` : '',
    limits.max_spend_limit ? `cada uma gasta até ${money(limits.max_spend_limit)} no total (o teto do servidor)` : 'sem limite de gasto'].filter(Boolean).join('; ');

  const countryIn = input({ placeholder: 'adicionar país', list: 'launch-countries', 'aria-label': 'Adicionar país' });
  const countryHint = h('span', { class: 'hint' }, '');
  const countryBox = h('div', { class: 'chip-input', onclick: (e) => { if (e.target === countryBox) countryIn.focus(); } });
  function drawCountries() {
    countryBox.replaceChildren(...s.countries.map((c) => h('span', { class: 'chip-x' }, COUNTRIES[c] || c,
      h('button', { type: 'button', 'aria-label': 'Tirar ' + (COUNTRIES[c] || c), onclick: () => { s.countries = s.countries.filter((x) => x !== c); drawCountries(); update(); } }, '×'))),
    countryIn, h('datalist', { id: 'launch-countries' }, Object.entries(COUNTRIES).filter(([c]) => !s.countries.includes(c)).map(([, name]) => h('option', { value: name }))));
  }
  function addCountry() {
    const t = countryIn.value.trim().toLowerCase();
    if (!t) return;
    const hit = Object.entries(COUNTRIES).find(([c, name]) => c.toLowerCase() === t || name.toLowerCase() === t);
    if (!hit) { countryHint.textContent = 'Escolha um da lista: ' + Object.values(COUNTRIES).join(', ') + '.'; return; }
    countryHint.textContent = '';
    if (!s.countries.includes(hit[0])) s.countries.push(hit[0]);
    countryIn.value = '';
    drawCountries();
    countryIn.focus();
    update();
  }
  countryIn.addEventListener('change', addCountry);
  countryIn.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); addCountry(); } });

  // Names: the team's, following the group; a click lets one be typed.
  const typed = { desktop: '', mobile: '' };
  let editing = '';
  const namesBox = h('div', { class: 'name-chips' });
  const teamName = (d) => (next ? next[d] : '');
  function drawNames() {
    if (make !== 'campaign') return;
    namesBox.replaceChildren(...devicesNow().map((d) => {
      const team = teamName(d);
      if (editing === d) {
        const inp = input({ value: typed[d] || team, class: 'mono', 'aria-label': 'Nome da campanha ' + DEVICES[d] });
        const done = () => { const v = inp.value.trim(); typed[d] = v === team ? '' : v; editing = ''; drawNames(); update(); };
        inp.addEventListener('blur', done);
        inp.addEventListener('keydown', (e) => {
          if (e.key === 'Enter') { e.preventDefault(); inp.blur(); }
          if (e.key === 'Escape') { inp.value = typed[d] || team; inp.blur(); }
        });
        setTimeout(() => inp.focus());
        return inp;
      }
      return h('button', { type: 'button', class: 'name-chip mono' + (typed[d] ? ' own' : ''), title: 'Mudar o nome da campanha ' + DEVICES[d], onclick: () => { editing = d; drawNames(); } },
        typed[d] || team || (s.account ? '…' : 'escolha o grupo'));
    }));
  }
  const campaignNames = () => devicesNow().map((d) => typed[d] || teamName(d));

  const trackIn = set.parts.tracking.querySelector('textarea');
  const moreSum = h('span', { class: 'mono faint more-sum' }, '');
  const more = h('details', { class: 'more box' }, h('summary', {}, h('b', {}, 'Mais configurações'), moreSum),
    presetHold, set.parts.tracking,
    h('div', { class: 'two' }, set.parts.bid, h('div', {}, set.parts.cpc, set.parts.cpa)),
    h('label', { class: 'field' }, 'Cidades fora (só nos Estados Unidos)', set.parts.cities, h('span', { class: 'hint' }, 'uma por linha, começando pelo número da cidade no Taboola')),
    set.parts.delivery);
  const campaignCard = make === 'campaign' ? h('section', { class: 'form-card' },
    h('div', { class: 'field' }, fieldHead('Grupo', 'conta › grupo'), tree),
    h('div', { class: 'field' }, 'Dispositivo', deviceBox),
    h('div', { class: 'two' },
      h('div', { class: 'field' }, 'Começa', startBox, h('span', { class: 'hint' }, 'roda o dia todo, sem data de fim')),
      h('label', { class: 'field' }, 'Orçamento diário', h('span', { class: 'affix' }, h('span', { class: 'faint' }, 'US$'), capIn, h('span', { class: 'faint' }, 'por campanha')),
        h('span', { class: 'hint' }, capHint))),
    h('div', { class: 'two' },
      h('div', { class: 'field' }, 'Países', countryBox, countryHint),
      h('label', { class: 'field' }, fieldHead('Marca', 'aparece em cada anúncio'), brandIn)),
    h('div', { class: 'field' }, fieldHead('Nomes', 'seguem o grupo; toque para mudar'), namesBox),
    more) : null;

  // campSettings is what both campaigns get: the form's, with the countries,
  // the start, no end and no total of their own (the server's ceiling still
  // applies).
  function campSettings() {
    const st = set.settings();
    return { ...st, countries: [...s.countries], exclude_cities: s.countries.includes('US') ? st.exclude_cities : [], start_date: startDate(), end_date: '', spending_limit: 0 };
  }

  // ---- Anúncios (make=ads): campaigns, page, the matrix and the button ----
  const to = (q.get('to') || '').split(',').map((x) => x.trim()).filter((x) => /^\d+$/.test(x));
  const adUrl = input({ type: 'url', placeholder: 'https://…', 'aria-label': 'Página de destino' });
  const urlWarn = h('div');
  adUrl.addEventListener('input', () => urlWarn.replaceChildren(...urlWarnings(adUrl.value.trim()).map((w) => h('div', { class: 'warn-line' }, w))));
  const url = () => adUrl.value.trim();
  const findCamp = input({ type: 'search', placeholder: 'nome ou id', 'aria-label': 'Buscar campanha' });
  const targetList = h('div', { class: 'table-wrap pick-tree' });
  const closed = new Set(); // groups closed in the tree, by account/id
  let treeDrawn = false;
  const chosenCount = h('span', { class: 'faint' }, '');
  const campChips = h('div', { class: 'chip-input' });
  const pickPop = h('div', { class: 'pick-pop', hidden: true }, findCamp, targetList,
    h('div', { class: 'actions' }, h('button', { type: 'button', class: 'small', onclick: () => { pickPop.hidden = true; } }, 'Pronto')));
  findCamp.addEventListener('input', () => drawTargets());
  const campOf = (id) => campaignList.find((c) => c.id === id);
  function drawChips() {
    chosenCount.textContent = to.length ? plural(to.length, 'escolhida', 'escolhidas') : '';
    campChips.replaceChildren(...to.map((id) => {
      const c = campOf(id);
      const name = c?.name || id;
      return h('span', { class: 'chip-x mono', title: name + (c ? ' · ' + acctName(c.account) : '') }, name.replace(/-pp-bl$/i, ''),
        h('button', { type: 'button', 'aria-label': 'Tirar ' + name, onclick: () => { to.splice(to.indexOf(id), 1); drawTargets(); changed(); } }, '×'));
    }), h('button', { type: 'button', class: 'add-link', 'aria-expanded': String(!pickPop.hidden), onclick: () => { pickPop.hidden = !pickPop.hidden; drawTargets(); drawChips(); if (!pickPop.hidden) findCamp.focus(); } }, '+ Escolher'));
  }
  // drawTargets is every account's groups with their campaigns; a group's
  // box picks all its campaigns. Big lists start closed, but the groups
  // with a picked campaign.
  function drawTargets() {
    if (make !== 'ads' || pickPop.hidden) return;
    const text = findCamp.value.trim().toLowerCase();
    const all = [...groups, ...standIns(groups, campaignList)];
    const rows = nest(all, campaignList, { state: 'all', device: 'all', text }).filter((r) => r.cs.length);
    if (!treeDrawn && campaignList.length) {
      treeDrawn = true;
      if (campaignList.length > 40) for (const r of rows) if (!r.cs.some((c) => to.includes(c.id))) closed.add(r.key);
    }
    const many = new Set(campaignList.map((c) => c.account)).size > 1;
    const choose = (ids, yes) => {
      for (const id of ids) {
        if (yes && !to.includes(id)) to.push(id);
        if (!yes && to.includes(id)) to.splice(to.indexOf(id), 1);
      }
      drawTargets();
      changed();
    };
    if (!rows.length) {
      targetList.replaceChildren(h('p', { class: 'faint pad' }, !connected.length ? 'Sem conexão: diga os ids das campanhas na planilha, no último passo.' : campaignList.length ? 'Nenhuma campanha com essa busca.' : 'Carregando as campanhas…'));
      return;
    }
    targetList.replaceChildren(h('table', { class: 'list nested' },
      h('thead', {}, h('tr', {}, h('th', { class: 'pick' }, ''), h('th', {}, 'Nome'), h('th', {}, 'Estado'), h('th', {}, 'Dispositivo'))),
      h('tbody', {}, rows.flatMap((r) => {
        const open = !!text || !closed.has(r.key);
        const ids = r.cs.map((c) => c.id);
        const picked = ids.filter((id) => to.includes(id)).length;
        const box = h('input', { type: 'checkbox', 'aria-label': 'Escolher as campanhas de ' + (r.g.name || r.g.id), checked: picked === ids.length, onchange: (e) => choose(ids, e.target.checked) });
        box.indeterminate = picked > 0 && picked < ids.length;
        return [h('tr', { class: 'row-group' },
          h('td', { class: 'pick' }, box),
          h('td', { class: 'name' }, h('div', { class: 'name-cell' },
            h('button', { type: 'button', class: 'caret' + (open ? ' open' : ''), 'aria-expanded': String(open), 'aria-label': (open ? 'Fechar ' : 'Abrir ') + (r.g.name || r.g.id),
              onclick: () => { closed.has(r.key) ? closed.delete(r.key) : closed.add(r.key); drawTargets(); } }, h('span', { 'aria-hidden': 'true' }, '▸')),
            h('span', { class: 'tag' }, 'Grupo'), h('span', { class: 'row-name' }, r.g.name || r.g.id),
            many ? h('span', { class: 'faint acct' }, acctName(r.g.account)) : null)),
          h('td', {}, r.g.status ? badge(r.g.status) : '—'),
          h('td', { class: 'faint' }, '—')),
        ...(open ? r.cs.map((c) => h('tr', { class: 'row-camp' + (to.includes(c.id) ? ' on' : '') },
          h('td', { class: 'pick' }, h('input', { type: 'checkbox', 'aria-label': 'Escolher ' + c.name, checked: to.includes(c.id), onchange: (e) => choose([c.id], e.target.checked) })),
          h('td', { class: 'name' }, h('div', { class: 'name-cell indent-1' }, h('span', { class: 'tag' }, 'Camp'), h('span', { class: 'row-name' }, c.name), h('span', { class: 'mono faint' }, c.id))),
          h('td', {}, badge(c.status)),
          h('td', {}, DEVICES[c.device] || '—'))) : [])];
      }))));
  }

  // The matrix: pictures are columns, headlines rows.
  const lib = make === 'ads' ? libraryPanel({
    letterOf: (sha) => { const i = s.cols.findIndex((c) => c.sha256 === sha); return i < 0 ? '' : letter(i); },
    hasHeadline: (text) => s.rows.some((r) => r.text.toLowerCase() === clean(text).trim().toLowerCase()),
    pickImage: async (c) => {
      const had = s.cols.find((x) => x.sha256 === c.sha256);
      if (had) { removeCol(had); return; }
      const r = await api('library/use?id=' + encodeURIComponent(c.id), { method: 'POST' });
      addCol({ ...r.image, ai: c.ai_label === 'ai', library: c.id });
    },
    pickHeadline: (x) => {
      const had = s.rows.find((r) => r.text.toLowerCase() === clean(x.text).trim().toLowerCase());
      if (had) removeRow(had);
      else addRow(x.text, { library: x.id, ai: x.ai_label === 'ai' });
    },
  }) : null;
  const matrixBox = h('div', { class: 'matrix-wrap' });
  const counter = h('span', { class: 'faint' }, '');
  const fileIn = h('input', { type: 'file', accept: 'image/jpeg,image/png,image/webp,image/gif', multiple: true, hidden: true, onchange: (e) => { addFiles(e.target.files); e.target.value = ''; } });
  const upNote = h('div');
  const hlType = input({ placeholder: 'ou escreva uma em inglês e tecle Enter', 'aria-label': 'Nova headline' });
  hlType.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter') return;
    e.preventDefault();
    for (const line of hlType.value.split(/\r?\n/)) addRow(line);
    hlType.value = '';
    changed();
    hlType.focus();
  });
  const ctaBox = h('div', { class: 'cta-chips', role: 'radiogroup', 'aria-label': 'Botão' });
  const namesLineEl = h('p', { class: 'faint names-line' }, '');

  function addCol(img) {
    if (s.cols.some((x) => x.sha256 === img.sha256)) return;
    s.cols.push(img);
    if (!s.ai && img.ai) s.ai = 'yes';
    changed();
  }
  function removeCol(c) {
    s.cols = s.cols.filter((x) => x !== c);
    forget(s.ticked, c.sha256);
    changed();
  }
  function addRow(text, extra = {}) {
    const t = clean(text || '').replace(/[\r\n]+/g, ' ').trim();
    if (!t || s.rows.some((r) => r.text.toLowerCase() === t.toLowerCase())) return null;
    const r = { id: newId(), text: t, desc: '', ...extra };
    s.rows.push(r);
    if (!s.ai && r.ai) s.ai = 'yes';
    changed();
    return r;
  }
  function removeRow(r) {
    s.rows = s.rows.filter((x) => x !== r);
    forget(s.ticked, r.id);
    changed();
  }

  async function addFiles(files) {
    const list = [...files];
    upNote.replaceChildren(note('', `Enviando ${plural(list.length, 'imagem', 'imagens')}…`));
    const problems = [];
    for (const f of list) {
      const bytes = new Uint8Array(await f.arrayBuffer());
      const form = new FormData();
      form.append('image', f, f.name);
      try {
        const info = await api('images', { method: 'POST', form });
        addCol({ ...info, ai: looksAIMade(bytes) });
      } catch (e) {
        problems.push(`${f.name}: ${e.message}`);
      }
    }
    upNote.replaceChildren(...problems.map((p) => note('fail', p)));
  }

  function drawMatrix() {
    if (make !== 'ads') return;
    const ads = matrixAds(s.rows, s.cols, s.ticked);
    const nOf = new Map(ads.map((a) => [a.row + ':' + a.col, a.n]));
    const focused = document.activeElement === hlType;
    const hlWarn = (t) => [...(portuguese(t) ? ['Parece português: as headlines vão sempre em inglês.'] : []), ...headlineWarnings(t)];
    matrixBox.replaceChildren(h('table', { class: 'matrix' },
      h('thead', {}, h('tr', {},
        h('th', { class: 'mx-corner' }, h('div', { class: 'mx-label' }, 'Headline e descrição ↓ · imagem →'), h('div', { class: 'faint' }, 'clique numa linha ou coluna para marcar todas')),
        ...s.cols.map((c, ci) => {
          const w = imageWarnings({ width: c.width, height: c.height, size: c.bytes, type: c.type });
          return h('th', { class: 'mx-col' },
            h('div', { class: 'mx-thumb' }, h('img', { src: '/launch/api/images/' + c.sha256, alt: '' }),
              h('button', { type: 'button', class: 'mx-x', 'aria-label': 'Tirar a imagem ' + letter(ci), onclick: () => removeCol(c) }, '×')),
            h('button', { type: 'button', class: 'mx-letter', title: 'Marcar ou desmarcar a coluna ' + letter(ci) + (w.length ? '. ' + w.join(' ') : ''), onclick: () => { toggle(s.ticked, s.rows.map((r) => cell(r.id, c.sha256))); changed(); } },
              letter(ci), w.length ? h('span', { class: 'mx-warn', 'aria-label': w.join(' ') }, ' !') : null));
        }),
        h('th', { class: 'mx-add' }, h('button', { type: 'button', class: 'mx-img', onclick: () => fileIn.click(), title: 'Uma imagem do computador' }, '+ img')))),
      h('tbody', {}, s.rows.map((r, ri) => {
        const w = hlWarn(r.text);
        return h('tr', {},
          h('th', { class: 'mx-row' },
            h('div', { class: 'mx-hl-line' },
              h('button', { type: 'button', class: 'mx-hl', title: 'Marcar ou desmarcar a linha toda', onclick: () => { toggle(s.ticked, s.cols.map((c) => cell(r.id, c.sha256))); changed(); } }, r.text),
              h('button', { type: 'button', class: 'mx-x row', 'aria-label': 'Tirar a headline', onclick: () => removeRow(r) }, '×')),
            h('input', { type: 'text', class: 'mx-desc', value: r.desc || '', placeholder: 'Descrição (opcional)', 'aria-label': 'Descrição de ' + r.text, oninput: (e) => { r.desc = e.target.value; } }),
            w.map((x) => h('div', { class: 'warn-line' }, x))),
          ...s.cols.map((c, ci) => {
            const k = cell(r.id, c.sha256);
            const on = s.ticked.has(k);
            return h('td', { class: 'mx-cell' + (on ? ' on' : '') }, h('label', {},
              h('input', { type: 'checkbox', checked: on, 'aria-label': `${r.text} com a imagem ${letter(ci)}`, onchange: () => { on ? s.ticked.delete(k) : s.ticked.add(k); changed(); } }),
              on ? h('span', { class: 'mx-ad' }, 'AD' + pad(nOf.get(ri + ':' + ci))) : null));
          }),
          h('td', { class: 'mx-add' }));
      })),
      h('tfoot', {}, h('tr', {}, h('td', { colspan: String(s.cols.length + 2) }, h('div', { class: 'mx-foot' },
        h('button', { type: 'button', class: 'add-link', onclick: () => lib.focusHeadlines() }, '+ headline da biblioteca'), hlType))))));
    if (!s.rows.length && !s.cols.length) matrixBox.prepend(h('p', { class: 'faint mx-empty' }, 'Clique nas imagens e headlines da biblioteca ao lado: cada imagem vira uma coluna e cada headline uma linha. Depois marque as combinações.'));
    if (focused) hlType.focus();
  }

  function drawCTAs() {
    const more = moreCTAs(CTAS);
    const inMore = more.includes(s.cta);
    ctaBox.replaceChildren(...CHIPS.map(([v, label]) => h('button', { type: 'button', class: 'cta-chip' + (s.cta === v ? ' on' : ''), role: 'radio', 'aria-checked': String(s.cta === v),
      onclick: () => { s.cta = v; drawCTAs(); update(); } }, label)),
    select([['', 'Mais'], ...more.map((c) => [c, c])], inMore ? s.cta : '', { class: 'cta-more' + (inMore ? ' on' : ''), 'aria-label': 'Outros botões do Taboola',
      onchange: (e) => { if (e.target.value) { s.cta = e.target.value; drawCTAs(); update(); } } }));
  }
  const ctaLabel = (c) => CHIPS.find(([v]) => v === c)?.[1] || c;

  const adsCard = make === 'ads' ? h('section', { class: 'form-card' },
    h('div', { class: 'two' },
      h('div', { class: 'field' }, fieldHead('Campanhas', chosenCount), campChips),
      h('label', { class: 'field' }, fieldHead('Página de destino', 'obrigatória'), adUrl, urlWarn)),
    pickPop,
    h('div', { class: 'field' }, fieldHead('Combinações', counter), matrixBox, fileIn, upNote),
    h('div', { class: 'field' }, fieldHead('Botão', 'o mesmo em todos estes anúncios'), ctaBox),
    namesLineEl) : null;

  // changed redraws what the matrix's parts show, then the rest.
  function changed() {
    drawChips();
    drawMatrix();
    lib?.draw();
    update();
  }

  // ---- Revisar ----
  const review = h('div');
  const sendOut = h('div');
  const sendBtn = h('button', { type: 'button', class: 'primary big', onclick: () => (make === 'group' ? sendGroup(false) : make === 'ads' ? sendAds() : sendPair()) },
    make === 'group' ? 'Criar grupo' : make === 'ads' ? (live ? 'Adicionar ativos' : 'Adicionar pausados') : live ? 'Criar e ligar' : 'Criar pausado');
  const sheetIds = input({ placeholder: '123456, 123457', 'aria-label': 'Ids das campanhas para a planilha' });
  const sheetOut = h('div');
  const sheetBtn = h('button', { type: 'button', onclick: () => downloadSheet() }, 'Baixar planilha e imagens');
  // Realize's "Create & add campaign": the group, then straight into a campaign in it.
  const andCampaign = h('button', { type: 'button', class: 'big', onclick: () => sendGroup(true) }, 'Criar e adicionar campanha');
  const aiBox = h('div');
  const reviewCard = h('section', { class: 'form-card' }, review,
    make === 'ads' ? h('div', { class: 'field' }, 'Feito com IA?', aiBox) : null,
    h('div', { class: 'actions' }, connected.length ? sendBtn : null, make === 'group' && connected.length ? andCampaign : null), sendOut,
    make !== 'ads' ? null : h('details', { class: 'sheet', open: !connected.length }, h('summary', {}, 'Subir à mão pelo Bulk Upload'),
      h('p', { class: 'muted' }, 'A planilha usa as campanhas que já existem no Taboola. Os anúncios entram pausados.'),
      h('div', { class: 'fields' }, field('Ids das campanhas', sheetIds, 'cada anúncio vai em todas')),
      h('div', { class: 'actions' }, sheetBtn), sheetOut));

  // ---- the steps ----
  const steps = {
    campaign: [
      { label: 'Campanha', short: 'revisar', el: campaignCard, sub: () => 'grupo, dispositivo e orçamento',
        lead: 'Sai uma campanha por dispositivo, com o nome do grupo na frente.' },
      { label: 'Revisar e criar', short: 'revisar', el: reviewCard, sub: () => '',
        lead: 'Confira na prévia ao lado. As campanhas ' + (live ? 'nascem rodando' : 'nascem pausadas') + ', ainda sem anúncios.' },
    ],
    group: [
      { label: 'Grupo', short: 'revisar', el: groupCard, sub: () => 'conta e nome',
        lead: 'O grupo nasce sem campanhas. Escolha a conta; o nome já vem pronto.' },
      { label: 'Revisar e criar', short: 'revisar', el: reviewCard, sub: () => '', lead: 'Confira na prévia ao lado. Depois de criar, você pode pôr uma campanha nele.' },
    ],
    ads: [
      { label: 'Anúncios', short: 'revisar', el: adsCard, sub: () => 'campanhas, página, combinações e botão', title: 'Novos anúncios',
        lead: 'Marque quais imagens vão com quais headlines. Cada marca vira um anúncio em cada campanha.' },
      { label: 'Revisar e adicionar', short: 'revisar', el: reviewCard, sub: () => '',
        lead: `Confira os anúncios como vão sair. Eles entram ${live ? 'ativos' : 'pausados'} em cada campanha escolhida e passam pela revisão do Taboola.` },
    ],
  }[make];
  const title = h('h1', {}, '');
  const lead = h('p', { class: 'lead' }, '');
  const stepOut = h('div');
  const backBtn = h('button', { type: 'button', class: 'ghost back', onclick: () => show(at - 1) }, 'Voltar');
  const cancel = h('a', { class: 'button ghost', href: back }, 'Cancelar');
  const nextBtn = h('button', { type: 'button', class: 'primary', onclick: () => {
    const p = stepProblem(steps[at]);
    if (p) { stepOut.replaceChildren(note('fail', p)); return; }
    show(at + 1);
  } }, 'Próximo');
  const preview = h('div', { class: 'pv-body' });
  const aside = h('aside', { class: 'steps-preview', 'aria-label': 'Prévia na tabela' });
  const content = h('div', { class: 'steps-content' + (make === 'ads' ? ' with-library' : '') });
  let at = 0;
  function drawRail() {
    railSteps.replaceChildren(...steps.map((st, k) => {
      const done = k < at && !stepProblem(st);
      const sub = st.sub();
      return h('li', { class: k === at ? 'on' : done ? 'done' : '' },
        h('button', { type: 'button', 'aria-current': k === at ? 'step' : null, onclick: () => show(k) },
          h('span', { class: 'n' }, done ? '✓' : String(k + 1)), h('span', {}, h('b', {}, st.label), sub ? h('small', {}, sub) : null)));
    }));
  }
  // drawAside is the right column: Novos anúncios' library on its first
  // step, the preview everywhere else.
  function drawAside() {
    const library = make === 'ads' && at === 0;
    aside.classList.toggle('lib-panel', library);
    aside.setAttribute('aria-label', library ? 'Biblioteca do Create' : 'Prévia na tabela');
    content.classList.toggle('with-library', library);
    aside.replaceChildren(...(library ? [lib.el] : [h('h2', {}, 'Prévia na tabela'), preview]));
  }
  function show(i) {
    at = Math.max(0, Math.min(i, steps.length - 1));
    steps.forEach((st, k) => { st.el.hidden = k !== at; });
    title.textContent = steps[at].title || `Passo ${at + 1} · ${steps[at].label}`;
    lead.textContent = steps[at].lead;
    stepOut.replaceChildren();
    backBtn.hidden = at === 0;
    nextBtn.hidden = at === steps.length - 1;
    drawAside();
    drawRail();
    update();
    window.scrollTo?.({ top: 0 });
  }
  const offline = connected.length ? null : note('warn', h('b', {}, 'Taboola desligado. '), make === 'ads' ? 'Monte os anúncios aqui e baixe a planilha no fim para subir pelo Bulk Upload do Taboola.' : 'Criar precisa do Taboola ligado. A planilha do Bulk Upload fica em Novos anúncios.');
  content.append(h('div', { class: 'steps-form' }, h('div', { class: 'steps-head' }, title, lead), offline, ...steps.map((st) => st.el), stepOut,
    h('div', { class: 'steps-next' }, backBtn, cancel, nextBtn)), aside);
  main.append(h('div', { class: 'steps-page' }, rail, content));

  // ---- loading ----
  // Every account's groups and campaigns: the account list (Novo grupo), the
  // group tree (Nova campanha), the campaign tree (Novos anúncios) and the
  // preview read them.
  async function load() {
    if (!connected.length) {
      loaded = true;
      drawAll();
      return;
    }
    try {
      accounts = (await api(`accounts/${s.net}`)).accounts;
    } catch (e) {
      stepOut.replaceChildren(note('fail', e.message));
      return;
    }
    if (!accounts.some((a) => a.id === s.account) && make !== 'ads') {
      const kept = store('launch.acct');
      s.account = (kept !== 'all' && accounts.some((a) => a.id === kept) && kept) || accounts[0]?.id || '';
    }
    const trees = await Promise.all(accounts.map((a) => api(`${s.net}/${encodeURIComponent(a.id)}/tree`).then((t) => ({ a, t }), (e) => ({ a, error: e.message }))));
    groups = [];
    campaignList = [];
    const problems = [];
    for (const { a, t, error } of trees) {
      if (error) { problems.push(`${a.name || a.id}: ${error}`); continue; }
      for (const g of t.groups) groups.push({ ...g, account: a.id });
      for (const c of t.campaigns) campaignList.push({ ...c, account: a.id });
    }
    loaded = true;
    if (problems.length) stepOut.replaceChildren(note('warn', h('b', {}, 'Não consegui ler: '), problems.join(' · ')));
    // A campaign named in the address belongs to its account.
    if (make === 'ads' && !s.account && to.length) s.account = campOf(to[0])?.account || '';
    if (make === 'campaign') {
      openAccts.add(s.account);
      if (s.group && groups.some((g) => g.account === s.account && g.id === s.group)) pickGroup(s.account + '|' + s.group);
      else s.group = '';
    }
    drawAll();
    if (make === 'group') await accountChanged();
  }
  // accountChanged reads what depends on Novo grupo's account: its next
  // group name and its presets.
  async function accountChanged() {
    drawCascade();
    update();
    if (!s.account) return;
    const acct = s.account;
    const [n, p] = await Promise.all([
      api(`${s.net}/${encodeURIComponent(acct)}/next`).catch(() => null), // worked out again when sent
      loadPresets(s.net, acct).catch(() => []),
    ]);
    if (acct !== s.account) return;
    next = n;
    if (!nameTyped) groupName.value = n?.group || '';
    groupPresetHold.replaceChildren(presetBar({ level: 'group', net: s.net, account: acct, form: gForm, list: p, onUse: () => update() }));
    drawCascade();
    update();
  }
  function drawAll() {
    drawAccounts();
    drawTree();
    drawTargets();
    drawChips();
    drawNames();
    drawCascade();
    update();
  }

  // ---- the ads ----
  // ads builds every ad the page would send, in AD order: each ticked cell
  // of the matrix, with its row's headline and description and the button.
  async function ads() {
    const list = matrixAds(s.rows, s.cols, s.ticked).map((m) => {
      const r = s.rows[m.row];
      return { n: m.n, col: m.col, img: s.cols[m.col], row: r, title: clean(r.text), description: (r.desc || '').trim(), cta: s.cta };
    });
    return Promise.all(list.map(async (a) => ({ ...a, adId: await adId(a.img.sha256.slice(0, 10), a.title, '') })));
  }
  const chosenNames = () => to.map((id) => campOf(id)?.name || id);

  let drawn = 0;
  async function update() {
    const run = ++drawn;
    if (make === 'campaign') moreSum.textContent = trackIn.value.trim() ? ' rastreio: ' + trackIn.value.trim().slice(0, 64) + (trackIn.value.trim().length > 64 ? '…' : '') : ' sem rastreio';
    const list = make === 'ads' ? await ads() : [];
    if (run !== drawn) return;
    if (make === 'ads') {
      const camps = Math.max(1, to.length);
      counter.textContent = to.length ? `${plural(list.length, 'marcada', 'marcadas')} × ${plural(to.length, 'campanha', 'campanhas')} = ${plural(list.length * camps, 'anúncio', 'anúncios')}` : plural(list.length, 'marcada', 'marcadas');
      namesLineEl.textContent = namesLine(chosenNames(), list.length);
      if (at === 0) nextBtn.textContent = `Próximo: revisar ${plural(list.length * camps, 'anúncio', 'anúncios')}`;
      drawAI();
    } else if (!nextBtn.hidden) {
      nextBtn.textContent = 'Próximo: ' + steps[at + 1].short;
    }
    drawReview(list);
    drawRail();
  }

  function warnings(list) {
    const w = [];
    const titles = [...new Set(list.map((a) => a.title))];
    const badHl = titles.filter((t) => headlineWarnings(t).length || portuguese(t)).length;
    const imgs = [...new Set(list.map((a) => a.img))];
    const badImg = imgs.filter((x) => imageWarnings({ width: x.width, height: x.height, size: x.bytes, type: x.type }).length).length;
    if (badHl) w.push(`${plural(badHl, 'headline tem', 'headlines têm')} aviso do Taboola.`);
    if (badImg) w.push(`${plural(badImg, 'imagem tem', 'imagens têm')} aviso de tamanho ou formato.`);
    if (list.length > MAX_ADS) w.push(`${list.length} anúncios passam de ${MAX_ADS}, o máximo de uma planilha.`);
    const twice = repeats(list);
    if (twice) w.push(`${plural(twice, 'anúncio repete', 'anúncios repetem')} outro (mesma imagem, headline e botão).`);
    if (s.ai === 'no' && imgs.some((x) => x.ai)) w.push('Marcado como sem IA, mas há imagens que parecem de IA.');
    return w;
  }

  function drawAI() {
    const looks = s.cols.filter((x) => x.ai).length;
    aiBox.replaceChildren(...[
      segmented('ai', [['yes', 'Sim, marcar como IA'], ['no', 'Não']], s.ai, (v) => { s.ai = v; update(); }),
      s.ai === 'no' && looks ? note('warn', `${plural(looks, 'imagem parece feita', 'imagens parecem feitas')} com IA. O Taboola pede que imagens de IA sejam declaradas; a escolha é sua.`) : null,
      !s.ai ? h('p', { class: 'faint' }, 'Escolha antes de adicionar. O Taboola pede que imagens e headlines de saúde feitas com IA sejam declaradas.') : null].filter(Boolean));
  }

  function drawReview(list) {
    const w = make === 'ads' ? warnings(list) : [];
    drawPreview(list, w);
    if (make === 'group') {
      const gf = gForm.get();
      review.replaceChildren(h('p', { class: 'muted' }, `O grupo ${gf.name || next?.group || ''} nasce ${live ? 'rodando' : 'pausado'} em ${acctName(s.account) || '—'}, ainda sem campanhas, para sempre (sem data de fim).`));
      return;
    }
    if (make === 'campaign') {
      review.replaceChildren(h('p', { class: 'muted' }, 'Os anúncios entram depois: ao criar, "Adicionar anúncios" abre Novos anúncios com ' +
        (s.devices === 'both' ? 'as duas campanhas já escolhidas.' : 'a campanha já escolhida.')));
      return;
    }
    // One row per ad; "Tirar" unticks its cell in the matrix.
    const names = chosenNames();
    review.replaceChildren(...[
      w.length ? note('warn', h('b', {}, 'Avisos (não impedem): '), w.join(' ')) : null,
      list.length ? h('p', { class: 'faint' }, `${plural(list.length, 'anúncio', 'anúncios')}, com o botão ${ctaLabel(s.cta)}, em ${to.length ? plural(to.length, 'campanha', 'campanhas') : 'cada campanha'}. ${namesLine(names, list.length)}`) : null,
      list.length ? h('div', { class: 'table-wrap ads-preview' }, h('table', { class: 'list' },
        h('thead', {}, h('tr', {}, h('th', {}, 'Anúncio'), h('th', {}, 'Imagem'), h('th', {}, 'Headline'), h('th', {}, 'Botão'), h('th', {}, ''))),
        h('tbody', {}, list.map((a) => {
          const hw = headlineWarnings(a.title);
          if (portuguese(a.title)) hw.unshift('Parece português: as headlines vão sempre em inglês.');
          return h('tr', {},
            h('td', { class: 'mono' }, 'AD' + pad(a.n), names.length ? h('div', { class: 'faint ad-id' }, names.slice(0, 2).map((c) => adName(c, a.n)).join(' · ') + (names.length > 2 ? ' …' : '')) : null),
            h('td', { class: 'ad-pic' }, h('img', { class: 'mini', src: '/launch/api/images/' + a.img.sha256, alt: '' }), h('span', { class: 'faint' }, letter(a.col))),
            h('td', { class: 'ad-title' }, h('b', {}, a.title), a.description ? h('div', { class: 'faint' }, a.description) : null,
              hw.map((x) => h('div', { class: 'warn-line' }, x)), h('div', { class: 'mono faint ad-id', title: 'Id do anúncio' }, a.adId)),
            h('td', {}, ctaLabel(a.cta)),
            h('td', {}, h('button', { type: 'button', class: 'small ghost', 'aria-label': 'Tirar o AD' + pad(a.n), onclick: () => { s.ticked.delete(cell(a.row.id, a.img.sha256)); changed(); } }, 'Tirar')));
        })))) : h('p', { class: 'faint' }, 'Nenhuma combinação marcada.')].filter(Boolean));
  }

  // drawPreview is the right column: the rows Campanhas will show, the new
  // ones marked "novo", under the group they go in.
  function drawPreview(list, w) {
    const row = (level, tag, name, o = {}) => h('div', { class: `pv-tr level-${level}` + (o.fresh ? ' fresh' : '') },
      h('span', { class: 'tag' }, tag), h('span', { class: 'pv-name', title: name }, name), o.fresh ? h('span', { class: 'new-tag' }, 'novo') : null,
      h('span', { class: 'pv-gap' }), o.state ? badge(o.state) : null);
    const sub = (level, text) => h('p', { class: `pv-sub level-${level} faint` }, text);
    const more = (level, n) => (n > 0 ? sub(level, `…e mais ${n}.`) : null);
    const born = live ? 'RUNNING' : 'PAUSED';
    const byName = (a, b) => String(b.name || '').localeCompare(String(a.name || ''), 'pt-BR', { numeric: true });
    const parts = [];
    if (s.account && make !== 'ads') parts.push(h('p', { class: 'pv-acct faint' }, acctName(s.account)));
    if (make === 'group') {
      parts.push(row(0, 'Grupo', groupName.value.trim() || next?.group || 'Grupo novo', { fresh: true, state: born }));
      const mine = groups.filter((g) => g.account === s.account).sort(byName);
      parts.push(...mine.slice(0, 8).map((g) => row(0, 'Grupo', g.name || g.id, { state: g.status })), more(0, mine.length - 8));
      parts.push(h('p', { class: 'faint pv-foot' }, live ? 'Nasce rodando no Taboola.' : 'Nasce pausado no Taboola.'));
    }
    if (make === 'campaign') {
      const g = groups.find((x) => x.account === s.account && x.id === s.group);
      if (s.newGroup) parts.push(row(0, 'Grupo', next?.group || nextNew.get(s.account)?.group || 'Grupo novo', { fresh: true, state: born }));
      else if (g) parts.push(row(0, 'Grupo', g.name || g.id, { state: g.status }));
      else parts.push(h('p', { class: 'faint' }, connected.length ? 'Escolha o grupo.' : 'Sem conexão com o Taboola.'));
      if (g) {
        const old = campaignList.filter((c) => c.account === s.account && c.group_id === g.id);
        parts.push(...old.slice(0, 8).map((c) => row(1, 'Camp', c.name)), more(1, old.length - 8));
      }
      if (g || s.newGroup) campaignNames().forEach((nm, i) => parts.push(row(1, 'Camp', nm || DEVICES[devicesNow()[i]], { fresh: true })));
      const st = campSettings();
      const where = s.countries.map((c) => COUNTRIES[c] || c).join(', ') || 'nenhum país';
      parts.push(h('p', { class: 'faint pv-foot' }, `${devicesNow().length > 1 ? 'Cada uma' : 'Ela'}: ${st.daily_cap ? money(st.daily_cap) + '/dia' : 'sem orçamento'}, começa ${s.start === 'tomorrow' ? 'amanhã' : 'hoje'}, ${where}. ` +
        (devicesNow().length > 1 ? (live ? 'Nascem rodando no Taboola.' : 'Nascem pausadas no Taboola.') : (live ? 'Nasce rodando no Taboola.' : 'Nasce pausada no Taboola.'))));
    }
    if (make === 'ads') {
      const adsLine = list.length ? plural(list.length, 'anúncio novo', 'anúncios novos') : 'novos anúncios aqui';
      const chosen = to.map((id) => campOf(id) || { id, name: id, account: s.account, group_id: '' });
      const all = [...groups, ...standIns(groups, campaignList)];
      const rows = nest(all, chosen, { state: 'all', device: 'all', text: '' }).filter((r) => r.cs.length);
      const loose = chosen.filter((c) => !all.some((g) => g.account === c.account && (g.id || '') === (c.group_id || '')));
      if (!to.length) parts.push(h('p', { class: 'faint' }, 'Escolha as campanhas.'));
      let shown = 0;
      for (const r of rows) {
        if (shown >= 8) break;
        parts.push(row(0, 'Grupo', r.g.name || r.g.id));
        for (const c of r.cs) {
          if (shown++ >= 8) break;
          parts.push(row(1, 'Camp', c.name), row(2, 'Ad', list.length ? adName(c.name, 1) + (list.length > 1 ? ' a AD' + pad(list.length) : '') : adsLine, { fresh: true }));
        }
      }
      for (const c of loose) if (shown++ < 8) parts.push(row(1, 'Camp', c.name), row(2, 'Ad', adsLine, { fresh: true }));
      parts.push(more(0, to.length - 8));
      if (list.length) {
        parts.push(h('div', { class: 'pv-cards' }, list.slice(0, 4).map((a) => h('figure', { class: 'pv-card' },
          h('img', { src: '/launch/api/images/' + a.img.sha256, alt: '', loading: 'lazy' }),
          h('figcaption', {}, h('b', {}, a.title), a.description ? h('span', { class: 'faint' }, a.description) : null, a.cta ? h('span', { class: 'pv-cta' }, a.cta) : null)))),
        more(0, list.length - 4));
      }
      if (w.length) parts.push(note('warn', h('b', {}, 'Avisos: '), w.join(' ')));
      parts.push(h('p', { class: 'faint pv-foot' }, live ? 'Sobem ativos: começam a gastar assim que o Taboola aprovar.' : 'Entram pausados: alguém liga no Taboola.'));
    }
    preview.replaceChildren(...parts.filter(Boolean));
  }

  // ---- what keeps a step from going on ----
  function stepProblem(st) {
    const el = st.el;
    if (el === campaignCard) {
      if (connected.length) {
        if (!s.account || (!s.newGroup && !s.group)) return 'Escolha o grupo.';
        if (s.newGroup) {
          const p = gForm.problem();
          if (p) return p;
        }
      }
      if (!s.countries.length) return 'Escolha ao menos um país.';
      if (!brandIn.value.trim()) return 'Escreva a marca.';
      const p = set.problem('campaign');
      if (p && /objetivo|CPC|CPA/.test(p)) more.open = true;
      return p;
    }
    if (el === groupCard) {
      if (connected.length && !s.account) return 'Escolha a conta.';
      return gForm.problem();
    }
    if (el === adsCard) {
      if (connected.length && !to.length) return 'Escolha ao menos uma campanha.';
      if (!url()) return 'Falta a página de destino.';
      if (!matrixAds(s.rows, s.cols, s.ticked).length) return 'Marque ao menos uma combinação de imagem e headline.';
    }
    return '';
  }

  function problem() {
    for (const st of steps) {
      const p = st.el === reviewCard ? '' : stepProblem(st);
      if (p) return `${st.label}: ${p}`;
    }
    return '';
  }

  // ---- send ----
  // forget drops the draft once what it held is made.
  const forgetDraft = () => (s.draftId ? api('drafts/' + s.draftId, { method: 'DELETE' }).catch(() => {}) : null);

  async function sendGroup(addCampaign) {
    const p = problem();
    if (p) {
      sendOut.replaceChildren(note('fail', p));
      return;
    }
    await busy(sendBtn, sendOut, async () => {
      const g = await api(`${s.net}/${encodeURIComponent(s.account)}/groups`, { method: 'POST', body: gForm.get() });
      await forgetDraft();
      const q2 = (more) => new URLSearchParams({ account: s.account, ...more });
      sendBtn.hidden = true;
      andCampaign.hidden = true;
      if (addCampaign) {
        location.assign('/launch/new?' + q2({ make: 'campaign', group: g.id }));
        return;
      }
      sendOut.replaceChildren(note('ok', h('b', {}, `Grupo ${g.name || g.id} criado${live ? ', rodando' : ', pausado'}. `), 'Ainda sem campanhas.'),
        h('div', { class: 'actions' }, h('a', { class: 'button primary', href: '/launch/new?' + q2({ make: 'campaign', group: g.id }) }, 'Criar uma campanha nele'),
          h('a', { class: 'button ghost', href: '/launch/campaigns?' + q2({ group: g.id }) }, 'Ver em Campanhas')));
    });
  }

  async function sendAds() {
    const list = await ads();
    const p = problem() || (!list.length ? 'Nenhum anúncio para criar.' : !s.ai ? 'Diga se os anúncios foram feitos com IA.' : '');
    if (p) {
      sendOut.replaceChildren(note('fail', p));
      return;
    }
    await busy(sendBtn, sendOut, async () => {
      // The ads go to each campaign's own account; every campaign gets every
      // ticked cell, in AD order (the server names them per campaign).
      const byAcct = new Map();
      for (const id of to) {
        const acct = campOf(id)?.account || s.account;
        if (!byAcct.has(acct)) byAcct.set(acct, []);
        byAcct.get(acct).push(id);
      }
      const newAds = list.map((a) => ({ title: a.title, description: a.description, url: url(), image: a.img.sha256, cta: a.cta, ad_id: a.adId, ai: s.ai === 'yes' }));
      const done = [];
      for (const [acct, ids] of byAcct) {
        const res = await api(`${s.net}/${encodeURIComponent(acct)}/add-ads`, { method: 'POST', body: { campaigns: ids, new_ads: newAds } });
        done.push(...res.done);
      }
      if (done.some((d) => !d.error)) await forgetDraft();
      sendBtn.hidden = true;
      const nameOf = (id) => campOf(id)?.name || id;
      const one = byAcct.size === 1 ? [...byAcct.keys()][0] : 'all';
      sendOut.replaceChildren(...done.map((d) => (d.error ? note('fail', h('b', {}, nameOf(d.campaign) + ': '), d.ads ? `${plural(d.ads, 'anúncio entrou', 'anúncios entraram')}, mas ` : '', d.error) :
        note('ok', h('b', {}, nameOf(d.campaign) + ': '), (d.ads === 1 ? '1 anúncio adicionado' + (live ? ', ativo' : ', pausado') : `${d.ads} anúncios adicionados` + (live ? ', ativos' : ', pausados'))))),
      h('p', {}, h('a', { href: '/launch/campaigns?' + new URLSearchParams({ account: one, ...(to.length === 1 ? { open: to[0] } : {}) }) }, 'Ver em Campanhas')));
    });
  }

  let sendKey = '';
  async function sendPair() {
    const p = problem();
    if (p) {
      sendOut.replaceChildren(note('fail', p));
      return;
    }
    const body = {
      network: s.net,
      account: s.account,
      name: '',
      devices: s.devices,
      group_id: s.newGroup ? '' : s.group,
      settings: campSettings(),
      ads: [], // they come after, in Novos anúncios
    };
    // A typed name names that campaign; empty ones get the team's.
    if (devicesNow().includes('desktop') && typed.desktop) body.desktop_name = typed.desktop;
    if (devicesNow().includes('mobile') && typed.mobile) body.mobile_name = typed.mobile;
    if (s.newGroup) body.new_group = { ...gForm.get(), name: '' };
    if (presetId) body.preset_id = presetId;
    if (s.draftId) body.draft_id = s.draftId;
    // The same content sent twice (a double click, a retry) is one send.
    const key = JSON.stringify(body);
    if (!sendKey || sendKey.body !== key) sendKey = { body: key, id: crypto.randomUUID?.() || String(Math.random()).slice(2) };
    body.key = sendKey.id;
    await busy(sendBtn, sendOut, async () => {
      let job = await api('pairs', { method: 'POST', body });
      sendBtn.hidden = true;
      while (true) {
        sendOut.replaceChildren(progress(job));
        if (job.done) break;
        await new Promise((r) => setTimeout(r, 1200));
        job = await api('jobs/' + job.id);
      }
      sendOut.replaceChildren(progress(job), result(job));
    });
  }

  function progress(job) {
    const mark = { wait: '○', run: '✱', ok: '✓', fail: '✕' };
    return h('ol', { class: 'steps' }, (job.steps || []).map((st) => h('li', { class: 'step-' + st.state },
      h('span', { class: 'mark' }, mark[st.state] || '○'), ' ', h('b', {}, st.label), st.detail ? h('span', { class: 'muted' }, ' · ' + st.detail) : null)));
  }

  function result(job) {
    const r = job.result || {};
    const camp = (m) => (m?.campaign?.id ? h('a', { href: link(s.net, s.account, r.group_id || '-', m.campaign.id) }, m.campaign.name) : null);
    if (job.error) return note('fail', job.error);
    // The new campaigns have no ads yet: the next page picks them.
    const ids = [r.desktop, r.mobile].map((m) => m?.campaign?.id).filter(Boolean);
    const addAds = ids.length ? h('a', { class: 'button primary', href: '/launch/new?' + new URLSearchParams({ make: 'ads', account: s.account, to: ids.join(',') }) }, 'Adicionar anúncios') : null;
    if (r.result === 'done') {
      const pair = r.desktop && r.mobile;
      return h('div', {}, note('ok', h('b', {}, (pair ? 'Par criado' : 'Campanha criada') + (live ? ', rodando' : (pair ? ', pausado' : ', pausada')) + ', ainda sem anúncios. '),
        live ? 'Começa a gastar quando os anúncios entrarem e o Taboola aprovar.' : 'Ponha os anúncios e ligue no Taboola quando quiser que comece.'),
        h('p', {}, ...[camp(r.desktop), pair ? ' · ' : null, camp(r.mobile)].filter(Boolean)),
        h('div', { class: 'actions' }, addAds, h('a', { class: 'button', href: '/launch/campaigns?' + new URLSearchParams({ account: s.account, group: r.group_id || '' }) }, 'Ver em Campanhas'),
          h('a', { class: 'button ghost', href: '/launch/new?' + new URLSearchParams({ make: 'campaign', account: s.account, group: r.group_id || '' }) }, 'Outra campanha neste grupo')));
    }
    return h('div', {}, note(r.result === 'partial' ? 'warn' : 'fail', h('b', {}, r.result === 'partial' ? 'Criado em parte. ' : 'Nada foi criado. '), (r.problems || []).join(' · ')),
      h('p', {}, camp(r.desktop), r.desktop && r.mobile ? ' · ' : '', camp(r.mobile)),
      h('p', { class: 'faint' }, (live ? 'O que foi criado está rodando' : 'O que foi criado está pausado') + ' e aparece no Histórico.'),
      addAds ? h('div', { class: 'actions' }, addAds) : null);
  }

  // ---- drafts ----
  // Drafts are no longer saved here (IMPLEMENT 43e7b65f44 took out "Salvar
  // rascunho"); the ones already in Rascunhos still open, and go away once
  // made. openDraft fills the page from one: a draft's pictures and
  // headlines become the matrix's columns and rows, with every combination
  // ticked (drafts kept a pairing, not cells).
  function openDraft(d) {
    const b = d.body || {};
    s.draftId = d.id;
    s.net = b.net || s.net;
    s.account = b.account || s.account;
    s.group = b.group || '';
    s.newGroup = make === 'campaign' && !!b.newGroup;
    s.devices = b.devices || 'both';
    const dv = deviceBox.querySelector(`input[value=${s.devices}]`);
    if (dv) dv.checked = true;
    presetId = b.preset_id || null;
    set.set({ ...(b.settings || {}) });
    if (b.group_fields) {
      gForm.name.value = b.group_fields.name || '';
      nameTyped = !!gForm.name.value;
      gForm.set(b.group_fields);
    }
    s.cols = (b.images || []).filter((x) => x.on !== false);
    s.rows = (b.headlines || []).filter((x) => x.on !== false && clean(x.text || '')).map((x) => ({ id: newId(), text: clean(x.text), desc: b.ad_desc || '' }));
    for (const r of s.rows) for (const c of s.cols) s.ticked.add(cell(r.id, c.sha256));
    if ((b.ctas || []).length) s.cta = b.ctas[0];
    s.ai = b.ai || '';
    to.splice(0, to.length, ...(b.to || to));
    adUrl.value = b.ad_url || '';
    rail.querySelector('.rail-label').textContent = MAKES[make] + ' · rascunho';
  }

  // ---- the bulk sheet ----
  async function downloadSheet() {
    await busy(sheetBtn, sheetOut, async () => {
      const list = await ads();
      const ids = campaignIds(sheetIds.value);
      if (!ids.length) throw new Error('Diga os ids das campanhas que recebem os anúncios.');
      if (!list.length) throw new Error('Marque ao menos uma combinação de imagem e headline.');
      const used = [...new Set(list.map((a) => a.img))];
      const names = uniqueNames(used.map((x) => x.name || 'imagem.jpg'));
      const fileOf = new Map(used.map((x, i) => [x, names[i]]));
      const camps = ids.map((id) => campOf(id)?.name || '');
      // A row goes in every campaign, so it has one description: each ad's own.
      const rows = adRows(list.map((a) => ({ creativeFile: fileOf.get(a.img), title: a.title, cta: a.cta, customId: a.adId, adName: sheetName(camps, a.n) })),
        { campaigns: ids, url: url(), description: '', ai: { yes: 'Yes', no: 'No' }[s.ai] || '' });
      const at2 = AD_COLUMNS.indexOf('Description');
      list.forEach((a, k) => { rows[k][at2] = a.description; });
      const base = new Uint8Array(await (await fetch('/launch/_ads/realize-base.xlsx')).arrayBuffer());
      const sheet = await fillTemplate(base, AD_COLUMNS, rows);
      const files = [];
      for (const x of used) {
        const res = await fetch('/launch/api/images/' + x.sha256);
        files.push({ name: fileOf.get(x), data: new Uint8Array(await res.arrayBuffer()) });
      }
      const stamp = new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '');
      save(new Blob([sheet], { type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' }), `taboola-anuncios-${stamp}.xlsx`);
      save(new Blob(zip(files), { type: 'application/zip' }), `taboola-imagens-${stamp}.zip`);
      sheetOut.replaceChildren(note('ok', `${plural(rows.length, 'anúncio', 'anúncios')} na planilha e ${plural(files.length, 'imagem', 'imagens')} no ZIP. Suba os dois no Bulk Upload do Taboola.`));
    });
  }

  // ---- start ----
  if (draft) openDraft(draft);
  brandAndCap();
  drawCountries();
  drawCTAs();
  drawMatrix();
  for (const st of steps) {
    st.el.addEventListener('input', () => update());
    st.el.addEventListener('change', () => update());
  }
  show(0);
  if (lib) {
    lib.load();
    // Create sends people here with the set it just saved: /launch/new?set=7
    // opens Novos anúncios with that folder, its pictures as columns and its
    // headlines as rows, nothing ticked yet.
    if (/^\d+$/.test(q.get('set') || '')) {
      lib.open(q.get('set')).then(async ({ creatives, headlines }) => {
        for (const x of headlines) addRow(x.text, { library: x.id, ai: x.ai_label === 'ai' });
        const problems = [];
        for (const c of creatives) {
          try {
            const r = await api('library/use?id=' + encodeURIComponent(c.id), { method: 'POST' });
            addCol({ ...r.image, ai: c.ai_label === 'ai', library: c.id });
          } catch (e) {
            problems.push(`${c.name || 'criativo ' + c.id}: ${e.message}`);
          }
        }
        upNote.replaceChildren(...problems.map((p) => note('fail', p)));
      }, (e) => upNote.replaceChildren(note('fail', e.message)));
    }
  }
  await load();
  show(at);
}

// brl writes a number the team's way: 500 is "500,00".
function brl(n) {
  return n.toLocaleString('pt-BR', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function save(blob, name) {
  const a = h('a', { href: URL.createObjectURL(blob), download: name });
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 10000);
}
