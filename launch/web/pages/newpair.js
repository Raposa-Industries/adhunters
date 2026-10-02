// Novo: the steps behind the "Novo ▾" button, on the left, with a preview
// of everything that will be made on the right, like Realize's "+ New".
// make=group makes a group only; make=campaign (the default) a campaign, or
// one desktop and one mobile campaign with the same settings and the same
// ads, paused in one group (the mobile one may have its own bid, daily
// budget and start); make=ads adds ads to campaigns that exist. The ads come
// from pictures, headlines (always English) and buttons, combined "Sortido"
// (every picture and headline used, the shorter list repeating), "Par a par"
// (picture 1 with headline 1, 2 with 2, …), pairs the person picks, or
// every combination; then, in the review, any ad can get another picture,
// headline or button, or be taken out, and that list is what is sent.
// Taboola's rules only warn: the person decides. Without a connected
// network, the same ads come out as Taboola's bulk sheet.
import { api, h, note, field, input, select, segmented, busy, plural, money, link, date, store, badge, numberOf, DEVICES } from './lib.js';
import { groupFields, settingsForm, presetBar, loadPresets, OBJECTIVES } from './presets.js';
import { mixedN, everyN, seeded, pairwiseN } from '/launch/_ads/pairing.js';
import { manualCombos, pairsFrom, reviewFrom, reviewAds, repeats, mobileSettings, mobileProblem } from './adset.js';
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

let hid = 0;
// withId gives a headline the id pairs and edited ads know it by.
function withId(hl) {
  if (!hl.id) hl.id = 'h' + Date.now().toString(36) + '-' + ++hid;
  return hl;
}

