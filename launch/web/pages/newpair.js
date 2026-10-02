// Novo: the steps behind "+ Novo" (Draw Designer batch 126, IMPLEMENT
// 43e7b65f44). Each item has its own short page, made one at a time: the
// steps are listed on the left; each step is one card ("Passo N · …") with
// Cancelar and "Próximo: …" under it; on the right, "Prévia na tabela" shows
// the rows Campanhas will show once it is made, the new ones marked "novo".
//
// make=campaign (the default): Campanha (the group, or a new one; the
// devices; each campaign's name, bid and budgets; countries and brand; more
// settings with the tracking code) and Revisar e criar. With "Os dois" one
// desktop and one mobile campaign go in the group; the mobile one may have
// its own name, bid, budgets and start. The campaigns go up without ads:
// "Adicionar anúncios" then opens make=ads with them picked.
// make=group makes a group only. make=ads adds ads to campaigns that exist,
// picked in a tree of every account's groups and campaigns.
//
// The ads come from pictures, headlines (always English) and buttons,
// combined "Sortido" (every picture and headline used, the shorter list
// repeating), "Par a par" (picture 1 with headline 1, 2 with 2, …), pairs
// the person picks, or every combination; then, in the review, any ad can
// get another picture, headline or button, or be taken out, and that list
// is what is sent. Taboola's rules only warn: the person decides. Without a
// connected network, the same ads come out as Taboola's bulk sheet.
import { api, h, note, field, input, select, segmented, busy, plural, money, link, date, store, badge, numberOf, DEVICES } from './lib.js';
import { groupFields, settingsForm, presetBar, loadPresets, BIDS, OBJECTIVES } from './presets.js';
import { mixedN, everyN, seeded, pairwiseN } from '/launch/_ads/pairing.js';
import { manualCombos, pairsFrom, reviewFrom, reviewAds, repeats, mobileSettings, mobileProblem } from './adset.js';
import { nest, standIns } from './rows.js';
import { clean, hasHidden, headlineWarnings, imageWarnings, looksAIMade } from '/launch/_ads/checks.js';
import { CTAS, AD_COLUMNS, MAX_ADS, adId, adRows, uniqueNames, campaignIds } from '/launch/_ads/sheet.js';
import { fillTemplate } from '/launch/_ads/template.js';
import { zip } from '/launch/_ads/zip.js';
import { libraryPicker } from './library.js';

// Portuguese in a headline: accents Portuguese uses and English does not,
// and a few common words. Headlines always go out in English.
const PT = /[ãõçâêô]|\b(você|para|como|seu|sua|não|mais|de|que|com|uma?)\b/i;

export function portuguese(text) {
  return PT.test(text);
}

// combos lists the ads as [image, headline, cta] index triples: every
// combination, "Par a par" (k with k) or "Sortido". The person's own pairs
// are manualCombos (adset.js).
export function combos(nImages, nHeadlines, nCTAs, mode, seed) {
  const sizes = [nImages, nHeadlines, nCTAs];
  if (sizes.some((n) => !n)) return [];
  if (mode === 'every') return everyN(sizes);
  if (mode === 'pairs') return pairwiseN(sizes);
  return mixedN(sizes, seed ? seeded(seed) : null);
}

// MODES are the ways pictures and headlines become ads.
const MODES = [['mixed', 'Sortido'], ['pairs', 'Par a par'], ['manual', 'Escolher pares'], ['every', 'Todas as combinações']];

// MAKES name each kind of new item: the rail's title.
const MAKES = { campaign: 'Nova campanha', group: 'Novo grupo', ads: 'Novos anúncios' };

let hid = 0;
// withId gives a headline the id pairs and edited ads know it by.
function withId(hl) {
  if (!hl.id) hl.id = 'h' + Date.now().toString(36) + '-' + ++hid;
  return hl;
}

// groupValue is a group's value in the Grupo list: its account and id, or
// new:<account> for a new group there.
const groupValue = (account, id) => account + '|' + id;

