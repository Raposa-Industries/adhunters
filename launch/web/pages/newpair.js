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
import { groupFields, settingsForm, OBJECTIVES, TEAM } from './presets.js';
import { repeats } from './adset.js';
import { nest, standIns } from './rows.js';
import { clean, headlineWarnings, imageWarnings, looksAIMade, urlWarnings } from '/launch/_ads/checks.js';
import { CTAS, AD_COLUMNS, MAX_ADS, adId, adRows, uniqueNames, campaignIds } from '/launch/_ads/sheet.js';
import { fillTemplate } from '/launch/_ads/template.js';
import { zip } from '/launch/_ads/zip.js';
import { libraryPanel } from './library.js';
import { CHIPS, moreCTAs, letter, cell, matrixAds, toggle, forget, groupPrefix, campaignName, adName, namesLine, sheetName, shortName, adAI } from './matrix.js';

// Portuguese in a headline: accents Portuguese uses and English does not,
// and a few common words. Headlines always go out in English.
const PT = /[ãõçâêô]|\b(você|para|como|seu|sua|não|mais|de|que|com|uma?)\b/i;

export function portuguese(text) {
  return PT.test(text);
}

// MAKES name each kind of new item: the rail's title.
const MAKES = { campaign: 'Nova campanha', group: 'Novo grupo', ads: 'Novos anúncios' };

// COUNTRIES are the countries a campaign can show in: every ISO 3166 code
// with a name in Portuguese (Taboola takes any two-letter code). The
// excluded cities are US cities, so they go only with the United States.
const REGION = (() => {
  try {
    return new Intl.DisplayNames(['pt-BR'], { type: 'region', fallback: 'none' });
  } catch {
    return null;
  }
})();
const NOT_COUNTRIES = new Set(['EU', 'EZ', 'UN', 'QO', 'XA', 'XB', 'ZZ', 'AN', 'BU', 'CS', 'DD', 'FX', 'NT', 'SU', 'TP', 'YD', 'YU', 'ZR', 'AC', 'CP', 'DG', 'EA', 'IC', 'TA', 'CQ']);
export const COUNTRIES = (() => {
  const out = [];
  if (REGION) {
    for (let a = 65; a <= 90; a++) {
      for (let b = 65; b <= 90; b++) {
        const code = String.fromCharCode(a, b);
        if (NOT_COUNTRIES.has(code)) continue;
        let name;
        try { name = REGION.of(code); } catch { name = undefined; }
        if (name && name !== code) out.push([code, name]);
      }
    }
  }
  if (!out.some(([c]) => c === 'US')) out.push(['US', 'Estados Unidos']);
  return out.sort((x, y) => x[1].localeCompare(y[1], 'pt-BR'));
})();
export const countryName = (code) => COUNTRIES.find(([c]) => c === code)?.[1] || code;
// fold is text compared without case or accents ("canada" finds Canadá).
const fold = (t) => String(t || '').trim().toLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, '');

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

// svgIcon draws a small line icon (Ember's 16 px set) from path data.
function svgIcon(cls, d, box = '0 0 16 16') {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  for (const [k, v] of Object.entries({ viewBox: box, 'aria-hidden': 'true', class: cls })) svg.setAttribute(k, v);
  for (const one of [].concat(d)) {
    const p = document.createElementNS(ns, 'path');
    p.setAttribute('d', one);
    svg.append(p);
  }
  return svg;
}