export async function newPair({ main, status }) {
  const q = new URLSearchParams(location.search);
  const connected = (status.networks || []).filter((n) => n.connected);
  // An address with campaigns (to=) is ads only, as before the menu.
  const make = ['group', 'campaign', 'ads'].includes(q.get('make')) ? q.get('make') : q.get('to') ? 'ads' : 'campaign';
  const s = {
    net: q.get('net') || connected[0]?.name || 'taboola',
    account: q.get('account') || '',
    group: q.get('group') || '',
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
    mobile: { on: false, cpc: '', target_cpa: '', daily_cap: '', start_date: '' }, // the mobile campaign's own values
  };
  let next = null; // the account's next names: {group, campaign, desktop, mobile}
  let accounts = [];
  let groups = [];
  let presetList = [];
  let campaignList = []; // the account's campaigns, for ads only

  const TITLES = {
    group: ['Novo grupo de campanha', 'O grupo nasce sem campanhas. As campanhas de um grupo têm o mesmo objetivo e podem dividir o orçamento dele.'],
    campaign: ['Nova campanha', 'Grupo, campanha e anúncios, como no Taboola. Com "Os dois" saem uma campanha desktop e uma mobile (celular e tablet), iguais ou com lance, orçamento e início próprios no mobile. Tudo nasce pausado.'],
    ads: ['Novos anúncios', 'Os mesmos anúncios, pausados, em cada campanha escolhida. Eles passam pela revisão do Taboola.'],
  };
  const back = '/launch/' + (make === 'group' ? 'groups' : make === 'ads' ? 'ads' : 'campaigns') + '?' +
    new URLSearchParams(Object.entries({ account: s.account, group: s.group }).filter(([, v]) => v));
  const head = h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, TITLES[make][0]), h('p', { class: 'lead muted' }, TITLES[make][1])),
    h('div', { class: 'actions' }, h('a', { class: 'button ghost', href: back }, 'Cancelar')));
  main.append(head);
  if (!connected.length) {
    main.append(note('warn', h('b', {}, 'Taboola desligado. '), 'Monte os anúncios aqui e baixe a planilha no fim para subir pelo Bulk Upload do Taboola.'));
  }

  // ---- 1. where ----
  const accountSel = select([['', 'Carregando contas…']], '', { 'aria-label': 'Conta' });
  const groupSel = select([['', '—']], '', { 'aria-label': 'Grupo' });
  const pairName = input({ placeholder: 'vazio: o nome do time', 'aria-label': 'Nome próprio' });
  const nameHint = h('span', { class: 'hint' }, '');
  const deviceBox = segmented('devices', [['both', 'Os dois'], ['mobile', 'Mobile'], ['desktop', 'Desktop']], s.devices, (v) => { s.devices = v; names(); update(); });
  const gForm = groupFields();
  let newGroup = false;
  const gBox = h('div', { class: 'new-group', hidden: true });
  const groupMode = segmented('group-mode', [['old', 'Grupo que já existe'], ['new', 'Grupo novo']], 'old', (v) => {
    newGroup = v === 'new';
    groupSel.closest('label').hidden = newGroup;
    gBox.hidden = !newGroup;
    update();
  });
  const where = h('section', { class: 'panel step' }, h('h2', {}, 'Grupo'),
    h('div', { class: 'fields' }, field('Conta', accountSel)),
    make === 'group' ? null : h('div', { class: 'group-pick' }, groupMode, field('Grupo', groupSel)), gBox);
  if (make === 'group') {
    newGroup = true;
    gBox.hidden = false;
  }
  const campaignHead = h('div', {},
    h('span', { class: 'field' }, 'Dispositivo'), deviceBox, h('p', { class: 'faint' }, 'Mobile é celular e tablet; desktop, só computador.'),
    h('div', { class: 'fields' }, h('label', { class: 'field' }, 'Nome próprio (opcional)', pairName, nameHint)));
  // names shows the campaigns' names: the team's (CMP<n>-<conta>-<Mobile|Desktop>-pp-bl) or the typed one.
  function names() {
    const own = pairName.value.trim();
    const list = campaignNames();
    nameHint.textContent = own ? list.join('  e  ') : next ? 'o nome do time: ' + list.join('  e  ') : '';
    const gHint = gForm.name.closest('label')?.querySelector('.hint');
    if (gHint && next) gHint.textContent = 'vazio: ' + next.group;
  }
  function campaignNames() {
    const own = pairName.value.trim();
    const both = s.devices === 'both' ? ['desktop', 'mobile'] : [s.devices];
    return both.map((d) => (own ? `${own} · ${d === 'desktop' ? 'Desktop' : 'Mobile'}` : next ? next[d] : ''));
  }
  pairName.addEventListener('input', names);

  // ---- 2. settings ----
  const set = settingsForm({}, status.limits || {});
  const presetHold = h('div');
  // With both devices the mobile campaign may have its own bid, daily
  // budget and start; empty fields keep the desktop's.
  const mobileBox = h('div', { class: 'mobile-own', hidden: make !== 'campaign' });
  const settingsPanel = h('section', { class: 'panel step' }, h('h2', {}, 'Campanha'),
    campaignHead,
    h('p', { class: 'muted' }, 'Começa com o padrão do time. Use um preset ou salve estas como um.'), presetHold, set.el, mobileBox);
  let presetId = null;
  const mobileIn = {
    cpc: input({ inputmode: 'decimal', 'aria-label': 'CPC do mobile' }),
    target_cpa: input({ inputmode: 'decimal', 'aria-label': 'CPA alvo do mobile' }),
    daily_cap: input({ inputmode: 'decimal', 'aria-label': 'Orçamento diário do mobile' }),
    start_date: input({ type: 'date', 'aria-label': 'Início do mobile' }),
  };
  for (const [k, el] of Object.entries(mobileIn)) el.addEventListener('input', () => { s.mobile[k] = el.value; });
  const mobileOn = h('input', { type: 'checkbox', onchange: (e) => { s.mobile.on = e.target.checked; update(); } });
  const mobileFields = h('div', { class: 'fields' });
  mobileBox.append(h('h3', {}, 'Mobile'),
    h('label', { class: 'check' }, mobileOn, 'Mobile com valores próprios (lance, orçamento diário, início)'),
    h('p', { class: 'faint' }, 'Sem marcar, as duas campanhas saem iguais. Campo vazio: o mesmo do desktop.'), mobileFields);
  // mobileOver is the mobile campaign's own values, as numbers and dates.
  const mobileOver = () => (s.devices === 'both' && s.mobile.on ? {
    cpc: String(s.mobile.cpc || '').trim() ? numberOf(s.mobile.cpc) : '',
    target_cpa: String(s.mobile.target_cpa || '').trim() ? numberOf(s.mobile.target_cpa) : '',
    daily_cap: String(s.mobile.daily_cap || '').trim() ? numberOf(s.mobile.daily_cap) : '',
    start_date: s.mobile.start_date || '',
  } : null);
  // deviceSettings is each campaign's settings: {desktop, mobile}.
  const deviceSettings = () => {
    const d = set.settings();
    return { desktop: d, mobile: mobileSettings(d, mobileOver()) || d };
  };
  // drawMobile shows the mobile's own fields under the settings, with the
  // desktop's values as placeholders. Only the bid kind the desktop uses.
  function drawMobile() {
    mobileBox.hidden = s.devices !== 'both';
    for (const [k, el] of Object.entries(mobileIn)) if (document.activeElement !== el) el.value = s.mobile[k] || '';
    const d = set.settings();
    const lim = status.limits || {};
    const same = (v) => (v ? 'igual: ' + String(v).replace('.', ',') : 'igual ao desktop');
    mobileIn.cpc.placeholder = same(d.cpc);
    mobileIn.target_cpa.placeholder = same(d.target_cpa);
    mobileIn.daily_cap.placeholder = same(d.daily_cap);
    mobileFields.hidden = !s.mobile.on;
    // Only redrawn when the bid kind changes (fields() moves the inputs into
    // new labels), so typing keeps its place.
    const kind = d.bid_strategy === 'MAX_CONVERSIONS' ? 'cpa' : 'cpc';
    if (mobileFields.dataset.kind !== kind) {
      mobileFields.dataset.kind = kind;
      mobileFields.replaceChildren(
        kind === 'cpa' ? field('CPA alvo do mobile (US$)', mobileIn.target_cpa, 'opcional') : field('CPC do mobile (US$)', mobileIn.cpc, lim.max_cpc ? 'até ' + money(lim.max_cpc) : null),
        field('Orçamento diário do mobile (US$)', mobileIn.daily_cap, lim.max_daily_cap ? 'até ' + money(lim.max_daily_cap) : null),
        field('Mobile começa em', mobileIn.start_date, 'vazio: o mesmo do desktop'));
    }
  }

  // ---- 3. ads ----
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
  const adsPanel = h('section', { class: 'panel step' }, h('h2', {}, 'Anúncios'),
    lib.el, h('h3', {}, 'Imagens'), drop, fileIn, upNote, imgGrid,
    h('h3', {}, 'Headlines'), h('p', { class: 'faint' }, 'Sempre em inglês. Os avisos são do Taboola e não impedem o envio.'), paste, h('div', { class: 'actions' }, addHl), hlList,
    h('h3', {}, 'Botão'), ctaBox,
    h('h3', {}, 'Combinação'), h('div', { class: 'actions' }, modeBox, reshuffle), manualBox, pairing,
    h('h3', {}, 'Feito com IA?'), aiBox);

  // ---- 4. review and send ----
  const review = h('div');
  const sendOut = h('div');
  const sendBtn = h('button', { type: 'button', class: 'primary big', onclick: () => (make === 'group' ? sendGroup(false) : sendPair()) }, make === 'group' ? 'Criar grupo' : 'Criar pausado');
  const draftBtn = h('button', { type: 'button', class: 'ghost', onclick: () => saveDraft() }, 'Salvar rascunho');
  const draftOut = h('span', { class: 'faint' });
  const sheetIds = input({ placeholder: '123456, 123457', 'aria-label': 'Ids das campanhas para a planilha' });
  const sheetOut = h('div');
  const sheetBtn = h('button', { type: 'button', onclick: () => downloadSheet() }, 'Baixar planilha e imagens');
  // Realize's "Create & add campaign": the group, then straight into a campaign in it.
  const andCampaign = h('button', { type: 'button', class: 'big', onclick: () => sendGroup(true) }, 'Criar e adicionar campanha');
  const sendPanel = h('section', { class: 'panel step' }, h('h2', {}, 'Revisar e criar'), review,
    h('div', { class: 'actions' }, connected.length ? sendBtn : null, make === 'group' && connected.length ? andCampaign : null, make === 'group' ? null : draftBtn, draftOut), sendOut,
    make === 'group' ? null : h('details', { class: 'sheet', open: !connected.length }, h('summary', {}, 'Subir à mão pelo Bulk Upload'),
      h('p', { class: 'muted' }, 'A planilha usa as campanhas que já existem no Taboola. Os anúncios entram pausados.'),
      h('div', { class: 'fields' }, field('Ids das campanhas', sheetIds, 'cada anúncio vai em todas')),
      h('div', { class: 'actions' }, sheetBtn), sheetOut));

  // Ads only (Realize's "assign creatives"): /launch/new?make=ads&account=…&to=<campaign ids>
  // adds the ads, paused, to campaigns that exist, picked here or before.
  const to = (q.get('to') || '').split(',').map((x) => x.trim()).filter((x) => /^\d+$/.test(x));
  const adUrl = input({ type: 'url', placeholder: 'https://…', 'aria-label': 'Página dos anúncios' });
  const adDesc = input({ placeholder: 'opcional, em inglês', 'aria-label': 'Descrição' });
  const url = () => (make === 'ads' ? adUrl.value.trim() : set.url());
  const description = () => (make === 'ads' ? adDesc.value.trim() : set.description());
  const findCamp = input({ type: 'search', placeholder: 'nome ou id', 'aria-label': 'Buscar campanha' });
  const targetList = h('div', { class: 'target-list' });
  const targets = h('section', { class: 'panel step' }, h('h2', {}, 'Campanhas'),
    h('div', { class: 'fields' }, make === 'ads' ? field('Conta', accountSel) : null, field('Buscar', findCamp)), targetList,
    h('div', { class: 'fields' }, field('Página (landing page)', adUrl, 'a mesma em todos os anúncios'), field('Descrição', adDesc)));
  findCamp.addEventListener('input', () => drawTargets());
  function drawTargets() {
    const f = findCamp.value.trim().toLowerCase();
    const list = campaignList.filter((c) => to.includes(c.id) || !f || c.name.toLowerCase().includes(f) || c.id.includes(f));
    const byGroup = new Map(groups.map((g) => [g.id, g.name || g.id]));
    targetList.replaceChildren(list.length ? h('ul', { class: 'targets' }, list.slice(0, 200).map((c) => h('li', {},
      h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: to.includes(c.id), onchange: (e) => {
        if (e.target.checked) to.push(c.id);
        else to.splice(to.indexOf(c.id), 1);
        update();
      } }), h('span', {}, c.name), ' ', h('span', { class: 'faint mono' }, (byGroup.get(c.group_id) || 'sem grupo') + ' · ' + c.id), ' ', badge(c.status))))) :
      h('p', { class: 'faint' }, campaignList.length ? 'Nenhuma campanha com essa busca.' : 'Carregando as campanhas…'));
  }

  // ---- the steps on the left, the preview on the right ----
  const steps = {
    group: [['Grupo', where], ['Revisar e criar', sendPanel]],
    campaign: [['Grupo', where], ['Campanha', settingsPanel], ['Anúncios', adsPanel], ['Revisar e criar', sendPanel]],
    ads: [['Campanhas', targets], ['Anúncios', adsPanel], ['Revisar e adicionar', sendPanel]],
  }[make];
  if (make === 'ads') {
    sendBtn.textContent = 'Adicionar pausados';
    sendPanel.querySelector('h2').textContent = 'Revisar e adicionar';
  }
  const nav = h('ol', { class: 'wiz-nav' });
  const stepOut = h('div');
  const backBtn = h('button', { type: 'button', class: 'ghost', onclick: () => show(at - 1) }, 'Voltar');
  const nextBtn = h('button', { type: 'button', class: 'primary', onclick: () => {
    const p = stepProblem(steps[at][1]);
    if (p) { stepOut.replaceChildren(note('fail', p)); return; }
    show(at + 1);
  } }, 'Próximo');
  const preview = h('div', { class: 'wiz-preview-body' });
  let at = 0;
  function show(i) {
    at = Math.max(0, Math.min(i, steps.length - 1));
    steps.forEach(([label, el], k) => {
      el.hidden = k !== at;
      el.querySelector('h2').textContent = `${k + 1} · ${label}`;
    });
    stepOut.replaceChildren();
    backBtn.hidden = at === 0;
    nextBtn.hidden = at === steps.length - 1;
    nav.replaceChildren(...steps.map(([label, el], k) => h('li', { class: k === at ? 'on' : k < at && !stepProblem(el) ? 'done' : '' },
      h('button', { type: 'button', 'aria-current': k === at ? 'step' : null, onclick: () => show(k) }, h('span', { class: 'n' }, k < at && !stepProblem(el) ? '✓' : String(k + 1)), label))));
    window.scrollTo?.({ top: 0 });
  }
  main.append(h('div', { class: 'wizard' },
    h('div', { class: 'wiz-main' }, nav, ...steps.map(([, el]) => el), stepOut, h('div', { class: 'actions wiz-move' }, backBtn, nextBtn)),
    h('aside', { class: 'wiz-preview panel', 'aria-label': 'Prévia' }, h('h2', {}, 'Prévia'), preview)));

  // ---- loading ----
  async function loadAccounts() {
    if (!connected.length) {
      accountSel.replaceChildren(h('option', { value: '' }, 'Sem conexão'));
      return;
    }
    try {
      accounts = (await api(`accounts/${s.net}`)).accounts;
    } catch (e) {
      (make === 'ads' ? targets : where).append(note('fail', e.message));
      return;
    }
    if (!s.account) s.account = (store('launch.acct') !== 'all' && store('launch.acct')) || accounts[0]?.id || '';
    accountSel.replaceChildren(...accounts.map((a) => h('option', { value: a.id, selected: a.id === s.account }, a.name || a.id)));
    s.account = accountSel.value;
    await loadGroups();
  }
  async function loadNext() {
    next = null;
    try {
      next = await api(`${s.net}/${encodeURIComponent(s.account)}/next`);
    } catch {
      // The names are worked out again when sent.
    }
    names();
    update();
  }
  async function loadGroups() {
    groupSel.replaceChildren(h('option', { value: '' }, 'Carregando…'));
    loadNext();
    try {
      const t = await api(`${s.net}/${encodeURIComponent(s.account)}/tree`);
      groups = t.groups;
      campaignList = t.campaigns;
    } catch (e) {
      groups = [];
      campaignList = [];
      (make === 'ads' ? targets : where).append(note('fail', e.message));
    }
    if (make === 'ads') drawTargets();
    groupSel.replaceChildren(h('option', { value: '' }, groups.length ? 'Escolha o grupo…' : 'Nenhum grupo: crie um novo'),
      ...groups.map((g) => h('option', { value: g.id, selected: g.id === s.group }, g.name || g.id)));
    presetList = await loadPresets(s.net, s.account).catch(() => []);
    presetHold.replaceChildren(presetBar({ level: 'campaign', net: s.net, account: s.account, form: set, list: presetList, onUse: (p) => { presetId = p.id; } }));
    const gp = gBox.querySelector('.preset-bar');
    gp?.remove();
    gBox.replaceChildren(presetBar({ level: 'group', net: s.net, account: s.account, form: gForm, list: presetList }), gForm.el);
  }
  accountSel.addEventListener('change', () => { s.account = accountSel.value; s.group = ''; to.splice(0); loadGroups(); update(); });
  groupSel.addEventListener('change', () => { s.group = groupSel.value; update(); });

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
      review.replaceChildren(h('p', { class: 'muted' }, 'Confira na prévia ao lado. Depois de criar, você pode pôr uma campanha nele.'));
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
        list.length ? h('p', { class: 'faint' }, 'Troque a imagem, a headline ou o botão de qualquer anúncio, ou tire um, antes de criar.') : null,
      list.length ? h('div', { class: 'table-wrap ads-preview' }, h('table', { class: 'list' },
        h('thead', {}, h('tr', {}, h('th', {}, ''), h('th', {}, 'Imagem'), h('th', {}, 'Headline'), h('th', {}, 'Botão'), h('th', {}, 'Id do anúncio'), h('th', {}, ''))),
        h('tbody', {}, list.map((a, k) => {
          const known = hls.find((x) => x.id === a.hl && clean(x.text) === a.title);
          const hw = headlineWarnings(a.title);
          if (portuguese(a.title)) hw.unshift('Parece português: as headlines vão sempre em inglês.');
          const hlOpts = [...(known ? [] : [['', 'texto próprio']]), ...hls.map((x) => [x.id, 'H' + (s.headlines.indexOf(x) + 1) + ' · ' + clean(x.text).slice(0, 50)])];
          const imgs = imgOpts.some(([v]) => v === a.img.sha256) ? imgOpts : [[a.img.sha256, 'imagem tirada da lista'], ...imgOpts];
          return h('tr', {},
            h('td', {}, h('img', { class: 'mini', src: '/launch/api/images/' + a.img.sha256, alt: a.i >= 0 ? 'I' + (a.i + 1) : '' })),
            h('td', {}, select(imgs, a.img.sha256, { 'aria-label': 'Imagem do anúncio ' + (k + 1), onchange: (e) => edit(k, (r) => { r.img = e.target.value; }) })),
            h('td', {},
              select(hlOpts, known ? known.id : '', { 'aria-label': 'Headline do anúncio ' + (k + 1), onchange: (e) => edit(k, (r) => {
                const x = hls.find((y) => y.id === e.target.value);
                if (x) { r.hl = x.id; r.title = clean(x.text); }
              }) }),
              h('input', { type: 'text', value: a.title, 'aria-label': 'Texto da headline do anúncio ' + (k + 1), onchange: (e) => edit(k, (r) => {
                r.title = e.target.value.replace(/[\r\n]+/g, ' ');
                r.hl = hls.find((y) => clean(y.text) === clean(r.title))?.id ?? null;
              }) }),
              hw.map((x) => h('div', { class: 'warn-line' }, x))),
            h('td', {}, select(ctaOpts, a.cta, { 'aria-label': 'Botão do anúncio ' + (k + 1), onchange: (e) => edit(k, (r) => { r.cta = e.target.value; }) })),
            h('td', { class: 'mono faint' }, a.adId),
            h('td', {}, h('button', { type: 'button', class: 'small ghost', 'aria-label': 'Tirar o anúncio ' + (k + 1), onclick: () => {
              if (!s.review) s.review = reviewFrom(list);
              s.review.splice(k, 1);
              update();
            } }, 'Tirar')));
        })))) : h('p', { class: 'faint' }, s.review ? 'Todos os anúncios foram tirados.' : 'Nenhum anúncio ainda.')].filter(Boolean));
  }

  // drawPreview is the right column: the whole thing as it will be made.
  function drawPreview(list, w) {
    const acct = accounts.find((a) => a.id === s.account);
    const st = set.settings();
    const row = (label, ...value) => h('div', { class: 'pv-row' }, h('span', { class: 'fr-label' }, label), h('div', {}, ...value));
    const parts = [row('Conta', acct ? acct.name || acct.id : s.account || '—')];
    if (make !== 'ads') {
      const gf = gForm.get();
      const objective = (OBJECTIVES.find(([v]) => v === (newGroup ? gf.objective : st.objective)) || [])[1];
      const gName = newGroup ? gf.name || (next ? next.group : 'próximo número') : groups.find((x) => x.id === groupSel.value)?.name || '';
      parts.push(h('div', { class: 'pv-node pv-group' },
        row('Grupo', gName ? h('b', {}, gName) : h('span', { class: 'faint' }, 'escolha o grupo'), newGroup ? h('span', { class: 'badge' }, 'novo') : null),
        newGroup ? h('p', { class: 'faint' }, (objective || '') + ' · ' + (gf.budget_model ? money(gf.budget) + { MONTHLY: ' por mês', ENTIRE: ' no total' }[gf.budget_model] : 'orçamento por campanha') + ' · para sempre, sem data de fim') : null));
    }
    if (make === 'campaign') {
      const devices = s.devices === 'both' ? ['desktop', 'mobile'] : [s.devices];
      const names = campaignNames();
      const per = deviceSettings();
      parts.push(...devices.map((d, i) => {
        const ds = s.devices === 'both' ? per[d] : st;
        const bid = ds.bid_strategy === 'MAX_CONVERSIONS' ? 'Maximizar conversões' + (ds.target_cpa ? `, CPA alvo ${money(ds.target_cpa)}` : '') : ds.cpc ? `CPC ${money(ds.cpc)}` : 'lance —';
        return h('div', { class: 'pv-node pv-campaign' },
          row('Campanha · ' + DEVICES[d] + (d === 'mobile' ? ' (celular e tablet)' : ''), h('b', {}, names[i] || '—'), ' ', badge('PAUSED')),
          h('p', { class: 'faint' }, [bid, ds.daily_cap ? money(ds.daily_cap) + ' por dia' : 'sem orçamento',
            ds.start_date ? 'começa em ' + ds.start_date : '',
            'Estados Unidos' + (ds.exclude_cities.length ? ` menos ${plural(ds.exclude_cities.length, 'cidade', 'cidades')}` : ''),
            (ds.spending_limit || status.limits?.max_spend_limit) ? 'no máximo ' + money(ds.spending_limit || status.limits.max_spend_limit) + ' no total' : 'sem limite total'].filter(Boolean).join(' · ')));
      }));
    }
    if (make === 'ads') {
      const byId = new Map(campaignList.map((c) => [c.id, c]));
      parts.push(h('div', { class: 'pv-node pv-campaign' }, row('Campanhas', to.length ? plural(to.length, 'campanha', 'campanhas') : h('span', { class: 'faint' }, 'escolha ao menos uma')),
        to.length ? h('ul', { class: 'pv-list' }, to.slice(0, 8).map((id) => h('li', {}, byId.get(id)?.name || id))) : null,
        to.length > 8 ? h('p', { class: 'faint' }, `…e mais ${to.length - 8}.`) : null));
    }
    if (make !== 'group') {
      const each = make === 'ads' ? to.length : campaignNames().length;
      parts.push(h('div', { class: 'pv-node pv-ads' },
        row('Anúncios', list.length ? `${list.length} em cada campanha, ${list.length * each} no total, pausados` : h('span', { class: 'faint' }, 'escolha imagens, headlines e botão')),
        list.length ? h('div', { class: 'pv-cards' }, list.slice(0, 6).map((a) => h('figure', { class: 'pv-card' },
          h('img', { src: '/launch/api/images/' + a.img.sha256, alt: '', loading: 'lazy' }),
          h('figcaption', {}, h('b', {}, a.title), h('span', { class: 'faint' }, st.brand || ''), a.cta ? h('span', { class: 'pv-cta' }, a.cta) : null)))) : null,
        list.length > 6 ? h('p', { class: 'faint' }, `…e mais ${list.length - 6}.`) : null));
    }
    if (w.length) parts.push(note('warn', h('b', {}, 'Avisos: '), w.join(' ')));
    parts.push(h('p', { class: 'faint' }, 'Tudo nasce pausado: alguém liga no Taboola.'));
    preview.replaceChildren(...parts);
  }

  // ---- send ----
  function stepProblem(el) {
    if (!s.account && connected.length) return 'Escolha a conta.';
    if (el === where) {
      if (newGroup) return gForm.problem();
      if (!groupSel.value) return 'Escolha o grupo, ou crie um novo.';
    }
    if (el === settingsPanel) return set.problem() || (s.devices === 'both' ? mobileProblem(mobileSettings(set.settings(), mobileOver()), status.limits || {}) : '');
    if (el === targets) {
      if (!to.length) return 'Escolha ao menos uma campanha.';
      if (!url()) return 'Falta o link da página.';
    }
    if (el === adsPanel) {
      if (!on().I.length || !on().H.length || !on().T.length) return 'Escolha ao menos uma imagem, uma headline e um botão.';
      if (s.mode === 'manual' && !s.review && !generated().length) return 'Monte ao menos um par.';
      if (!s.ai) return 'Diga se os anúncios foram feitos com IA.';
    }
    return '';
  }

  function problem() {
    for (const [label, el] of steps) {
      const p = el === sendPanel ? '' : stepProblem(el);
      if (p) return `${label}: ${p}`;
    }
    return '';
  }

  async function sendGroup(addCampaign) {
    const p = problem();
    if (p) {
      sendOut.replaceChildren(note('fail', p));
      return;
    }
    await busy(sendBtn, sendOut, async () => {
      const g = await api(`${s.net}/${encodeURIComponent(s.account)}/groups`, { method: 'POST', body: gForm.get() });
      const q2 = (more) => new URLSearchParams({ account: s.account, ...more });
      sendBtn.hidden = true;
      andCampaign.hidden = true;
      if (addCampaign) {
        location.assign('/launch/new?' + q2({ make: 'campaign', group: g.id }));
        return;
      }
      sendOut.replaceChildren(note('ok', h('b', {}, `Grupo ${g.name || g.id} criado. `), 'Ainda sem campanhas.'),
        h('div', { class: 'actions' }, h('a', { class: 'button primary', href: '/launch/new?' + q2({ make: 'campaign', group: g.id }) }, 'Criar uma campanha nele'),
          h('a', { class: 'button ghost', href: '/launch/groups?' + q2({}) }, 'Ver os grupos')));
      loadNext();
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
    if (!list.length) {
      sendOut.replaceChildren(note('fail', 'Nenhum anúncio para criar.'));
      return;
    }
    if (make === 'ads') {
      await busy(sendBtn, sendOut, async () => {
        const res = await api(`${s.net}/${encodeURIComponent(s.account)}/add-ads`, { method: 'POST', body: { campaigns: to,
          new_ads: list.map((a) => ({ title: a.title, description: description(), url: url(), image: a.img.sha256, cta: a.cta, ad_id: a.adId, ai: s.ai === 'yes' })) } });
        sendBtn.hidden = true;
        sendOut.replaceChildren(...res.done.map((d) => (d.error ? note('fail', h('b', {}, d.campaign + ': '), d.error) : note('ok', h('b', {}, d.campaign + ': '), plural(d.ads, 'anúncio adicionado', 'anúncios adicionados') + ', pausados'))),
          h('p', {}, h('a', { href: '/launch/ads?' + new URLSearchParams({ account: s.account, ...(to.length === 1 ? { campaign: to[0] } : {}) }) }, 'Ver os anúncios')));
      });
      return;
    }
    const body = {
      network: s.net,
      account: s.account,
      name: pairName.value.trim(),
      devices: s.devices,
      group_id: newGroup ? '' : groupSel.value,
      settings: set.settings(),
      ads: list.map((a) => ({ title: a.title, description: description(), url: url(), image: a.img.sha256, cta: a.cta, ad_id: a.adId, ai: s.ai === 'yes' })),
    };
    // The mobile campaign's own bid, daily budget or start, when asked.
    const mobile = s.devices === 'both' ? mobileSettings(body.settings, mobileOver()) : null;
    if (mobile) body.mobile = mobile;
    if (newGroup) body.new_group = gForm.get();
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
    if (r.result === 'done') {
      loadNext();
      return h('div', {}, note('ok', h('b', {}, (r.desktop && r.mobile ? 'Par criado' : 'Campanha criada') + ', pausado. '), 'Ligue no Taboola quando quiser que comece.'),
        h('p', {}, ...[camp(r.desktop), r.desktop && r.mobile ? ' · ' : null, camp(r.mobile)].filter(Boolean)),
        h('div', { class: 'actions' }, h('a', { class: 'button', href: '/launch/campaigns?' + new URLSearchParams({ account: s.account, group: r.group_id || '-' }) }, 'Ver o grupo'),
          h('a', { class: 'button ghost', href: '/launch/new?' + new URLSearchParams({ make: 'campaign', account: s.account, group: r.group_id || '' }) }, 'Outra campanha neste grupo')));
    }
    return h('div', {}, note(r.result === 'partial' ? 'warn' : 'fail', h('b', {}, r.result === 'partial' ? 'Criado em parte. ' : 'Nada foi criado. '), (r.problems || []).join(' · ')),
      h('p', {}, camp(r.desktop), r.desktop && r.mobile ? ' · ' : '', camp(r.mobile)),
      h('p', { class: 'faint' }, 'O que foi criado está pausado e aparece no Histórico.'));
  }

  // ---- drafts ----
  function draftBody() {
    return {
      net: s.net, account: s.account, group: groupSel.value, newGroup, group_fields: gForm.get(),
      name: pairName.value, settings: set.preset(), start: set.settings().start_date, end: set.settings().end_date,
      images: s.images, headlines: s.headlines, ctas: [...s.ctas], mode: s.mode, seed: s.seed, ai: s.ai, devices: s.devices, preset_id: presetId,
      manual: s.manual, review: s.review, mobile: s.mobile,
    };
  }
  async function saveDraft() {
    await busy(draftBtn, null, async () => {
      const body = { network: s.net, account: s.account, name: pairName.value.trim() || 'sem nome', body: draftBody() };
      const res = await api(s.draftId ? 'drafts/' + s.draftId : 'drafts', { method: s.draftId ? 'PUT' : 'POST', body });
      s.draftId = res.id;
      history.replaceState(null, '', '/launch/new?draft=' + res.id);
      draftOut.textContent = 'Rascunho salvo ' + date(new Date().toISOString()) + '.';
    });
  }
  async function openDraft(id) {
    const d = await api('drafts/' + id);
    const b = d.body || {};
    s.draftId = d.id;
    s.net = b.net || s.net;
    s.account = b.account || s.account;
    s.group = b.group || '';
    s.images = b.images || [];
    s.headlines = (b.headlines || []).map(withId);
    s.manual = b.manual || [];
    s.review = b.review || null;
    s.mobile = { ...s.mobile, ...(b.mobile || {}) };
    mobileOn.checked = !!s.mobile.on;
    s.ctas = new Set(b.ctas || ['Learn More']);
    s.mode = b.mode || 'mixed';
    s.seed = b.seed || 0;
    s.ai = b.ai || '';
    s.devices = b.devices || 'both';
    const dv = deviceBox.querySelector(`input[value=${s.devices}]`);
    if (dv) dv.checked = true;
    presetId = b.preset_id || null;
    pairName.value = b.name || '';
    set.set({ ...(b.settings || {}), settings: { ...(b.settings?.settings || {}), start_date: b.start || '', end_date: b.end || '' } });
    if (b.group_fields) {
      gForm.name.value = b.group_fields.name || '';
      gForm.set(b.group_fields);
    }
    if (b.newGroup) groupMode.querySelector('input[value=new]').click();
    const r = modeBox.querySelector(`input[value=${s.mode}]`);
    if (r) r.checked = true;
    head.querySelector('h1').textContent = 'Nova campanha · rascunho';
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
  if (q.get('draft')) {
    try {
      await openDraft(q.get('draft'));
    } catch (e) {
      main.prepend(note('fail', e.message));
    }
  }
  drawImages();
  drawHeadlines();
  drawCTAs();
  update();
  // Create sends people here with the set they just saved: /launch/new?set=7.
  if (/^\d+$/.test(q.get('set') || '')) lib.open(q.get('set'), true);
  for (const el of [where, settingsPanel, targets]) {
    el.addEventListener('input', () => update());
    el.addEventListener('change', () => update());
  }
  show(0);
  await loadAccounts();
  if (s.group && groups.some((g) => g.id === s.group)) groupSel.value = s.group;
  update();
  show(at);
}

function save(blob, name) {
  const a = h('a', { href: URL.createObjectURL(blob), download: name });
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 10000);
}

// drafts is the Rascunhos page: pairs saved to finish later.
export async function drafts({ main }) {
  main.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Rascunhos'),
    h('p', { class: 'lead muted' }, 'Pares salvos para terminar depois. Um rascunho some quando o par é criado.')),
  h('div', { class: 'actions' }, h('a', { class: 'button primary', href: '/launch/new' }, 'Novo par'))));
  const list = (await api('drafts')).drafts;
  if (!list.length) {
    main.append(h('p', { class: 'empty' }, 'Nenhum rascunho.'));
    return;
  }
  main.append(h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
    h('thead', {}, h('tr', {}, h('th', {}, 'Par'), h('th', {}, 'Conta'), h('th', {}, 'De'), h('th', {}, 'Mudado'), h('th', {}, ''))),
    h('tbody', {}, list.map((d) => {
      const out = h('span');
      const del = h('button', { type: 'button', class: 'small ghost', onclick: () => {
        if (!confirm(`Apagar o rascunho “${d.name}”?`)) return;
        busy(del, out, async () => { await api('drafts/' + d.id, { method: 'DELETE' }); location.reload(); });
      } }, 'Apagar');
      return h('tr', {}, h('td', {}, h('a', { href: '/launch/new?draft=' + d.id }, d.name)), h('td', {}, d.account),
        h('td', { class: 'muted' }, (d.made_by || '').split('@')[0]), h('td', { class: 'muted' }, date(d.changed_at)), h('td', {}, del, out));
    })))));
}