export async function newPair({ main, status }) {
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
    newGroup: make === 'group',
    draftId: 0,
    images: [], // {sha256, name, type, bytes, width, height, ai, url}
    headlines: [], // {text, on}
    ctas: new Set(['Learn More']),
    mode: 'mixed',
    seed: 0,
    ai: '',
    devices: 'both',
    manual: [], // the person's own pairs: {img: sha256, hl: headline id}
    review: null, // the ads as edited in the review; null: the pairing's
  };
  let next = null; // the account's next names: {group, campaign, desktop, mobile}
  let accounts = [];
  let groups = []; // every account's groups, each with its account
  let campaignList = []; // every account's campaigns, each with its account
  let presetList = [];
  let presetId = null;
  const acctName = (id) => accounts.find((a) => a.id === id)?.name || id;

  const back = '/launch/campaigns?' + new URLSearchParams(Object.entries({ account: s.account, group: s.group }).filter(([, v]) => v));

  // ---- the left rail ----
  const railSteps = h('ol', { class: 'rail-steps' });
  const rail = h('aside', { class: 'steps-rail', 'aria-label': 'Passos' },
    h('div', {}, h('div', { class: 'rail-label' }, MAKES[make]), railSteps));

  // ---- Campanha: group, devices, each campaign ----
  const groupSel = h('select', { 'aria-label': 'Grupo' }, h('option', { value: '' }, connected.length ? 'Carregando os grupos…' : 'Sem conexão: só a planilha'));
  const groupHint = h('span', { class: 'hint' }, 'um grupo que já existe');
  const deviceHint = h('span', { class: 'hint' }, '');
  const deviceBox = segmented('devices', [['both', 'Os dois'], ['desktop', 'Desktop'], ['mobile', 'Mobile']], s.devices, (v) => { s.devices = v; drawDevices(); names(); update(); });
  const gForm = groupFields();
  const set = settingsForm({}, limits);
  const presetHold = h('div', { class: 'preset-row' });
  // The main card is the desktop campaign, or the only one; the mobile card
  // shows with "Os dois", its empty fields the same as the desktop's.
  const mainName = input({ 'aria-label': 'Nome da campanha' });
  const mainLabel = h('span', {}, 'Desktop');
  const mainCard = h('div', { class: 'dev-card' }, h('div', { class: 'dev-head' }, mainLabel),
    field('Nome', mainName), set.parts.bid, set.parts.cpc, set.parts.cpa, h('div', { class: 'two' }, set.parts.cap, set.parts.limit));
  const mob = {
    name: input({ 'aria-label': 'Nome do mobile' }),
    bid_strategy: select([['', 'igual ao desktop'], ...BIDS], '', { 'aria-label': 'Lance do mobile' }),
    cpc: input({ inputmode: 'decimal', 'aria-label': 'CPC do mobile' }),
    target_cpa: input({ inputmode: 'decimal', 'aria-label': 'CPA alvo do mobile' }),
    daily_cap: input({ inputmode: 'decimal', 'aria-label': 'Orçamento diário do mobile' }),
    spending_limit: input({ inputmode: 'decimal', 'aria-label': 'Limite total do mobile' }),
    start_date: input({ type: 'date', 'aria-label': 'Início do mobile' }),
  };
  const mobSame = h('span', { class: 'faint' }, ' · igual ao desktop');
  const mobCPC = field('CPC (US$)', mob.cpc, limits.max_cpc ? 'até ' + money(limits.max_cpc) : null);
  const mobCPA = field('CPA alvo (US$)', mob.target_cpa, 'opcional');
  const mobileCard = h('div', { class: 'dev-card' }, h('div', { class: 'dev-head' }, h('span', {}, 'Mobile'), mobSame),
    field('Nome', mob.name), field('Lance', mob.bid_strategy), mobCPC, mobCPA,
    h('div', { class: 'two' }, field('Orç. diário (US$)', mob.daily_cap), field('Limite total (US$)', mob.spending_limit)));
  const mobStart = field('Mobile começa em', mob.start_date, 'vazio: o mesmo do desktop');
  const groupPresetHold = h('div', { class: 'preset-row' });
  const newGroupCard = h('div', { class: 'dev-card', hidden: true }, h('div', { class: 'dev-head' }, h('span', {}, 'Grupo novo'), h('span', { class: 'faint' }, ' · criado junto, sem data de fim')), groupPresetHold,
    h('div', { class: 'two' }, gForm.parts.name, gForm.parts.objective), h('div', { class: 'two' }, gForm.parts.model, gForm.parts.budget));
  const countries = h('span', {}, '');
  const where = h('details', { class: 'select-like' }, h('summary', {}, countries),
    h('p', { class: 'faint' }, 'Estados Unidos, menos estas cidades (uma por linha, começando pelo número da cidade no Taboola):'), set.parts.cities);
  const more = h('details', { class: 'more' }, h('summary', {}, 'Mais configurações'),
    h('div', { class: 'two' }, set.parts.start, set.parts.end), mobStart, set.parts.delivery, set.parts.objective, set.parts.tracking);
  const campaignCard = h('section', { class: 'form-card' },
    presetHold,
    h('div', { class: 'two' }, h('label', { class: 'field' }, 'Grupo', groupSel, groupHint), h('div', { class: 'field' }, 'Dispositivo', deviceBox, deviceHint)),
    newGroupCard,
    h('div', { class: 'two cards' }, mainCard, mobileCard),
    h('div', { class: 'two' }, h('div', { class: 'field' }, 'Países', where), set.parts.brand),
    more);

  // ---- Grupo (make=group): the account and the group ----
  const accountSel = select([['', connected.length ? 'Carregando contas…' : 'Sem conexão']], '', { 'aria-label': 'Conta' });
  const groupCard = make === 'group' ? h('section', { class: 'form-card' }, groupPresetHold,
    h('div', { class: 'two' }, field('Conta', accountSel), gForm.parts.name),
    h('div', { class: 'two' }, gForm.parts.objective, h('div', { class: 'stack' }, gForm.parts.model, gForm.parts.budget)),
    gForm.parts.duration) : null;

  // ---- Anúncios ----
  const imgGrid = h('div', { class: 'thumbs' });
  const fileIn = h('input', { type: 'file', accept: 'image/jpeg,image/png,image/webp,image/gif', multiple: true, hidden: true, onchange: (e) => { addFiles(e.target.files); e.target.value = ''; } });
  const drop = h('div', { class: 'drop', tabindex: 0, role: 'button', onclick: () => fileIn.click(), onkeydown: (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fileIn.click(); } } },
    h('b', {}, 'Solte as imagens aqui'), ' ou clique para escolher. JPG ou PNG, 1000×600 ou maior.');
  drop.addEventListener('dragover', (e) => { e.preventDefault(); drop.classList.add('over'); });
  drop.addEventListener('dragleave', () => drop.classList.remove('over'));
  drop.addEventListener('drop', (e) => { e.preventDefault(); drop.classList.remove('over'); addFiles(e.dataTransfer.files); });
  const upNote = h('div');
  const lib = libraryPicker(fromLibrary);

  const paste = h('textarea', { rows: 4, placeholder: 'Uma headline por linha, em inglês', spellcheck: 'true' });
  const hlList = h('ol', { class: 'headlines' });
  const addHl = h('button', { type: 'button', onclick: () => {
    const known = new Set(s.headlines.map((x) => clean(x.text).toLowerCase()));
    for (const line of paste.value.split(/\r?\n/)) {
      const t = line.trim();
      if (!t || known.has(clean(t).toLowerCase())) continue;
      known.add(clean(t).toLowerCase());
      s.headlines.push(withId({ text: t, on: true }));
    }
    paste.value = '';
    drawHeadlines();
    update();
  } }, 'Adicionar headlines');

  const ctaBox = h('div', { class: 'chips' });
  const modeBox = segmented('mode', MODES, s.mode, (v) => {
    s.mode = v;
    if (v === 'manual' && !s.manual.length) s.manual = pairsFrom(on().I, on().H);
    update();
  });
  // The person's own pairs: each row ties one headline to one picture.
  const manualBox = h('div', { class: 'manual-pairs' });
  function drawManual() {
    manualBox.hidden = s.mode !== 'manual';
    if (manualBox.hidden) return;
    const { I, H } = on();
    const imgOpts = I.map((x) => [x.sha256, 'I' + (s.images.indexOf(x) + 1) + ' · ' + (x.name || x.sha256.slice(0, 10))]);
    const hlOpts = H.map((x) => [x.id, 'H' + (s.headlines.indexOf(x) + 1) + ' · ' + clean(x.text).slice(0, 70)]);
    manualBox.replaceChildren(
      h('p', { class: 'faint' }, 'Cada linha é um anúncio: esta headline com esta imagem (com cada botão escolhido).'),
      h('ol', { class: 'headlines' }, s.manual.map((p) => {
        const gone = !I.some((x) => x.sha256 === p.img) || !H.some((x) => x.id === p.hl);
        return h('li', { class: gone ? 'off' : '' }, h('div', { class: 'hl-row' },
          h('img', { class: 'mini', src: '/launch/api/images/' + p.img, alt: '' }),
          select(imgOpts, p.img, { 'aria-label': 'Imagem do par', onchange: (e) => { p.img = e.target.value; update(); } }),
          select(hlOpts, p.hl, { 'aria-label': 'Headline do par', onchange: (e) => { p.hl = e.target.value; update(); } }),
          h('button', { type: 'button', class: 'small ghost', onclick: () => { s.manual = s.manual.filter((x) => x !== p); update(); } }, 'Tirar')),
        gone ? h('div', { class: 'warn-line' }, 'A imagem ou a headline deste par não está mais escolhida: ele fica de fora.') : null);
      })),
      h('div', { class: 'actions' },
        h('button', { type: 'button', class: 'small', disabled: !I.length || !H.length, onclick: () => {
          const k = s.manual.length;
          s.manual.push({ img: I[k % I.length].sha256, hl: H[k % H.length].id });
          update();
        } }, '+ Par'),
        h('button', { type: 'button', class: 'small ghost', onclick: () => { s.manual = pairsFrom(I, H); update(); } }, 'Recomeçar par a par')));
  }
  const reshuffle = h('button', { type: 'button', class: 'small ghost', onclick: () => { s.seed = 1 + Math.floor(Math.random() * 2 ** 31); update(); } }, 'Sortear de novo');
  const aiBox = h('div');
  const pairing = h('p', { class: 'muted' });
  const adsCard = h('section', { class: 'form-card' }, h('h3', {}, 'Imagens'), lib.el, drop, fileIn, upNote, imgGrid,
    h('h3', {}, 'Headlines'), h('p', { class: 'faint' }, 'Sempre em inglês. Os avisos são do Taboola e não impedem o envio.'), paste, h('div', { class: 'actions' }, addHl), hlList,
    h('h3', {}, 'Botão'), ctaBox,
    h('h3', {}, 'Combinação'), h('div', { class: 'actions' }, modeBox, reshuffle), manualBox, pairing,
    h('h3', {}, 'Feito com IA?'), aiBox);
  const adsStep = h('div', {}, adsCard);

  // ---- Campanhas (make=ads): the campaigns that get the ads ----
  const to = (q.get('to') || '').split(',').map((x) => x.trim()).filter((x) => /^\d+$/.test(x));
  const adUrl = input({ type: 'url', placeholder: 'https://…', 'aria-label': 'Página dos anúncios' });
  const adDesc = input({ placeholder: 'opcional, em inglês', 'aria-label': 'Descrição' });
  const url = () => adUrl.value.trim();
  const description = () => adDesc.value.trim();
  const findCamp = input({ type: 'search', placeholder: 'nome ou id', 'aria-label': 'Buscar campanha' });
  const targetList = h('div', { class: 'table-wrap pick-tree' });
  const closed = new Set(); // groups closed in the tree, by account/id
  let treeDrawn = false;
  const targetsCard = make === 'ads' ? h('section', { class: 'form-card' }, findCamp, targetList,
    h('div', { class: 'two' }, field('Página (landing page)', adUrl, 'a mesma em todos os anúncios'), field('Descrição', adDesc))) : null;
  findCamp.addEventListener('input', () => drawTargets());
  // drawTargets is every account's groups with their campaigns; a group's
  // box picks all its campaigns. Big lists start closed, but the groups
  // with a picked campaign.
  function drawTargets() {
    if (make !== 'ads') return;
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
      update();
    };
    if (!rows.length) {
      targetList.replaceChildren(h('p', { class: 'faint' }, !connected.length ? 'Sem conexão: diga os ids das campanhas na planilha, no último passo.' : campaignList.length ? 'Nenhuma campanha com essa busca.' : 'Carregando as campanhas…'));
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

  // ---- Revisar ----
  const review = h('div');
  const sendOut = h('div');
  const sendBtn = h('button', { type: 'button', class: 'primary big', onclick: () => (make === 'group' ? sendGroup(false) : sendPair()) },
    make === 'group' ? 'Criar grupo' : make === 'ads' ? (live ? 'Adicionar ativos' : 'Adicionar pausados') : live ? 'Criar e ligar' : 'Criar pausado');
  const sheetIds = input({ placeholder: '123456, 123457', 'aria-label': 'Ids das campanhas para a planilha' });
  const sheetOut = h('div');
  const sheetBtn = h('button', { type: 'button', onclick: () => downloadSheet() }, 'Baixar planilha e imagens');
  // Realize's "Create & add campaign": the group, then straight into a campaign in it.
  const andCampaign = h('button', { type: 'button', class: 'big', onclick: () => sendGroup(true) }, 'Criar e adicionar campanha');
  const reviewCard = h('section', { class: 'form-card' }, review,
    h('div', { class: 'actions' }, connected.length ? sendBtn : null, make === 'group' && connected.length ? andCampaign : null), sendOut,
    make !== 'ads' ? null : h('details', { class: 'sheet', open: !connected.length }, h('summary', {}, 'Subir à mão pelo Bulk Upload'),
      h('p', { class: 'muted' }, 'A planilha usa as campanhas que já existem no Taboola. Os anúncios entram pausados.'),
      h('div', { class: 'fields' }, field('Ids das campanhas', sheetIds, 'cada anúncio vai em todas')),
      h('div', { class: 'actions' }, sheetBtn), sheetOut));

  // ---- the steps ----
  const adsSub = () => (s.review || on().I.length ? plural(adCount, 'anúncio', 'anúncios') : 'imagens, headlines, botão');
  let adCount = 0;
  const steps = {
    campaign: [
      { label: 'Campanha', short: 'campanha', el: campaignCard, sub: () => ({ both: 'grupo e par desktop + mobile', desktop: 'grupo e campanha desktop', mobile: 'grupo e campanha mobile' })[s.devices],
        lead: 'Com "Os dois" saem uma campanha desktop e uma mobile, iguais ou com lance e orçamento próprios no mobile.' },
      { label: 'Revisar e criar', short: 'revisar', el: reviewCard, sub: () => '',
        lead: 'Confira na prévia ao lado. As campanhas ' + (live ? 'sobem ativas' : 'nascem pausadas') + ', ainda sem anúncios.' },
    ],
    group: [
      { label: 'Grupo', short: 'grupo', el: groupCard, sub: () => 'objetivo e orçamento',
        lead: 'O grupo nasce sem campanhas. As campanhas de um grupo têm o mesmo objetivo e podem dividir o orçamento dele.' },
      { label: 'Revisar e criar', short: 'revisar', el: reviewCard, sub: () => '', lead: 'Confira na prévia ao lado. Depois de criar, você pode pôr uma campanha nele.' },
    ],
    ads: [
      { label: 'Campanhas', short: 'campanhas', el: targetsCard, sub: () => (to.length ? plural(to.length, 'escolhida', 'escolhidas') : 'onde os anúncios entram'),
        lead: `Os mesmos anúncios, ${live ? 'ativos' : 'pausados'}, em cada campanha escolhida. Eles passam pela revisão do Taboola.` },
      { label: 'Anúncios', short: 'anúncios', el: adsStep, sub: adsSub, lead: 'Imagens, headlines e botão. As headlines vão sempre em inglês.' },
      { label: 'Revisar e adicionar', short: 'revisar', el: reviewCard, sub: () => '', lead: 'Confira os anúncios como vão sair, em cada campanha escolhida.' },
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
  function show(i) {
    at = Math.max(0, Math.min(i, steps.length - 1));
    steps.forEach((st, k) => { st.el.hidden = k !== at; });
    title.textContent = `Passo ${at + 1} · ${steps[at].label}`;
    lead.textContent = steps[at].lead;
    stepOut.replaceChildren();
    backBtn.hidden = at === 0;
    nextBtn.hidden = at === steps.length - 1;
    if (!nextBtn.hidden) nextBtn.textContent = 'Próximo: ' + steps[at + 1].short;
    drawRail();
    window.scrollTo?.({ top: 0 });
  }
  const offline = connected.length ? null : note('warn', h('b', {}, 'Taboola desligado. '), make === 'ads' ? 'Monte os anúncios aqui e baixe a planilha no fim para subir pelo Bulk Upload do Taboola.' : 'Criar precisa do Taboola ligado. A planilha do Bulk Upload fica em Novos anúncios.');
  main.append(h('div', { class: 'steps-page' }, rail,
    h('div', { class: 'steps-content' },
      h('div', { class: 'steps-form' }, h('div', { class: 'steps-head' }, title, lead), offline, ...steps.map((st) => st.el), stepOut,
        h('div', { class: 'steps-next' }, backBtn, cancel, nextBtn)),
      h('aside', { class: 'steps-preview', 'aria-label': 'Prévia na tabela' }, h('h2', {}, 'Prévia na tabela'), preview))));

  // ---- loading ----
  // Every account's groups and campaigns: the Grupo list (Nova campanha),
  // the tree (Novos anúncios) and the preview read them.
  async function load() {
    if (!connected.length) {
      drawGroupSel();
      drawTargets();
      return;
    }
    try {
      accounts = (await api(`accounts/${s.net}`)).accounts;
    } catch (e) {
      stepOut.replaceChildren(note('fail', e.message));
      return;
    }
    if (make === 'group' && !accounts.some((a) => a.id === s.account)) {
      const kept = store('launch.acct');
      s.account = (kept !== 'all' && accounts.some((a) => a.id === kept) && kept) || accounts[0]?.id || '';
    }
    accountSel.replaceChildren(...accounts.map((a) => h('option', { value: a.id, selected: a.id === s.account }, a.name || a.id)));
    const trees = await Promise.all(accounts.map((a) => api(`${s.net}/${encodeURIComponent(a.id)}/tree`).then((t) => ({ a, t }), (e) => ({ a, error: e.message }))));
    groups = [];
    campaignList = [];
    const problems = [];
    for (const { a, t, error } of trees) {
      if (error) { problems.push(`${a.name || a.id}: ${error}`); continue; }
      for (const g of t.groups) groups.push({ ...g, account: a.id });
      for (const c of t.campaigns) campaignList.push({ ...c, account: a.id });
    }
    if (problems.length) stepOut.replaceChildren(note('warn', h('b', {}, 'Não consegui ler: '), problems.join(' · ')));
    // A campaign named in the address belongs to its account.
    if (make === 'ads' && !s.account && to.length) s.account = campaignList.find((c) => c.id === to[0])?.account || '';
    drawGroupSel();
    drawTargets();
    await accountChanged();
  }
  // accountChanged reads what depends on the account: its next names and
  // its presets.
  async function accountChanged() {
    names();
    if (!s.account || make === 'ads') {
      update();
      return;
    }
    const acct = s.account;
    const [n, p] = await Promise.all([
      api(`${s.net}/${encodeURIComponent(acct)}/next`).catch(() => null), // the names are worked out again when sent
      loadPresets(s.net, acct).catch(() => []),
    ]);
    if (acct !== s.account) return;
    next = n;
    presetList = p;
    presetHold.replaceChildren(presetBar({ level: 'campaign', net: s.net, account: acct, form: set, list: presetList, onUse: (x) => { presetId = x.id; update(); } }));
    groupPresetHold.replaceChildren(presetBar({ level: 'group', net: s.net, account: acct, form: gForm, list: presetList, onUse: () => update() }));
    names();
    update();
  }
  accountSel.addEventListener('change', () => { s.account = accountSel.value; accountChanged(); });

  // drawGroupSel lists each account's groups, and a new group in each.
  function drawGroupSel() {
    if (make !== 'campaign') return;
    if (!connected.length) return;
    const real = (a) => groups.filter((g) => g.account === a.id);
    const chosen = s.newGroup ? 'new:' + s.account : s.group ? groupValue(s.account, s.group) : '';
    groupSel.replaceChildren(h('option', { value: '' }, accounts.length ? 'Escolha o grupo…' : 'Nenhuma conta'),
      ...accounts.map((a) => h('optgroup', { label: a.name || a.id },
        ...real(a).map((g) => h('option', { value: groupValue(a.id, g.id) }, g.name || g.id)),
        h('option', { value: 'new:' + a.id }, '+ Grupo novo em ' + (a.name || a.id)))));
    groupSel.value = chosen;
    if (groupSel.value !== chosen) { s.group = ''; s.newGroup = false; groupSel.value = ''; }
    drawGroupPart();
  }
  function drawGroupPart() {
    newGroupCard.hidden = !s.newGroup;
    groupHint.textContent = s.newGroup ? 'um grupo novo, criado junto com as campanhas' : 'um grupo que já existe';
    // The campaigns take their group's objective (Maximize conversions needs one of conversions).
    const g = groups.find((x) => x.account === s.account && x.id === s.group);
    const objective = s.newGroup ? gForm.get().objective : g?.objective;
    if (objective && OBJECTIVES.some(([v]) => v === objective)) set.set({ settings: { objective } });
  }
  groupSel.addEventListener('change', () => {
    const v = groupSel.value;
    const was = s.account;
    if (v.startsWith('new:')) {
      s.newGroup = true;
      s.account = v.slice(4);
      s.group = '';
    } else {
      s.newGroup = false;
      [s.account, s.group] = v ? v.split('|') : [s.account, ''];
    }
    drawGroupPart();
    if (s.account !== was) accountChanged();
  });
  gForm.parts.objective.addEventListener('change', () => drawGroupPart());

  // ---- the campaigns: names, devices, the mobile's own values ----
  const mainDevice = () => (s.devices === 'mobile' ? 'mobile' : 'desktop');
  // names shows the team's next names as placeholders: typing one names that campaign.
  function names() {
    mainName.placeholder = next ? next[mainDevice()] : 'o nome do time';
    mob.name.placeholder = next ? next.mobile : 'o nome do time';
    const gHint = gForm.parts.name.querySelector('.hint');
    if (gHint) gHint.textContent = next ? `vazio: o próximo número (${next.group})` : 'vazio: o próximo número da conta';
  }
  // campaignNames is each new campaign's name: the typed one or the team's.
  function campaignNames() {
    const own = { [mainDevice()]: mainName.value.trim(), ...(s.devices === 'both' ? { mobile: mob.name.value.trim() } : {}) };
    return devicesNow().map((d) => own[d] || (next ? next[d] : ''));
  }
  const devicesNow = () => (s.devices === 'both' ? ['desktop', 'mobile'] : [s.devices]);
  function drawDevices() {
    mainLabel.textContent = DEVICES[mainDevice()];
    mobileCard.hidden = s.devices !== 'both';
    mobStart.hidden = s.devices !== 'both';
    deviceHint.textContent = { both: 'os dois: sai um par desktop + mobile', desktop: 'só computador', mobile: 'celular e tablet' }[s.devices];
  }
  const own = (v) => (String(v ?? '').trim() ? numberOf(v) : '');
  // mobileOver is the mobile campaign's own values; null when it has none.
  function mobileOver() {
    if (s.devices !== 'both') return null;
    const o = { bid_strategy: mob.bid_strategy.value, cpc: own(mob.cpc.value), target_cpa: own(mob.target_cpa.value), daily_cap: own(mob.daily_cap.value),
      spending_limit: own(mob.spending_limit.value), start_date: mob.start_date.value };
    return Object.values(o).some((v) => v !== '') ? o : null;
  }
  // deviceSettings is each campaign's settings: {desktop, mobile}.
  function deviceSettings() {
    const d = set.settings();
    return { desktop: d, mobile: mobileSettings(d, mobileOver()) || d };
  }
  // drawMobile shows the desktop's values in the mobile's empty fields, and
  // the bid field its bid kind needs.
  function drawMobile() {
    const d = set.settings();
    const m = deviceSettings().mobile;
    const same = (v) => (v ? 'igual: ' + String(v).replace('.', ',') : 'igual ao desktop');
    mob.bid_strategy.options[0].textContent = 'igual ao desktop (' + (BIDS.find(([v]) => v === d.bid_strategy)?.[1] || '') + ')';
    mob.cpc.placeholder = same(d.cpc);
    mob.target_cpa.placeholder = same(d.target_cpa);
    mob.daily_cap.placeholder = same(d.daily_cap);
    mob.spending_limit.placeholder = d.spending_limit || limits.max_spend_limit ? same(d.spending_limit || limits.max_spend_limit) : 'igual: nenhum';
    const maxConv = m.bid_strategy === 'MAX_CONVERSIONS';
    mobCPC.hidden = maxConv;
    mobCPA.hidden = !maxConv;
    mobSame.textContent = mobileOver() ? ' · com valores próprios' : ' · igual ao desktop';
    const n = d.exclude_cities.length;
    countries.textContent = 'Estados Unidos' + (n ? `, menos ${plural(n, 'cidade', 'cidades')}` : '');
  }

  // ---- images ----
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
        if (s.images.some((x) => x.sha256 === info.sha256)) continue;
        s.images.push({ ...info, ai: looksAIMade(bytes), on: true });
      } catch (e) {
        problems.push(`${f.name}: ${e.message}`);
      }
    }
    upNote.replaceChildren(...problems.map((p) => note('fail', p)));
    if (!s.ai && s.images.some((x) => x.ai)) s.ai = 'yes';
    drawImages();
    update();
  }
  // fromLibrary brings a library set's chosen creatives and headlines in.
  // A creative's AI label is the one saved with it; an unlabelled one is
  // looked at like an upload.
  async function fromLibrary(creatives, lines) {
    const problems = [];
    let nI = 0;
    let nH = 0;
    for (const c of creatives) {
      try {
        const r = await api('library/use?id=' + encodeURIComponent(c.id), { method: 'POST' });
        if (s.images.some((x) => x.sha256 === r.image.sha256)) continue;
        let ai = c.ai_label === 'ai';
        if (c.ai_label !== 'ai' && c.ai_label !== 'not_ai') {
          const res = await fetch('/launch/api/images/' + r.image.sha256).catch(() => null);
          if (res?.ok) ai = looksAIMade(new Uint8Array(await res.arrayBuffer()));
        }
        s.images.push({ ...r.image, ai, on: true, library: c.id });
        nI++;
      } catch (e) {
        problems.push(`${c.name || 'criativo ' + c.id}: ${e.message}`);
      }
    }
    const known = new Set(s.headlines.map((x) => clean(x.text).toLowerCase()));
    for (const x of lines) {
      const t = clean(x.text || '');
      if (!t || known.has(t.toLowerCase())) continue;
      known.add(t.toLowerCase());
      s.headlines.push(withId({ text: t, on: true, library: x.id, ai: x.ai_label === 'ai' }));
      nH++;
    }
    if (!s.ai && (s.images.some((x) => x.on && x.ai) || s.headlines.some((x) => x.on && x.ai))) s.ai = 'yes';
    upNote.replaceChildren(...problems.map((p) => note('fail', p)));
    drawImages();
    drawHeadlines();
    update();
    return { images: nI, headlines: nH, problems: problems.length };
  }

  function drawImages() {
    imgGrid.replaceChildren(...s.images.map((img, i) => {
      const warn = imageWarnings({ width: img.width, height: img.height, size: img.bytes, type: img.type });
      return h('figure', { class: 'thumb' + (img.on ? ' chosen' : '') },
        h('img', { src: '/launch/api/images/' + img.sha256, alt: '', loading: 'lazy' }),
        h('figcaption', {},
          h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: img.on, onchange: (e) => { img.on = e.target.checked; drawImages(); update(); } }), 'I' + (i + 1) + ' · ' + img.name),
          h('div', { class: 'faint' }, `${img.width}×${img.height}`, img.ai ? ' · parece feita com IA' : ''),
          warn.map((w) => h('div', { class: 'warn-line' }, w)),
          h('button', { type: 'button', class: 'small ghost', onclick: () => { s.images = s.images.filter((x) => x !== img); drawImages(); update(); } }, 'Tirar')));
    }));
  }

  // ---- headlines ----
  function drawHeadlines() {
    hlList.replaceChildren(...s.headlines.map((hl) => {
      const warn = headlineWarnings(hl.text);
      if (portuguese(hl.text)) warn.unshift('Parece português: as headlines vão sempre em inglês.');
      const text = h('input', { type: 'text', value: hl.text, 'aria-label': 'Headline', oninput: (e) => { hl.text = e.target.value.replace(/[\r\n]+/g, ' '); update(); }, onchange: () => drawHeadlines() });
      return h('li', { class: hl.on ? '' : 'off' },
        h('div', { class: 'hl-row' }, h('input', { type: 'checkbox', checked: hl.on, 'aria-label': 'Usar esta headline', onchange: (e) => { hl.on = e.target.checked; drawHeadlines(); update(); } }), text,
          hasHidden(hl.text) ? h('button', { type: 'button', class: 'small', onclick: () => { hl.text = clean(hl.text); drawHeadlines(); update(); } }, 'Tirar invisíveis') : null,
          h('button', { type: 'button', class: 'small ghost', onclick: () => { s.headlines = s.headlines.filter((x) => x !== hl); drawHeadlines(); update(); } }, 'Tirar')),
        h('div', { class: 'faint' }, clean(hl.text).length + ' caracteres'),
        warn.map((w) => h('div', { class: 'warn-line' }, w)));
    }));
  }

  // ---- CTAs ----
  function drawCTAs() {
    ctaBox.replaceChildren(...CTAS.map((c) => h('label', { class: 'chip' },
      h('input', { type: 'checkbox', checked: s.ctas.has(c), onchange: (e) => { e.target.checked ? s.ctas.add(c) : s.ctas.delete(c); update(); } }),
      h('span', {}, c || 'Sem botão'))));
  }

  // ---- AI label ----
  function drawAI() {
    const looks = s.images.filter((x) => x.on && x.ai).length;
    aiBox.replaceChildren(...[
      segmented('ai', [['yes', 'Sim, marcar como IA'], ['no', 'Não']], s.ai, (v) => { s.ai = v; drawAI(); update(); }),
      s.ai === 'no' && looks ? note('warn', `${plural(looks, 'imagem parece feita', 'imagens parecem feitas')} com IA. O Taboola pede que imagens de IA sejam declaradas; a escolha é sua.`) : null,
      !s.ai ? h('p', { class: 'faint' }, 'Escolha antes de criar. O Taboola pede que imagens e headlines de saúde feitas com IA sejam declaradas.') : null].filter(Boolean));
  }

  // ---- the ads ----
  const on = () => ({ I: s.images.filter((x) => x.on), H: s.headlines.filter((x) => x.on && clean(x.text)), T: CTAS.filter((c) => s.ctas.has(c)) });

  // generated is the ads the pairing makes, before any edit in the review.
  // i and h are the picture's and headline's places in the whole lists.
  function generated() {
    const { I, H, T } = on();
    const list = s.mode === 'manual' ? manualCombos(s.manual, I, H, T.length) : combos(I.length, H.length, T.length, s.mode, s.seed);
    return list.map(([i, hh, t]) => ({ img: I[i], i: s.images.indexOf(I[i]), h: s.headlines.indexOf(H[hh]), hl: H[hh].id, title: clean(H[hh].text), cta: T[t] }));
  }

  // ads builds every ad the page would send: the review's edited list when
  // the person changed it there, else the pairing's.
  async function ads() {
    const list = s.review ? reviewAds(s.review, s.images, s.headlines) : generated();
    const manyCTAs = s.review ? new Set(list.map((a) => a.cta)).size > 1 : on().T.length > 1;
    return Promise.all(list.map(async (a) => {
      const title = clean(a.title);
      return { ...a, title, adId: await adId(a.img.sha256.slice(0, 10), title, manyCTAs ? a.cta : '') };
    }));
  }


  let drawn = 0;
  async function update() {
    const run = ++drawn;
    reshuffle.hidden = s.mode !== 'mixed';
    drawManual();
    if (make === 'campaign') drawMobile();
    const { I, H, T } = on();
    const list = await ads();
    if (run !== drawn) return;
    adCount = list.length;
    const made = generated();
    if (!I.length || !H.length || !T.length) {
      pairing.textContent = 'Escolha ao menos uma imagem, uma headline e um botão.';
    } else {
      const uI = I.map((x) => made.filter((a) => a.img === x).length);
      const uH = H.map((x) => made.filter((a) => a.hl === x.id).length);
      const range = (a) => (Math.min(...a) === Math.max(...a) ? times(a[0]) : `${Math.min(...a)} a ${times(Math.max(...a))}`);
      pairing.textContent = `${plural(I.length, 'imagem', 'imagens')} × ${plural(H.length, 'headline', 'headlines')} × ${plural(T.length, 'botão', 'botões')} → ${plural(made.length, 'anúncio', 'anúncios')} em cada campanha. Cada imagem ${range(uI)}, cada headline ${range(uH)}.` +
        (s.review ? ` A revisão foi editada: vão os ${plural(list.length, 'anúncio', 'anúncios')} de lá até você usar "Refazer pela combinação".` : '');
    }
    drawAI();
    drawReview(list);
    drawRail();
  }
  const times = (n) => (n === 1 ? '1 vez' : `${n} vezes`);

  function warnings(list) {
    const w = [];
    const { I, H } = on();
    const titles = s.review ? [...new Set(list.map((a) => a.title))] : H.map((x) => x.text);
    const badHl = titles.filter((t) => headlineWarnings(t).length || portuguese(t)).length;
    const badImg = I.filter((x) => imageWarnings({ width: x.width, height: x.height, size: x.bytes, type: x.type }).length).length;
    if (badHl) w.push(`${plural(badHl, 'headline tem', 'headlines têm')} aviso do Taboola.`);
    if (badImg) w.push(`${plural(badImg, 'imagem tem', 'imagens têm')} aviso de tamanho ou formato.`);
    if (list.length > MAX_ADS) w.push(`${list.length} anúncios passam de ${MAX_ADS}, o máximo de uma planilha.`);
    const twice = repeats(list);
    if (twice) w.push(`${plural(twice, 'anúncio repete', 'anúncios repetem')} outro (mesma imagem, headline e botão).`);
    if (s.ai === 'no' && I.some((x) => x.ai)) w.push('Marcado como sem IA, mas há imagens que parecem de IA.');
    return w;
  }

  function drawReview(list) {
    const w = warnings(list);
    drawPreview(list, w);
    if (make === 'group') {
      review.replaceChildren(h('p', { class: 'muted' }, live ? 'O grupo sobe ativo, ainda sem campanhas.' : 'O grupo nasce pausado, ainda sem campanhas.'));
      return;
    }
    if (make === 'campaign') {
      review.replaceChildren(h('p', { class: 'muted' }, 'Os anúncios entram depois: ao criar, "Adicionar anúncios" abre Novos anúncios com ' +
        (s.devices === 'both' ? 'as duas campanhas já escolhidas.' : 'a campanha já escolhida.')));
      return;
    }
    // Each ad can get another picture, headline (from the list or typed) or
    // button, or be taken out. The first edit freezes the list; the edited
    // list is exactly what is sent.
    const edit = (k, change) => {
      if (!s.review) s.review = reviewFrom(list);
      change(s.review[k]);
      update();
    };
    const imgOpts = s.images.map((x, n) => [x.sha256, 'I' + (n + 1) + ' · ' + (x.name || x.sha256.slice(0, 10))]);
    const hls = s.headlines.filter((x) => clean(x.text));
    const ctaOpts = CTAS.map((c) => [c, c || 'sem botão']);
    review.replaceChildren(...[
      w.length ? note('warn', h('b', {}, 'Avisos (não impedem): '), w.join(' ')) : null,
      s.review ? note('', h('b', {}, 'Anúncios editados. '), 'Vai exatamente esta lista. ',
        h('button', { type: 'button', class: 'small ghost', onclick: () => { s.review = null; update(); } }, 'Refazer pela combinação')) :
        list.length ? h('p', { class: 'faint' }, 'Troque a imagem, a headline ou o botão de qualquer anúncio, ou tire um, antes de ' + (make === 'ads' ? 'adicionar.' : 'criar.')) : null,
      list.length ? h('div', { class: 'table-wrap ads-preview' }, h('table', { class: 'list' },
        h('thead', {}, h('tr', {}, h('th', {}, 'Imagem'), h('th', {}, 'Headline'), h('th', {}, 'Botão'), h('th', {}, ''))),
        h('tbody', {}, list.map((a, k) => {
          const known = hls.find((x) => x.id === a.hl && clean(x.text) === a.title);
          const hw = headlineWarnings(a.title);
          if (portuguese(a.title)) hw.unshift('Parece português: as headlines vão sempre em inglês.');
          const hlOpts = [...(known ? [] : [['', 'texto próprio']]), ...hls.map((x) => [x.id, 'H' + (s.headlines.indexOf(x) + 1) + ' · ' + clean(x.text).slice(0, 50)])];
          const imgs = imgOpts.some(([v]) => v === a.img.sha256) ? imgOpts : [[a.img.sha256, 'imagem tirada da lista'], ...imgOpts];
          return h('tr', {},
            h('td', { class: 'ad-pic' }, h('img', { class: 'mini', src: '/launch/api/images/' + a.img.sha256, alt: a.i >= 0 ? 'I' + (a.i + 1) : '' }),
              select(imgs, a.img.sha256, { 'aria-label': 'Imagem do anúncio ' + (k + 1), onchange: (e) => edit(k, (r) => { r.img = e.target.value; }) })),
            h('td', { class: 'ad-title' },
              select(hlOpts, known ? known.id : '', { 'aria-label': 'Headline do anúncio ' + (k + 1), onchange: (e) => edit(k, (r) => {
                const x = hls.find((y) => y.id === e.target.value);
                if (x) { r.hl = x.id; r.title = clean(x.text); }
              }) }),
              h('input', { type: 'text', value: a.title, 'aria-label': 'Texto da headline do anúncio ' + (k + 1), onchange: (e) => edit(k, (r) => {
                r.title = e.target.value.replace(/[\r\n]+/g, ' ');
                r.hl = hls.find((y) => clean(y.text) === clean(r.title))?.id ?? null;
              }) }),
              hw.map((x) => h('div', { class: 'warn-line' }, x)),
              h('div', { class: 'mono faint ad-id', title: 'Id do anúncio' }, a.adId)),
            h('td', {}, select(ctaOpts, a.cta, { 'aria-label': 'Botão do anúncio ' + (k + 1), onchange: (e) => edit(k, (r) => { r.cta = e.target.value; }) })),
            h('td', {}, h('button', { type: 'button', class: 'small ghost', 'aria-label': 'Tirar o anúncio ' + (k + 1), onclick: () => {
              if (!s.review) s.review = reviewFrom(list);
              s.review.splice(k, 1);
              update();
            } }, 'Tirar')));
        })))) : h('p', { class: 'faint' }, s.review ? 'Todos os anúncios foram tirados.' : 'Nenhum anúncio ainda.')].filter(Boolean));
  }

  // drawPreview is the right column: the rows Campanhas will show, the new
  // ones marked "novo", under the group they go in.
  function drawPreview(list, w) {
    const row = (level, tag, name, o = {}) => h('div', { class: `pv-tr level-${level}` + (o.fresh ? ' fresh' : '') },
      h('span', { class: 'tag' }, tag), h('span', { class: 'pv-name', title: name }, name), o.fresh ? h('span', { class: 'new-tag' }, 'novo') : null,
      h('span', { class: 'pv-gap' }), o.state ? badge(o.state) : null);
    const sub = (level, text) => h('p', { class: `pv-sub level-${level} faint` }, text);
    const more = (level, n) => (n > 0 ? sub(level, `…e mais ${n}.`) : null);
    const adsLine = list.length ? plural(list.length, 'anúncio novo', 'anúncios novos') : 'novos anúncios aqui';
    const parts = [];
    if (s.account && make !== 'ads') parts.push(h('p', { class: 'pv-acct faint' }, 'Conta ' + acctName(s.account)));
    if (make === 'group') {
      const gf = gForm.get();
      parts.push(row(0, 'Grupo', gf.name || (next ? next.group : 'Grupo novo'), { fresh: true, state: live ? 'RUNNING' : 'PAUSED' }),
        sub(1, [OBJECTIVES.find(([v]) => v === gf.objective)?.[1], gf.budget_model ? money(gf.budget) + { MONTHLY: ' por mês', ENTIRE: ' no total' }[gf.budget_model] : 'orçamento por campanha', 'para sempre'].filter(Boolean).join(' · ')));
      const mine = [...groups.filter((g) => g.account === s.account), ...standIns(groups.filter((g) => g.account === s.account), campaignList.filter((c) => c.account === s.account))];
      parts.push(...mine.slice(0, 8).map((g) => row(0, 'Grupo', g.name || g.id, { state: g.status })), more(0, mine.length - 8));
    }
    if (make === 'campaign') {
      const g = groups.find((x) => x.account === s.account && x.id === s.group);
      if (s.newGroup) parts.push(row(0, 'Grupo', gForm.get().name || (next ? next.group : 'Grupo novo'), { fresh: true, state: live ? 'RUNNING' : 'PAUSED' }));
      else if (g) parts.push(row(0, 'Grupo', g.name || g.id, { state: g.status }));
      else parts.push(h('p', { class: 'faint' }, connected.length ? 'Escolha o grupo.' : 'Sem conexão com o Taboola.'));
      if (g) {
        const old = campaignList.filter((c) => c.account === s.account && c.group_id === g.id);
        parts.push(...old.slice(0, 5).map((c) => row(1, 'Camp', c.name)), more(1, old.length - 5));
      }
      const nm = campaignNames();
      const per = deviceSettings();
      devicesNow().forEach((d, i) => {
        const ds = s.devices === 'both' ? per[d] : per.desktop;
        const bid = ds.bid_strategy === 'MAX_CONVERSIONS' ? 'Maximizar conversões' + (ds.target_cpa ? `, CPA ${money(ds.target_cpa)}` : '') : ds.cpc ? `CPC ${money(ds.cpc)}` : 'lance —';
        parts.push(row(1, 'Camp', nm[i] || DEVICES[d], { fresh: true }),
          sub(2, [DEVICES[d], bid, ds.daily_cap ? money(ds.daily_cap) + ' por dia' : 'sem orçamento',
            (ds.spending_limit || limits.max_spend_limit) ? 'até ' + money(ds.spending_limit || limits.max_spend_limit) + ' no total' : 'sem limite total',
            ds.start_date ? 'começa em ' + ds.start_date : ''].filter(Boolean).join(' · ')));
      });
    }
    if (make === 'ads') {
      const byId = new Map(campaignList.map((c) => [c.id, c]));
      const chosen = to.map((id) => byId.get(id) || { id, name: id, account: s.account, group_id: '' });
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
          parts.push(row(1, 'Camp', c.name), row(2, 'Ad', adsLine, { fresh: true }));
        }
      }
      for (const c of loose) if (shown++ < 8) parts.push(row(1, 'Camp', c.name), row(2, 'Ad', adsLine, { fresh: true }));
      parts.push(more(0, to.length - 8));
    }
    if (make === 'ads' && list.length && at > 0) {
      parts.push(h('div', { class: 'pv-cards' }, list.slice(0, 4).map((a) => h('figure', { class: 'pv-card' },
        h('img', { src: '/launch/api/images/' + a.img.sha256, alt: '', loading: 'lazy' }),
        h('figcaption', {}, h('b', {}, a.title), a.cta ? h('span', { class: 'pv-cta' }, a.cta) : null)))),
      more(0, list.length - 4));
    }
    if (w.length) parts.push(note('warn', h('b', {}, 'Avisos: '), w.join(' ')));
    parts.push(h('p', { class: 'faint pv-foot' }, live ? 'Tudo sobe ativo: começa a gastar assim que o Taboola aprovar.' : 'Tudo nasce pausado: alguém liga no Taboola.'));
    preview.replaceChildren(...parts.filter(Boolean));
  }

  // ---- what keeps a step from going on ----
  function stepProblem(st) {
    const el = st.el;
    if (el === campaignCard) {
      if (connected.length) {
        if (!s.account) return 'Escolha o grupo.';
        if (s.newGroup) {
          const p = gForm.problem();
          if (p) return p;
        } else if (!s.group) return 'Escolha o grupo, ou crie um novo.';
      }
      if (!set.settings().brand) return 'Escreva a marca.';
      return set.problem('campaign') || (s.devices === 'both' ? mobileProblem(mobileSettings(set.settings(), mobileOver()), limits) : '');
    }
    if (el === groupCard) {
      if (connected.length && !s.account) return 'Escolha a conta.';
      return gForm.problem();
    }
    if (el === targetsCard) {
      if (connected.length && !to.length) return 'Escolha ao menos uma campanha.';
      if (!url()) return 'Falta o link da página.';
    }
    if (el === adsStep) {
      if (!on().I.length || !on().H.length || !on().T.length) return 'Escolha ao menos uma imagem, uma headline e um botão.';
      if (s.mode === 'manual' && !s.review && !generated().length) return 'Monte ao menos um par.';
      if (!s.ai) return 'Diga se os anúncios foram feitos com IA.';
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
  const forget = () => (s.draftId ? api('drafts/' + s.draftId, { method: 'DELETE' }).catch(() => {}) : null);

  async function sendGroup(addCampaign) {
    const p = problem();
    if (p) {
      sendOut.replaceChildren(note('fail', p));
      return;
    }
    await busy(sendBtn, sendOut, async () => {
      const g = await api(`${s.net}/${encodeURIComponent(s.account)}/groups`, { method: 'POST', body: gForm.get() });
      await forget();
      const q2 = (more) => new URLSearchParams({ account: s.account, ...more });
      sendBtn.hidden = true;
      andCampaign.hidden = true;
      if (addCampaign) {
        location.assign('/launch/new?' + q2({ make: 'campaign', group: g.id }));
        return;
      }
      sendOut.replaceChildren(note('ok', h('b', {}, `Grupo ${g.name || g.id} criado${live ? ', ativo' : ', pausado'}. `), 'Ainda sem campanhas.'),
        h('div', { class: 'actions' }, h('a', { class: 'button primary', href: '/launch/new?' + q2({ make: 'campaign', group: g.id }) }, 'Criar uma campanha nele'),
          h('a', { class: 'button ghost', href: '/launch/campaigns?' + q2({ group: g.id }) }, 'Ver em Campanhas')));
    });
  }

  let sendKey = '';
  async function sendPair() {
    const list = await ads();
    const p = problem();
    if (p) {
      sendOut.replaceChildren(note('fail', p));
      return;
    }
    if (make === 'ads') {
      if (!list.length) {
        sendOut.replaceChildren(note('fail', 'Nenhum anúncio para criar.'));
        return;
      }
      await busy(sendBtn, sendOut, async () => {
        // The ads go to each campaign's own account.
        const byAcct = new Map();
        for (const id of to) {
          const acct = campaignList.find((c) => c.id === id)?.account || s.account;
          if (!byAcct.has(acct)) byAcct.set(acct, []);
          byAcct.get(acct).push(id);
        }
        const done = [];
        for (const [acct, ids] of byAcct) {
          const res = await api(`${s.net}/${encodeURIComponent(acct)}/add-ads`, { method: 'POST', body: { campaigns: ids,
            new_ads: list.map((a) => ({ title: a.title, description: description(), url: url(), image: a.img.sha256, cta: a.cta, ad_id: a.adId, ai: s.ai === 'yes' })) } });
          done.push(...res.done);
        }
        if (done.some((d) => !d.error)) await forget();
        sendBtn.hidden = true;
        const nameOf = (id) => campaignList.find((c) => c.id === id)?.name || id;
        const one = byAcct.size === 1 ? [...byAcct.keys()][0] : 'all';
        sendOut.replaceChildren(...done.map((d) => (d.error ? note('fail', h('b', {}, nameOf(d.campaign) + ': '), d.error) : note('ok', h('b', {}, nameOf(d.campaign) + ': '), (d.ads === 1 ? '1 anúncio adicionado' + (live ? ', ativo' : ', pausado') : `${d.ads} anúncios adicionados` + (live ? ', ativos' : ', pausados'))))),
          h('p', {}, h('a', { href: '/launch/campaigns?' + new URLSearchParams({ account: one, ...(to.length === 1 ? { open: to[0] } : {}) }) }, 'Ver em Campanhas')));
      });
      return;
    }
    const body = {
      network: s.net,
      account: s.account,
      name: '',
      devices: s.devices,
      group_id: s.newGroup ? '' : s.group,
      settings: set.settings(),
      ads: [], // they come after, in Novos anúncios
    };
    // A typed name names that campaign; empty ones get the team's.
    const typed = { [mainDevice()]: mainName.value.trim(), ...(s.devices === 'both' ? { mobile: mob.name.value.trim() } : {}) };
    if (typed.desktop) body.desktop_name = typed.desktop;
    if (typed.mobile) body.mobile_name = typed.mobile;
    // The mobile campaign's own bid, budgets or start, when asked.
    const mobile = s.devices === 'both' ? mobileSettings(body.settings, mobileOver()) : null;
    if (mobile) body.mobile = mobile;
    if (s.newGroup) body.new_group = gForm.get();
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
      return h('div', {}, note('ok', h('b', {}, (pair ? 'Par criado' : 'Campanha criada') + (live ? (pair ? ', ativo' : ', ativa') : (pair ? ', pausado' : ', pausada')) + ', ainda sem anúncios. '),
        live ? 'Começa a gastar quando os anúncios entrarem e o Taboola aprovar.' : 'Ponha os anúncios e ligue no Taboola quando quiser que comece.'),
        h('p', {}, ...[camp(r.desktop), pair ? ' · ' : null, camp(r.mobile)].filter(Boolean)),
        h('div', { class: 'actions' }, addAds, h('a', { class: 'button', href: '/launch/campaigns?' + new URLSearchParams({ account: s.account, group: r.group_id || '' }) }, 'Ver em Campanhas'),
          h('a', { class: 'button ghost', href: '/launch/new?' + new URLSearchParams({ make: 'campaign', account: s.account, group: r.group_id || '' }) }, 'Outra campanha neste grupo')));
    }
    return h('div', {}, note(r.result === 'partial' ? 'warn' : 'fail', h('b', {}, r.result === 'partial' ? 'Criado em parte. ' : 'Nada foi criado. '), (r.problems || []).join(' · ')),
      h('p', {}, camp(r.desktop), r.desktop && r.mobile ? ' · ' : '', camp(r.mobile)),
      h('p', { class: 'faint' }, (live ? 'O que foi criado está ativo' : 'O que foi criado está pausado') + ' e aparece no Histórico.'),
      addAds ? h('div', { class: 'actions' }, addAds) : null);
  }

  // ---- drafts ----
  // Drafts are no longer saved here (IMPLEMENT 43e7b65f44 took out "Salvar
  // rascunho"); the ones already in Rascunhos still open, and go away once
  // made.
  // openDraft fills the page from a draft. Drafts from before the steps
  // (a pair's one name, the mobile's values behind "on") still open.
  function openDraft(d) {
    const b = d.body || {};
    s.draftId = d.id;
    s.net = b.net || s.net;
    s.account = b.account || s.account;
    s.group = b.group || '';
    s.newGroup = make === 'group' || !!b.newGroup;
    s.images = b.images || [];
    s.headlines = (b.headlines || []).map(withId);
    s.manual = b.manual || [];
    s.review = b.review || null;
    s.ctas = new Set(b.ctas || ['Learn More']);
    s.mode = b.mode || 'mixed';
    s.seed = b.seed || 0;
    s.ai = b.ai || '';
    s.devices = b.devices || 'both';
    const dv = deviceBox.querySelector(`input[value=${s.devices}]`);
    if (dv) dv.checked = true;
    presetId = b.preset_id || null;
    if (b.names) {
      mainName.value = b.names.main || '';
      mob.name.value = b.names.mobile || '';
    } else if (b.name) {
      mainName.value = `${b.name} · ${DEVICES[s.devices === 'mobile' ? 'mobile' : 'desktop']}`;
      mob.name.value = `${b.name} · Mobile`;
    }
    const m = b.mobile || {};
    if (m.on !== false) for (const [k, el] of Object.entries(mob)) if (k !== 'name' && m[k] !== undefined && m[k] !== null) el.value = m[k];
    set.set({ ...(b.settings || {}), settings: { ...(b.settings?.settings || {}), start_date: b.start || '', end_date: b.end || '' } });
    if (b.group_fields) {
      gForm.name.value = b.group_fields.name || '';
      gForm.set(b.group_fields);
    }
    to.splice(0, to.length, ...(b.to || to));
    adUrl.value = b.ad_url || '';
    adDesc.value = b.ad_desc || '';
    const r = modeBox.querySelector(`input[value=${s.mode}]`);
    if (r) r.checked = true;
    rail.querySelector('.rail-label').textContent = MAKES[make] + ' · rascunho';
  }

  // ---- the bulk sheet ----
  async function downloadSheet() {
    await busy(sheetBtn, sheetOut, async () => {
      const list = await ads();
      const ids = campaignIds(sheetIds.value);
      if (!ids.length) throw new Error('Diga os ids das campanhas que recebem os anúncios.');
      if (!list.length) throw new Error('Escolha ao menos uma imagem, uma headline e um botão.');
      const used = [...new Set(list.map((a) => a.img))];
      const names = uniqueNames(used.map((x) => x.name || 'imagem.jpg'));
      const fileOf = new Map(used.map((x, i) => [x, names[i]]));
      const T = on().T;
      const rows = adRows(list.map((a, k) => ({
        creativeFile: fileOf.get(a.img), title: a.title, cta: a.cta, customId: a.adId,
        adName: `${fileOf.get(a.img).replace(/\.[^.]+$/, '')} - ${a.h >= 0 ? 'H' + (a.h + 1) : 'E' + (k + 1)}${T.length > 1 ? ' - ' + (a.cta || 'sem botão') : ''}`,
      })), { campaigns: ids, url: url(), description: description(), ai: { yes: 'Yes', no: 'No' }[s.ai] || '' });
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
  drawImages();
  drawHeadlines();
  drawCTAs();
  drawDevices();
  drawGroupPart();
  names();
  // Create sends people here with the set they just saved: /launch/new?set=7
  // opens Novos anúncios with it.
  if (/^\d+$/.test(q.get('set') || '')) lib.open(q.get('set'), true);
  for (const st of steps) {
    st.el.addEventListener('input', () => update());
    st.el.addEventListener('change', () => update());
  }
  show(0);
  update();
  await load();
  show(at);
}

function save(blob, name) {
  const a = h('a', { href: URL.createObjectURL(blob), download: name });
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 10000);
}