// chevron points right (closed), down (open) or up.
const chevron = (dir) => svgIcon('chev chev-' + dir, { right: 'M6 4l4 4-4 4', down: 'M4 6l4 4 4-4', up: 'M4 10l4-4 4 4' }[dir]);

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
    draftId: 0,
    devices: 'both',
    start: 'today', // or 'tomorrow'
    countries: ['US'],
    cols: [], // the matrix's pictures: {sha256, name, type, bytes, width, height, ai, library}
    rows: [], // its headlines: {id, text, desc, cta, library, ai}
    ticked: new Set(), // cell(row id, sha256)
    cta: 'Learn More', // the button the last row was given; a new row starts with it
    aiChosen: new Map(), // cell key → the AI label the person chose for that ad in Revisar
    ctaChosen: new Map(), // cell key → the button the person chose for that ad in Revisar
  };
  let next = null; // the names for the chosen group, or the account's new group: actions.NextNames
  let accounts = [];
  let loaded = false;
  let groups = []; // every account's groups, each with its account
  let campaignList = []; // every account's campaigns, each with its account
  const acctName = (id) => accounts.find((a) => a.id === id)?.name || id;

  const back = '/launch/campaigns?' + new URLSearchParams(Object.entries({ account: s.account, group: s.group }).filter(([, v]) => v));

  // ---- the left rail ----
  const railSteps = h('ol', { class: 'rail-steps' });
  const rail = h('aside', { class: 'steps-rail', 'aria-label': 'Passos' },
    h('div', {}, h('div', { class: 'rail-label' }, MAKES[make]), railSteps));

  // A new group gets the team's defaults: objective Online Purchases, each
  // campaign with its own budget, no end date (Figma "Launch · Novo grupo"
  // asks only the account and the name).
  const gForm = groupFields();
  const set = settingsForm({}, limits);
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
      h('label', { class: 'field group-name' }, 'Nome', groupName, h('span', { class: 'hint' }, 'o próximo número nesta conta')),
      h('div', { class: 'field' }, 'Os nomes descem assim', cascade))) : null;

  function drawAccounts() {
    if (make !== 'group') return;
    if (!connected.length) { acctList.replaceChildren(h('p', { class: 'faint pad' }, 'Sem conexão com o Taboola: criar precisa dele.')); return; }
    if (!accounts.length) { acctList.replaceChildren(h('p', { class: 'faint pad' }, loaded ? 'Nenhuma conta. Adicione em Contas.' : 'Carregando as contas…')); return; }
    acctList.replaceChildren(...accounts.map((a) => {
      const n = groups.filter((g) => g.account === a.id).length;
      return h('label', { class: 'radio-row' + (a.id === s.account ? ' on' : '') },
        h('input', { type: 'radio', name: 'acct', value: a.id, checked: a.id === s.account, onchange: () => { s.account = a.id; drawAccounts(); accountChanged(); } }),
        h('span', { class: 'rr-name' }, a.name || a.id), h('span', { class: 'mono faint' }, a.id), h('span', { class: 'gap' }),
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
  // The group, in an account › group tree (Figma "Launch · Nova campanha").
  const tree = h('div', { class: 'radio-list tree', role: 'radiogroup', 'aria-label': 'Grupo' });
  const openAccts = new Set();
  const byNumber = (a, b) => String(a.name || a.id).localeCompare(String(b.name || b.id), 'pt-BR', { numeric: true });
  function drawTree() {
    if (make !== 'campaign') return;
    if (!connected.length) { tree.replaceChildren(h('p', { class: 'faint pad' }, 'Sem conexão com o Taboola: criar precisa dele.')); return; }
    if (!accounts.length) { tree.replaceChildren(h('p', { class: 'faint pad' }, loaded ? 'Nenhuma conta. Adicione em Contas.' : 'Carregando os grupos…')); return; }
    tree.replaceChildren(...accounts.flatMap((a) => {
      const mine = groups.filter((g) => g.account === a.id).sort(byNumber);
      const open = openAccts.has(a.id);
      const head = h('button', { type: 'button', class: 'tree-acct', 'aria-expanded': String(open), onclick: () => { open ? openAccts.delete(a.id) : openAccts.add(a.id); drawTree(); } },
        chevron(open ? 'down' : 'right'), h('span', { class: 'rr-name' }, a.name || a.id), h('span', { class: 'mono faint' }, a.id), h('span', { class: 'gap' }),
        h('span', { class: 'faint' }, mine.length ? plural(mine.length, 'grupo', 'grupos') : 'nenhum grupo'));
      if (!open) return [head];
      const row = (value, label, sub, on) => h('label', { class: 'radio-row in' + (on ? ' on' : '') },
        h('input', { type: 'radio', name: 'grp', value, checked: on, onchange: () => pickGroup(value) }), h('span', { class: 'rr-name' }, label), h('span', { class: 'gap' }), sub ? h('span', { class: 'faint' }, sub) : null);
      if (!mine.length) return [head, h('p', { class: 'faint pad in' }, 'Nenhum grupo nesta conta. Crie um em Novo › Grupo de campanha.')];
      return [head, ...mine.map((g) => {
        const n = campaignList.filter((c) => c.account === a.id && c.group_id === g.id).length;
        return row(a.id + '|' + g.id, g.name || g.id, `${plural(n, 'campanha', 'campanhas')} · ${stateName(g.status)}`, s.account === a.id && s.group === g.id);
      })];
    }));
  }
  // pickGroup chooses the group (account|id) and reads the names the
  // campaigns get in it.
  async function pickGroup(v) {
    [s.account, s.group] = v.split('|');
    openAccts.add(s.account);
    // The campaigns take their group's objective (Maximize conversions needs one of conversions).
    const g = groups.find((x) => x.account === s.account && x.id === s.group);
    if (g?.objective && OBJECTIVES.some(([v2]) => v2 === g.objective)) set.set({ settings: { objective: g.objective } });
    next = null;
    drawTree();
    drawNames();
    update();
    const acct = s.account;
    const group = s.group;
    const n = await api(`${s.net}/${encodeURIComponent(acct)}/next?group=` + encodeURIComponent(group)).catch(() => null); // worked out again when sent
    if (acct !== s.account || group !== s.group) return;
    next = n;
    drawNames();
    update();
  }

  const devicesNow = () => (s.devices === 'both' ? ['desktop', 'mobile'] : [s.devices]);
  const deviceBox = h('div', { class: 'device-cards', role: 'radiogroup', 'aria-label': 'Dispositivo' }, DEVICE_CARDS.map(([v, label, sub]) =>
    h('label', { class: 'device-card' }, h('input', { type: 'radio', name: 'devices', value: v, checked: s.devices === v, onchange: () => { s.devices = v; drawNames(); update(); } }),
      icon(v), h('span', { class: 'dev-text' }, h('b', {}, label), h('small', {}, sub)))));
  const startBox = segmented('start', [['today', 'Hoje'], ['tomorrow', 'Amanhã']], s.start, (v) => { s.start = v; update(); });
  startBox.classList.add('start-seg');
  const startDate = () => (s.start === 'tomorrow' ? tomorrow() : ''); // empty: Taboola starts it today

  // The brand and the daily budget are the settings form's own fields, laid
  // out here. The brand starts as the last one used in this browser.
  const brandIn = set.parts.brand.querySelector('input');
  brandIn.setAttribute('aria-label', 'Marca');
  brandIn.placeholder = 'Nerve Health Report';
  if (!brandIn.value) brandIn.value = store('launch.brand') || '';
  const capIn = set.parts.cap.querySelector('input');
  capIn.setAttribute('aria-label', 'Orçamento diário');
  const brandAndCap = () => { if (numberOf(capIn.value)) capIn.value = brl(numberOf(capIn.value)); };
  capIn.addEventListener('change', brandAndCap);
  // The server's ceilings still apply (it refuses what passes them).
  const capHint = limits.max_spend_limit ? `cada uma gasta até ${money(limits.max_spend_limit)} no total` : 'sem limite de gasto';

  // Países: any country Taboola takes (ISO 3166 two letters), by its name
  // in Portuguese; the United States by default.
  const countryIn = input({ placeholder: 'adicionar país', list: 'launch-countries', 'aria-label': 'Adicionar país' });
  const countryHint = h('span', { class: 'hint' }, '');
  const countryBox = h('div', { class: 'chip-input', onclick: (e) => { if (e.target === countryBox) countryIn.focus(); } });
  function drawCountries() {
    countryBox.replaceChildren(...s.countries.map((c) => h('span', { class: 'chip-x' }, countryName(c),
      h('button', { type: 'button', 'aria-label': 'Tirar ' + countryName(c), onclick: () => { s.countries = s.countries.filter((x) => x !== c); drawCountries(); update(); } }, '×'))),
    countryIn, h('datalist', { id: 'launch-countries' }, COUNTRIES.filter(([c]) => !s.countries.includes(c)).map(([, name]) => h('option', { value: name }))));
  }
  function addCountry() {
    const t = fold(countryIn.value);
    if (!t) return;
    const hit = COUNTRIES.find(([c, name]) => fold(c) === t || fold(name) === t);
    if (!hit) { countryHint.textContent = 'Não achei esse país; escolha um da lista.'; return; }
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
        typed[d] || team || (s.group ? '…' : 'escolha o grupo'));
    }));
  }
  const campaignNames = () => devicesNow().map((d) => typed[d] || teamName(d));

  // Mais configurações: only the tracking code; the objective, bid, ad
  // delivery and excluded cities go with the team's defaults (TEAM).
  const trackIn = set.parts.tracking.querySelector('textarea');
  const moreSum = h('span', { class: 'mono faint more-sum' }, '');
  const more = h('details', { class: 'more box' }, h('summary', {}, chevron('right'), h('b', {}, 'Mais configurações'), moreSum),
    set.parts.tracking);
  const campaignCard = make === 'campaign' ? h('section', { class: 'form-card' },
    h('div', { class: 'field' }, fieldHead('Grupo', 'conta › grupo'), tree),
    h('div', { class: 'field' }, 'Dispositivo', deviceBox),
    h('div', { class: 'two' },
      h('div', { class: 'field' }, 'Começa', startBox, h('span', { class: 'hint' }, 'roda o dia todo, sem data de fim')),
      h('label', { class: 'field' }, 'Orçamento diário', h('span', { class: 'affix' }, h('span', { class: 'affix-pre' }, 'US$'), capIn, h('span', { class: 'faint' }, 'por campanha')),
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
  const adUrl = input({ type: 'text', inputmode: 'url', placeholder: 'site.com/pagina', spellcheck: 'false', 'aria-label': 'Página de destino' });
  const urlWarn = h('div');
  // An address typed without https:// gets it.
  const url = () => { const u = adUrl.value.trim(); return !u || /^[a-z][a-z0-9+.-]*:\/\//i.test(u) ? u : 'https://' + u; };
  adUrl.addEventListener('input', () => urlWarn.replaceChildren(...urlWarnings(url()).map((w) => h('div', { class: 'warn-line' }, w))));
  const urlBox = h('label', { class: 'affix url-box' }, svgIcon('link-icon', ['M6.5 9.5l3-3', 'M7.5 4.5l1-1a2.8 2.8 0 0 1 4 4l-1 1', 'M8.5 11.5l-1 1a2.8 2.8 0 0 1-4-4l1-1']), adUrl);
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
      return h('span', { class: 'chip-x mono', title: name + (c ? ' · ' + acctName(c.account) : '') }, shortName(name),
        h('button', { type: 'button', 'aria-label': 'Tirar ' + name, onclick: () => { to.splice(to.indexOf(id), 1); drawTargets(); changed(); } }, '×'));
    }), h('span', { class: 'gap' }), h('button', { type: 'button', class: 'add-link', 'aria-expanded': String(!pickPop.hidden), onclick: () => { pickPop.hidden = !pickPop.hidden; drawTargets(); drawChips(); if (!pickPop.hidden) findCamp.focus(); } }, '+ Escolher'));
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
  const namesLineEl = h('p', { class: 'faint names-line' }, '');

  function addCol(img) {
    if (s.cols.some((x) => x.sha256 === img.sha256)) return;
    s.cols.push(img);
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
    const r = { id: newId(), text: t, desc: '', cta: s.cta, ...extra };
    s.rows.push(r);
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
            h('button', { type: 'button', class: 'mx-hl', title: 'Marcar ou desmarcar a linha toda (tire a headline clicando nela na biblioteca)', onclick: () => { toggle(s.ticked, s.cols.map((c) => cell(r.id, c.sha256))); changed(); } }, r.text),
            h('input', { type: 'text', class: 'mx-desc', value: r.desc || '', placeholder: 'Descrição (opcional)', 'aria-label': 'Descrição de ' + r.text, oninput: (e) => { r.desc = e.target.value; } }),
            rowCTAs(r),
            w.map((x) => h('div', { class: 'warn-line' }, x))),
          ...s.cols.map((c, ci) => {
            const k = cell(r.id, c.sha256);
            const on = s.ticked.has(k);
            return h('td', { class: 'mx-cell' + (on ? ' on' : '') }, h('label', {},
              h('input', { type: 'checkbox', class: 'box', checked: on, 'aria-label': `${r.text} com a imagem ${letter(ci)}`, onchange: () => { on ? s.ticked.delete(k) : s.ticked.add(k); changed(); } }),
              on ? h('span', { class: 'mx-ad' }, 'AD' + pad(nOf.get(ri + ':' + ci))) : null));
          }),
          h('td', { class: 'mx-add' }));
      })),
      h('tfoot', {}, h('tr', {}, h('td', { colspan: String(s.cols.length + 2) }, h('div', { class: 'mx-foot' },
        h('button', { type: 'button', class: 'add-link', onclick: () => lib.focusHeadlines() }, '+ headline da biblioteca')))))));
    if (!s.rows.length && !s.cols.length) matrixBox.prepend(h('p', { class: 'faint mx-empty' }, 'Clique nas imagens e headlines da biblioteca ao lado: cada imagem vira uma coluna e cada headline uma linha. Depois marque as combinações.'));
  }

  // rowCTAs is a row's Botão, under its description: it goes on every ad of
  // the row (Revisar can still change one ad's).
  function rowCTAs(r) {
    const more = moreCTAs(CTAS);
    const inMore = more.includes(r.cta);
    const pick = (v) => {
      r.cta = v;
      s.cta = v;
      for (const c of s.cols) s.ctaChosen.delete(cell(r.id, c.sha256));
      changed();
    };
    return h('div', { class: 'cta-chips mx-cta', role: 'radiogroup', 'aria-label': 'Botão de ' + r.text },
      ...CHIPS.map(([v, label]) => h('button', { type: 'button', class: 'cta-chip' + (r.cta === v ? ' on' : ''), role: 'radio', 'aria-checked': String(r.cta === v),
        onclick: () => pick(v) }, label)),
      select([['', 'Mais'], ...more.map((c) => [c, c])], inMore ? r.cta : '', { class: 'cta-more' + (inMore ? ' on' : ''), 'aria-label': 'Outros botões do Taboola',
        onchange: (e) => { if (e.target.value) pick(e.target.value); } }));
  }
  const ctaLabel = (c) => CHIPS.find(([v]) => v === c)?.[1] || c;
  // ctaSelect is one ad's button in Revisar.
  const ctaSelect = (a) => select([...CHIPS.map(([v, label]) => [v, label]), ...moreCTAs(CTAS).map((c) => [c, c])], a.cta,
    { class: 'rv-cta', 'aria-label': 'Botão do AD' + pad(a.n), onchange: (e) => { s.ctaChosen.set(cell(a.row.id, a.img.sha256), e.target.value); update(); } });

  const adsCard = make === 'ads' ? h('section', { class: 'form-card' },
    h('div', { class: 'two' },
      h('div', { class: 'field' }, fieldHead('Campanhas', chosenCount), campChips),
      h('div', { class: 'field' }, fieldHead('Página de destino', 'obrigatória'), urlBox, urlWarn)),
    pickPop,
    h('div', { class: 'field' }, fieldHead('Combinações', counter), matrixBox, fileIn, upNote),
    namesLineEl) : null;

  // changed redraws what the matrix's parts show, then the rest.
  function changed() {
    drawChips();
    drawMatrix();
    lib?.draw();
    update();
  }

  // ---- Revisar ----
  const review = h('div', { class: 'review' });
  const sendOut = h('div');
  // The send button takes "Próximo"'s place on the last step.
  const sendBtn = h('button', { type: 'button', class: 'primary', onclick: () => (make === 'group' ? sendGroup() : make === 'ads' ? sendAds() : sendPair()) }, '');
  const sheetIds = input({ placeholder: '123456, 123457', 'aria-label': 'Ids das campanhas para a planilha' });
  const sheetOut = h('div');
  const sheetBtn = h('button', { type: 'button', onclick: () => downloadSheet() }, 'Baixar planilha e imagens');
  const reviewCard = h('section', { class: make === 'ads' ? 'ads-review' : 'form-card' }, review,
    sendOut,
    make !== 'ads' || connected.length ? null : h('details', { class: 'sheet', open: true }, h('summary', {}, 'Subir à mão pelo Bulk Upload'),
      h('p', { class: 'muted' }, 'A planilha usa as campanhas que já existem no Taboola. Os anúncios entram pausados.'),
      h('div', { class: 'fields' }, field('Ids das campanhas', sheetIds, 'cada anúncio vai em todas')),
      h('div', { class: 'actions' }, sheetBtn), sheetOut));
  // The card a finished send shows, in place of the steps' cards.
  const doneCard = h('section', { class: 'form-card done-card', hidden: true });

  // ---- the steps ----
  const both = () => devicesNow().length > 1;
  const steps = {
    campaign: [
      { label: 'Campanha', short: 'revisar', el: campaignCard, sub: () => 'grupo, dispositivo e orçamento',
        lead: () => 'Sai uma campanha por dispositivo, com o nome do grupo na frente.' },
      { label: 'Revisar e criar', title: 'Passo 2 · Revisar', el: reviewCard, sub: () => '',
        lead: () => 'Confira antes de criar. ' + (both() ? `As duas campanhas nascem ${live ? 'rodando' : 'pausadas'} no Taboola.` : `A campanha nasce ${live ? 'rodando' : 'pausada'} no Taboola.`) },
    ],
    group: [
      { label: 'Grupo', short: 'revisar', el: groupCard, sub: () => 'conta e nome',
        lead: () => 'O grupo nasce sem campanhas. Escolha a conta; o nome já vem pronto.' },
      { label: 'Revisar e criar', title: 'Passo 2 · Revisar', el: reviewCard, sub: () => '',
        lead: () => `Confira antes de criar. O grupo nasce ${live ? 'rodando' : 'pausado'} no Taboola, ainda sem campanhas.` },
    ],
    ads: [
      { label: 'Anúncios', el: adsCard, sub: () => 'campanhas, página, combinações e botão', title: 'Novos anúncios',
        lead: () => 'Marque quais imagens vão com quais headlines. Cada marca vira um anúncio em cada campanha.' },
      { label: 'Revisar e adicionar', title: 'Passo 2 · Revisar', el: reviewCard, sub: () => '',
        lead: () => `Um anúncio por linha; cada um vai ${to.length === 1 ? 'na campanha escolhida' : to.length === 2 ? 'nas duas campanhas' : to.length ? `nas ${to.length} campanhas` : 'em cada campanha escolhida'}. Tire o que não quiser antes de adicionar.` },
    ],
  }[make];
  const title = h('h1', {}, '');
  const lead = h('p', { class: 'lead' }, '');
  const stepOut = h('div');
  const backBtn = h('button', { type: 'button', class: 'back', onclick: () => show(at - 1) }, 'Voltar');
  const cancel = h('a', { class: 'button', href: back }, 'Cancelar');
  const nextBtn = h('button', { type: 'button', class: 'primary', onclick: () => {
    const p = stepProblem(steps[at]);
    if (p) { stepOut.replaceChildren(note('fail', p)); return; }
    show(at + 1);
  } }, 'Próximo');
  const nextRow = h('div', { class: 'steps-next' }, backBtn, cancel, nextBtn, connected.length ? sendBtn : null);
  const preview = h('div', { class: 'pv-body' });
  const aside = h('aside', { class: 'steps-preview', 'aria-label': 'Prévia na tabela' });
  const content = h('div', { class: 'steps-content' + (make === 'ads' ? ' with-library' : '') });
  let at = 0;
  let finished = false; // the send is done: every step is checked
  const check = () => svgIcon('check-icon', 'M3.5 8.5l3 3 6-7');
  function drawRail() {
    railSteps.replaceChildren(...steps.map((st, k) => {
      const done = finished || (k < at && !stepProblem(st));
      const sub = st.sub();
      return h('li', { class: !finished && k === at ? 'on' : done ? 'done' : '' },
        h('button', { type: 'button', 'aria-current': !finished && k === at ? 'step' : null, disabled: finished, onclick: () => show(k) },
          h('span', { class: 'n' }, done ? check() : String(k + 1)), h('span', {}, h('b', {}, st.label), sub ? h('small', {}, sub) : null)));
    }));
  }
  // drawAside is the right column: Novos anúncios' library on its first
  // step, the preview everywhere else.
  // Novos anúncios' Revisar has neither: its table takes the whole width.
  function drawAside() {
    const library = make === 'ads' && at === 0 && !finished;
    const wide = make === 'ads' && at > 0 && !finished;
    aside.hidden = wide;
    content.classList.toggle('wide', wide);
    aside.classList.toggle('lib-panel', library);
    aside.setAttribute('aria-label', library ? 'Biblioteca do Create' : 'Prévia na tabela');
    content.classList.toggle('with-library', library);
    aside.replaceChildren(...(library ? [lib.el] : [h('h2', {}, 'Prévia na tabela'), preview]));
  }
  function show(i) {
    if (finished) return;
    at = Math.max(0, Math.min(i, steps.length - 1));
    steps.forEach((st, k) => { st.el.hidden = k !== at; });
    title.textContent = steps[at].title || `Passo ${at + 1} · ${steps[at].label}`;
    lead.textContent = steps[at].lead();
    stepOut.replaceChildren();
    const last = at === steps.length - 1;
    backBtn.hidden = at === 0;
    nextBtn.hidden = last;
    sendBtn.hidden = !last;
    drawAside();
    drawRail();
    update();
    window.scrollTo?.({ top: 0 });
  }
  // finish shows what a send made: its own title and card, every step
  // checked, and the buttons that go on from it.
  function finish(head, sub, card, actions) {
    finished = true;
    for (const st of steps) st.el.hidden = true;
    title.textContent = head;
    lead.textContent = sub;
    stepOut.replaceChildren();
    doneCard.replaceChildren(...card);
    doneCard.hidden = false;
    nextRow.replaceChildren(...actions);
    drawAside();
    drawRail();
    update();
  }
  const offline = connected.length ? null : note('warn', h('b', {}, 'Taboola desligado. '), make === 'ads' ? 'Monte os anúncios aqui e baixe a planilha no fim para subir pelo Bulk Upload do Taboola.' : 'Criar precisa do Taboola ligado. A planilha do Bulk Upload fica em Novos anúncios.');
  content.append(h('div', { class: 'steps-form' }, h('div', { class: 'steps-head' }, title, lead), offline, ...steps.map((st) => st.el), doneCard, stepOut, nextRow), aside);
  main.append(h('div', { class: 'steps-page' + (make === 'ads' ? ' ads' : '') }, rail, content));

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
      // The group from the address, else the account's first group.
      if (!groups.some((g) => g.account === s.account && g.id === s.group)) {
        const first = groups.filter((g) => g.account === s.account).sort(byNumber)[0] || groups.filter((g) => accounts.some((a) => a.id === g.account)).sort(byNumber)[0];
        s.group = first?.id || '';
        if (first) s.account = first.account;
      }
      openAccts.add(s.account);
      if (s.group) pickGroup(s.account + '|' + s.group);
    }
    drawAll();
    if (make === 'group') {
      await accountChanged();
      if (at === 0 && !document.activeElement?.matches?.('input, textarea')) groupName.focus();
    }
  }
  // accountChanged reads what depends on Novo grupo's account: its next
  // group name.
  async function accountChanged() {
    drawCascade();
    update();
    if (!s.account) return;
    const acct = s.account;
    const n = await api(`${s.net}/${encodeURIComponent(acct)}/next`).catch(() => null); // worked out again when sent
    if (acct !== s.account) return;
    next = n;
    if (!nameTyped) groupName.value = n?.group || '';
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
      const img = s.cols[m.col];
      const k = cell(r.id, img.sha256);
      const cta = s.ctaChosen.has(k) ? s.ctaChosen.get(k) : r.cta ?? s.cta;
      return { n: m.n, col: m.col, img, row: r, title: clean(r.text), description: (r.desc || '').trim(), cta, ai: adAI(r, img, s.aiChosen) };
    });
    return Promise.all(list.map(async (a) => ({ ...a, adId: await adId(a.img.sha256.slice(0, 10), a.title, '') })));
  }
  const chosenNames = () => to.map((id) => campOf(id)?.name || id);

  let drawn = 0;
  async function update() {
    const run = ++drawn;
    if (make === 'campaign') moreSum.textContent = trackIn.value.trim() ? 'rastreio: ' + trackIn.value.trim() : 'sem rastreio';
    const list = make === 'ads' ? await ads() : [];
    if (run !== drawn) return;
    if (make === 'ads') {
      const camps = Math.max(1, to.length);
      const total = plural(list.length * camps, 'anúncio', 'anúncios');
      counter.textContent = to.length ? `${plural(list.length, 'marcada', 'marcadas')} × ${plural(to.length, 'campanha', 'campanhas')} = ${total}` : plural(list.length, 'marcada', 'marcadas');
      namesLineEl.textContent = namesLine(chosenNames(), list.length);
      nextBtn.textContent = `Próximo: revisar ${total}`;
      sendBtn.textContent = 'Adicionar ' + total;
    } else {
      nextBtn.textContent = 'Próximo: revisar';
      sendBtn.textContent = make === 'group' ? 'Criar grupo' : 'Criar ' + plural(devicesNow().length, 'campanha', 'campanhas');
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
    const bare = list.filter((a) => !a.ai && a.img.ai).length;
    if (bare) w.push(`${plural(bare, 'anúncio vai', 'anúncios vão')} sem o rótulo de IA, mas com imagem que parece de IA.`);
    return w;
  }

  function drawReview(list) {
    const w = make === 'ads' ? warnings(list) : [];
    drawPreview(list, w);
    const facts = (pairs) => h('dl', { class: 'rv-facts' }, pairs.flatMap(([k, v]) => [h('dt', {}, k), h('dd', {}, v)]));
    const box = (tag, name, pairs) => h('div', { class: 'rv-box' }, h('div', { class: 'rv-name' }, h('span', { class: 'tag' }, tag), h('span', { class: 'mono' }, name)), facts(pairs));
    if (make === 'group') {
      const name = groupName.value.trim() || next?.group || 'Grupo novo';
      review.replaceChildren(
        h('div', { class: 'rv-head' }, h('b', {}, acctName(s.account) || '—'), h('span', { class: 'faint' }, '1 grupo novo')),
        h('div', { class: 'rv-boxes one' }, box('Grupo', name, [
          ['Conta', acctName(s.account) || '—'],
          ['Objetivo', 'Compras (Online Purchases)'],
          ['Orçamento', 'cada campanha tem o seu'],
          ['Duração', 'para sempre, sem data de fim'],
          ['Nasce', live ? 'rodando, sem campanhas' : 'pausado, sem campanhas'],
        ])),
        h('p', { class: 'faint rv-foot' }, 'Objetivo e orçamento vão com os padrões do Launch.'));
      return;
    }
    if (make === 'campaign') {
      const g = groups.find((x) => x.account === s.account && x.id === s.group);
      const st = campSettings();
      const day = s.start === 'tomorrow' ? 'amanhã, ' + dayMonth(1) : 'hoje, ' + dayMonth(0);
      const budget = (st.daily_cap ? brlMoney(st.daily_cap) + ' por dia' : 'sem orçamento') + (limits.max_spend_limit ? `, até ${money(limits.max_spend_limit)} no total` : ', sem limite');
      const tracking = !st.tracking_code ? 'sem rastreio' : st.tracking_code === TEAM.tracking_code ? 'padrão (src, utm, sub1 a sub10)' : 'próprio';
      const names = campaignNames();
      review.replaceChildren(
        h('div', { class: 'rv-head' }, h('b', {}, `${acctName(s.account) || '—'} › ${g?.name || g?.id || '—'}`), h('span', { class: 'faint' }, plural(names.length, 'campanha nova', 'campanhas novas'))),
        h('div', { class: 'rv-boxes' + (names.length > 1 ? '' : ' one') }, devicesNow().map((d, i) => box('Camp', names[i] || DEVICES[d], [
          ['Dispositivo', d === 'mobile' ? 'Mobile (celular + tablet)' : 'Desktop'],
          ['Começa', day + ', o dia todo'],
          ['Orçamento', budget],
          ['Países', s.countries.map(countryName).join(', ') || '—'],
          ['Marca', st.brand || '—'],
          ['Rastreio', tracking],
        ]))),
        h('p', { class: 'faint rv-foot' }, 'Objetivo, lance e o resto vão com os padrões do Launch.'));
      return;
    }
    // Figma "Launch · Novos anúncios (revisar)": what goes where, then one
    // row per ad. "Tirar" unticks its cell in the matrix; a click on its
    // label turns Taboola's AI label off or on for that ad alone.
    const names = chosenNames();
    const camps = Math.max(1, to.length);
    const fact = (k, v, cls) => h('div', { class: 'rv-fact' }, h('span', { class: 'fr-label' }, k), h('span', { class: cls || '' }, v));
    const label = (a) => {
      const k = cell(a.row.id, a.img.sha256);
      return h('button', { type: 'button', class: 'ai-label' + (a.ai ? '' : ' off'), 'aria-pressed': String(a.ai),
        title: a.ai ? 'Vai com o rótulo de IA do Taboola. Clique para tirar deste anúncio.' : 'Vai sem o rótulo de IA. Clique para pôr neste anúncio.',
        onclick: () => { s.aiChosen.set(k, !a.ai); update(); } }, a.ai ? (a.img.ai ? 'Imagem feita com IA' : 'Feita com IA') : 'Sem rótulo de IA');
    };
    review.replaceChildren(...[
      w.length ? note('warn', h('b', {}, 'Avisos (não impedem): '), w.join(' ')) : null,
      h('div', { class: 'rv-summary' },
        fact('Campanhas', names.length ? names.join('  ·  ') : '—', 'mono'),
        fact('Página de destino', adUrl.value.trim().replace(/^https?:\/\//i, '') || '—'),
        fact('Botão', new Set(list.map((a) => a.cta)).size > 1 ? 'um por anúncio' : ctaLabel(list[0]?.cta ?? s.cta))),
      list.length ? h('div', { class: 'rv-ads' }, h('table', {},
        h('thead', {}, h('tr', {}, ...['Anúncio', 'Imagem', 'Headline', 'Descrição', 'Botão', 'Rótulo', ''].map((x) => h('th', {}, x)))),
        h('tbody', {}, list.map((a) => {
          const hw = headlineWarnings(a.title);
          if (portuguese(a.title)) hw.unshift('Parece português: as headlines vão sempre em inglês.');
          return h('tr', {},
            h('td', { class: 'rv-ad' }, 'AD' + pad(a.n)),
            h('td', { class: 'rv-pic' }, h('span', { class: 'rv-thumb' }, h('img', { src: '/launch/api/images/' + a.img.sha256, alt: 'Imagem ' + letter(a.col) }),
              a.img.ai ? h('span', { class: 'rv-ia', title: 'Imagem marcada como IA na biblioteca, ou que parece feita com IA' }, 'IA') : null)),
            h('td', { class: 'rv-title' }, h('span', {}, a.title), hw.map((x) => h('div', { class: 'warn-line' }, x))),
            h('td', { class: 'rv-desc', title: a.description || null }, a.description || '—'),
            h('td', {}, ctaSelect(a)),
            h('td', {}, label(a)),
            h('td', { class: 'rv-out' }, h('button', { type: 'button', class: 'small', 'aria-label': 'Tirar o AD' + pad(a.n), onclick: () => { s.ticked.delete(cell(a.row.id, a.img.sha256)); changed(); } },
              svgIcon('x-icon', ['M4.5 4.5l7 7', 'M11.5 4.5l-7 7']), 'Tirar')));
        })))) : h('p', { class: 'faint' }, 'Nenhuma combinação marcada.'),
      list.length ? h('p', { class: 'faint rv-foot' }, `${plural(list.length, 'anúncio', 'anúncios')} × ${plural(camps, 'campanha', 'campanhas')} = ${plural(list.length * camps, 'anúncio', 'anúncios')}. `
        + (names.length ? namesLine(names, list.length).replace(/^Nomes: /, 'Nomes ') + '. ' : '') + (live ? 'Nascem rodando.' : 'Nascem pausados.')) : null].filter(Boolean));
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
    if (s.account && make === 'group') parts.push(h('p', { class: 'pv-acct faint' }, acctName(s.account)));
    if (make === 'group') {
      parts.push(row(0, 'Grupo', groupName.value.trim() || next?.group || 'Grupo novo', { fresh: true, state: born }));
      const mine = groups.filter((g) => g.account === s.account).sort(byName);
      parts.push(...mine.slice(0, 8).map((g) => row(0, 'Grupo', g.name || g.id, { state: g.status })), more(0, mine.length - 8));
      parts.push(h('p', { class: 'faint pv-foot' }, live ? 'Nasce rodando no Taboola.' : 'Nasce pausado no Taboola.'));
    }
    if (make === 'campaign') {
      const g = groups.find((x) => x.account === s.account && x.id === s.group);
      if (g) parts.push(row(0, 'Grupo', g.name || g.id, { state: g.status }));
      else parts.push(h('p', { class: 'faint' }, connected.length ? 'Escolha o grupo.' : 'Sem conexão com o Taboola.'));
      if (g) {
        const old = campaignList.filter((c) => c.account === s.account && c.group_id === g.id);
        parts.push(...old.slice(0, 8).map((c) => row(1, 'Camp', c.name)), more(1, old.length - 8));
      }
      if (g) campaignNames().forEach((nm, i) => parts.push(row(1, 'Camp', nm || DEVICES[devicesNow()[i]], { fresh: true })));
      const st = campSettings();
      const where = s.countries.map(countryName).join(', ') || 'nenhum país';
      parts.push(h('p', { class: 'faint pv-foot' }, finished ? `Adicionar anúncios abre Novos anúncios com ${both() ? 'as duas campanhas já escolhidas' : 'a campanha já escolhida'}.` :
        `${both() ? 'Cada uma' : 'Ela'}: ${st.daily_cap ? usd(st.daily_cap) + '/dia' : 'sem orçamento'}, começa ${s.start === 'tomorrow' ? 'amanhã' : 'hoje'}, ${where}. ` +
        (both() ? (live ? 'Nascem rodando no Taboola.' : 'Nascem pausadas no Taboola.') : (live ? 'Nasce rodando no Taboola.' : 'Nasce pausada no Taboola.'))));
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
      if (connected.length && (!s.account || !s.group)) return 'Escolha o grupo.';
      if (!s.countries.length) return 'Escolha ao menos um país.';
      if (!brandIn.value.trim()) return 'Escreva a marca.';
      return set.problem('campaign');
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

  async function sendGroup() {
    const p = problem();
    if (p) {
      sendOut.replaceChildren(note('fail', p));
      return;
    }
    await busy(sendBtn, sendOut, async () => {
      const g = await api(`${s.net}/${encodeURIComponent(s.account)}/groups`, { method: 'POST', body: gForm.get() });
      await forgetDraft();
      const q2 = (more) => new URLSearchParams({ account: s.account, ...more });
      groups.push({ ...g, account: s.account, status: g.status || (live ? 'RUNNING' : 'PAUSED') });
      finish('Grupo criado', `Já está no Taboola${live ? ' e rodando' : ', pausado'}. Agora é só pôr as campanhas.`,
        [h('h2', {}, `${g.name || g.id} em ${acctName(s.account)}`),
          h('div', { class: 'done-row' }, h('span', { class: 'tag' }, 'Grupo'), h('span', { class: 'mono' }, g.name || g.id), h('span', { class: 'mono faint' }, 'id ' + g.id), h('span', { class: 'gap' }), badge(live ? 'RUNNING' : 'PAUSED')),
          h('p', { class: 'faint' }, 'Sem campanhas, ele ainda não aparece para ninguém.')],
        [h('a', { class: 'button', href: '/launch/campaigns?' + q2({ group: g.id }) }, 'Ver na tabela'),
          h('a', { class: 'button primary', href: '/launch/new?' + q2({ make: 'campaign', group: g.id }) }, 'Adicionar campanha')]);
    });
  }

  async function sendAds() {
    const list = await ads();
    const p = problem() || (!list.length ? 'Nenhum anúncio para criar.' : '');
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
      const newAds = list.map((a) => ({ title: a.title, description: a.description, url: url(), image: a.img.sha256, cta: a.cta, ad_id: a.adId, ai: a.ai }));
      const done = [];
      for (const [acct, ids] of byAcct) {
        const res = await api(`${s.net}/${encodeURIComponent(acct)}/add-ads`, { method: 'POST', body: { campaigns: ids, new_ads: newAds } });
        done.push(...res.done);
      }
      const nameOf = (id) => campOf(id)?.name || id;
      const one = byAcct.size === 1 ? [...byAcct.keys()][0] : 'all';
      const fails = done.filter((d) => d.error).map((d) => note('fail', h('b', {}, nameOf(d.campaign) + ': '), d.ads ? `${plural(d.ads, 'anúncio entrou', 'anúncios entraram')}, mas ` : '', d.error));
      const made = done.filter((d) => d.ads > 0);
      if (!made.length) {
        sendOut.replaceChildren(...fails);
        return;
      }
      await forgetDraft();
      const n = made.reduce((t, d) => t + d.ads, 0);
      finish(plural(n, 'anúncio adicionado', 'anúncios adicionados'), `Já estão no Taboola${live ? ', ativos' : ', pausados'}, e passam pela revisão dele.`,
        [h('h2', {}, `${plural(n, 'anúncio', 'anúncios')} em ${plural(made.length, 'campanha', 'campanhas')}`), ...fails,
          ...made.map((d) => h('div', { class: 'done-row' }, h('span', { class: 'tag' }, 'Camp'), h('span', { class: 'mono' }, nameOf(d.campaign)),
            h('span', { class: 'mono faint' }, plural(d.ads, 'anúncio', 'anúncios')), h('span', { class: 'gap' }), badge('PENDING'))),
          h('p', { class: 'faint' }, live ? 'Começam a gastar assim que o Taboola aprovar.' : 'Entram pausados: alguém liga no Taboola.')],
        [h('a', { class: 'button', href: '/launch/campaigns?' + new URLSearchParams({ account: one, ...(to.length === 1 ? { open: to[0] } : {}) }) }, 'Ver na tabela'),
          h('a', { class: 'button primary', href: '/launch/new?' + new URLSearchParams({ make: 'ads', to: to.join(',') }) }, 'Outros anúncios')]);
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
      group_id: s.group,
      settings: campSettings(),
      ads: [], // they come after, in Novos anúncios
    };
    // A typed name names that campaign; empty ones get the team's.
    if (devicesNow().includes('desktop') && typed.desktop) body.desktop_name = typed.desktop;
    if (devicesNow().includes('mobile') && typed.mobile) body.mobile_name = typed.mobile;
    if (s.draftId) body.draft_id = s.draftId;
    // The same content sent twice (a double click, a retry) is one send.
    const key = JSON.stringify(body);
    if (!sendKey || sendKey.body !== key) sendKey = { body: key, id: crypto.randomUUID?.() || String(Math.random()).slice(2) };
    body.key = sendKey.id;
    await busy(sendBtn, sendOut, async () => {
      let job = await api('pairs', { method: 'POST', body });
      while (true) {
        sendOut.replaceChildren(progress(job));
        if (job.done) break;
        await new Promise((r) => setTimeout(r, 1200));
        job = await api('jobs/' + job.id);
      }
      store('launch.brand', body.settings.brand);
      result(job);
    });
  }

  function progress(job) {
    const mark = { wait: '○', run: '✱', ok: '✓', fail: '✕' };
    return h('ol', { class: 'steps' }, (job.steps || []).map((st) => h('li', { class: 'step-' + st.state },
      h('span', { class: 'mark' }, mark[st.state] || '○'), ' ', h('b', {}, st.label), st.detail ? h('span', { class: 'muted' }, ' · ' + st.detail) : null)));
  }

  // result shows what the send made (Figma "Nova campanha · resultado"):
  // the new campaigns, then Ver na tabela and Adicionar anúncios, which opens
  // Novos anúncios with them picked. When nothing was made, the steps stay
  // and say why, so the person can fix it and send again.
  function result(job) {
    const r = job.result || {};
    const made = [r.desktop, r.mobile].filter((m) => m?.campaign?.id);
    if (job.error || !made.length) {
      sendOut.replaceChildren(progress(job), note('fail', h('b', {}, 'Nada foi criado. '), job.error || (r.problems || []).join(' · ')));
      return;
    }
    const g = groups.find((x) => x.account === s.account && x.id === (r.group_id || s.group));
    const gName = g?.name || g?.id || r.group_id || '';
    const ids = made.map((m) => m.campaign.id);
    const bothMade = made.length > 1;
    finish(bothMade ? 'Campanhas criadas' : 'Campanha criada',
      `${bothMade ? 'Já estão' : 'Já está'} no Taboola${live ? ' e rodando' : (bothMade ? ', pausadas' : ', pausada')}. Agora é só pôr os anúncios.`,
      [h('h2', {}, `${plural(made.length, 'campanha', 'campanhas')} em ${gName}`),
        r.result === 'partial' ? note('warn', h('b', {}, 'Criado em parte. '), (r.problems || []).join(' · ')) : null,
        ...made.map((m) => h('div', { class: 'done-row' }, h('span', { class: 'tag' }, 'Camp'),
          h('a', { class: 'mono', href: link(s.net, s.account, r.group_id || '-', m.campaign.id) }, m.campaign.name), h('span', { class: 'mono faint' }, 'id ' + m.campaign.id),
          h('span', { class: 'gap' }), badge(m.campaign.status || (live ? 'RUNNING' : 'PAUSED')))),
        h('p', { class: 'faint' }, bothMade ? 'Sem anúncios, elas ainda não aparecem para ninguém.' : 'Sem anúncios, ela ainda não aparece para ninguém.')].filter(Boolean),
      [h('a', { class: 'button', href: '/launch/campaigns?' + new URLSearchParams({ account: s.account, group: r.group_id || s.group }) }, 'Ver na tabela'),
        h('a', { class: 'button primary', href: '/launch/new?' + new URLSearchParams({ make: 'ads', account: s.account, to: ids.join(',') }) }, 'Adicionar anúncios')]);
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
    s.devices = b.devices || 'both';
    const dv = deviceBox.querySelector(`input[value=${s.devices}]`);
    if (dv) dv.checked = true;
    set.set({ ...(b.settings || {}) });
    if (b.group_fields) {
      gForm.name.value = b.group_fields.name || '';
      nameTyped = !!gForm.name.value;
      gForm.set(b.group_fields);
    }
    s.cols = (b.images || []).filter((x) => x.on !== false);
    if ((b.ctas || []).length) s.cta = b.ctas[0];
    s.rows = (b.headlines || []).filter((x) => x.on !== false && clean(x.text || '')).map((x) => ({ id: newId(), text: clean(x.text), desc: b.ad_desc || '', cta: s.cta }));
    for (const r of s.rows) for (const c of s.cols) s.ticked.add(cell(r.id, c.sha256));
    // Old drafts kept one AI answer for every ad.
    if (b.ai === 'yes' || b.ai === 'no') for (const k of s.ticked) s.aiChosen.set(k, b.ai === 'yes');
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
        { campaigns: ids, url: url(), description: '' });
      const at2 = AD_COLUMNS.indexOf('Description');
      const at3 = AD_COLUMNS.indexOf('AI Content');
      list.forEach((a, k) => { rows[k][at2] = a.description; rows[k][at3] = a.ai ? 'Yes' : 'No'; });
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

// usd is a whole amount without cents (US$ 500), else with them.
function usd(n) {
  return Number.isInteger(n) ? 'US$ ' + n.toLocaleString('pt-BR') : money(n);
}

// brlMoney is US$ with two decimals: US$ 500,00.
function brlMoney(n) {
  return 'US$ ' + brl(n);
}

// dayMonth is today plus days, as dd/mm.
function dayMonth(days) {
  const d = new Date();
  d.setDate(d.getDate() + days);
  return `${pad(d.getDate())}/${pad(d.getMonth() + 1)}`;
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
