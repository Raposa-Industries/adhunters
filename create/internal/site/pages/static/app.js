// Create's pages, on the shared shell (/create/_frame). One file routes by
// path: /create/ lists briefs, /create/new and /create/briefs/{id} are a
// brief and its options, /create/library browses the library, /create/rules
// shows the rules. Every change goes to /create/api; the server's worker
// does the paid work and this page asks again every 2 seconds while
// something is being made.

import { mountFrame, h } from '/create/_frame/frame.js';

const TABS = [
  { id: 'new', label: 'Novo brief', href: '/create/new' },
  { id: 'briefs', label: 'Briefs', href: '/create/' },
  { id: 'library', label: 'Biblioteca', href: '/create/library' },
  { id: 'rules', label: 'Regras', href: '/create/rules' },
];

const STATE = {
  draft: ['Rascunho', ''],
  reading: ['Lendo anúncios', 'review'],
  making: ['Fazendo', 'running'],
  ready: ['Pronto', 'paused'],
  failed: ['Falhou', 'rejected'],
};

// ---- talking to the server ---------------------------------------------------

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body instanceof FormData) opts.body = body;
  else if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  if (res.status === 204) return null;
  let data = null;
  try { data = await res.json(); } catch { /* not JSON */ }
  if (!res.ok) throw new Error((data && data.error) || `erro ${res.status}`);
  return data;
}

let rules = null;
async function getRules() {
  if (!rules) rules = await api('GET', '/create/api/rules');
  return rules;
}

let verticals = null;
async function getVerticals() {
  if (!verticals) {
    try { verticals = (await api('GET', '/create/library-api/api/verticals')).verticals || []; } catch { verticals = []; }
  }
  return verticals;
}

// ---- small helpers -----------------------------------------------------------

function toast(msg, kind = 'fail') {
  const n = h('div', { class: 'note ' + kind, role: 'status', style: 'position:fixed;right:16px;bottom:16px;z-index:50;max-width:420px' }, msg);
  document.body.append(n);
  setTimeout(() => n.remove(), 6000);
}

async function run(fn) {
  try { return await fn(); } catch (e) { toast(e.message); return undefined; }
}

function badge(state) {
  const [label, cls] = STATE[state] || [state, ''];
  return h('span', { class: 'badge ' + cls }, label);
}

function money(v) { return 'US$ ' + (v || 0).toFixed(2).replace('.', ','); }
function hhmm(t) { return new Date(t).toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' }); }
function day(t) { return new Date(t).toLocaleDateString('pt-BR', { day: '2-digit', month: '2-digit' }); }

function debounce(fn, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
}

function briefTitle(b) {
  return b.name || [b.vertical_name || b.vertical_id || 'Sem vertical', day(b.created_at)].join(' · ');
}

// ---- boot --------------------------------------------------------------------

const path = location.pathname.replace(/\/+$/, '') || '/create';
const main = document.querySelector('main');
let status = { user: '' };

async function boot() {
  status = await api('GET', '/create/api/status').catch(() => ({ user: '' }));
  let active = 'briefs';
  let aside = null;
  let render;
  let m;
  if (path === '/create/new') {
    active = 'new';
    render = () => briefPage(null);
  } else if ((m = path.match(/^\/create\/briefs\/(\d+)$/))) {
    aside = h('aside', { 'data-frame': 'filters' });
    render = () => briefPage(Number(m[1]), aside);
  } else if (path === '/create/library') {
    active = 'library';
    aside = h('aside', { 'data-frame': 'filters' });
    render = () => libraryPage(aside);
  } else if (path === '/create/rules') {
    active = 'rules';
    render = rulesPage;
  } else {
    render = listPage;
  }
  if (aside) document.body.prepend(aside);
  mountFrame({
    app: 'create', tabs: TABS, active, user: status.user,
    searchLabel: 'Buscar briefs',
    search: searchBriefs,
  });
  main.replaceChildren();
  await render();
}

let briefIndex = null;
async function searchBriefs(q) {
  if (!briefIndex) briefIndex = (await api('GET', '/create/api/briefs?limit=200')).briefs;
  const want = q.toLowerCase();
  return briefIndex.filter((b) => `${briefTitle(b)} ${b.vertical_name} ${b.requested_by}`.toLowerCase().includes(want)).slice(0, 20).map((b) => ({ title: briefTitle(b), sub: `${STATE[b.state]?.[0] || b.state} · ${b.options} opções`, href: `/create/briefs/${b.id}` }));
}

// ---- the list of briefs ------------------------------------------------------

async function listPage() {
  const { briefs } = await api('GET', '/create/api/briefs?limit=100');
  main.append(h('div', { class: 'page-head' },
    h('div', {}, h('h1', {}, 'Briefs'), h('p', { class: 'lead' }, 'O que foi pedido ao Create e as opções feitas. Imagens e títulos saem da OpenAI; os títulos sempre em inglês.')),
    h('div', { class: 'actions' }, h('a', { class: 'button primary', href: '/create/new' }, 'Novo brief'))));
  if (status.openai_why) main.append(h('div', { class: 'note warn' }, status.openai_why));
  if (!briefs.length) {
    main.append(h('div', { class: 'empty' }, 'Nenhum brief ainda. ', h('a', { href: '/create/new' }, 'Comece um.')));
    return;
  }
  main.append(h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
    h('thead', {}, h('tr', {}, h('th', {}, 'Brief'), h('th', {}, 'Estado'), h('th', { class: 'num' }, 'Opções'),
      h('th', { class: 'num' }, 'Escolhidas'), h('th', {}, 'Por'), h('th', { class: 'num' }, 'Custo'), h('th', {}, 'Dia'))),
    h('tbody', {}, briefs.map((b) => h('tr', {},
      h('td', {}, h('a', { href: `/create/briefs/${b.id}` }, briefTitle(b))),
      h('td', {}, badge(b.state)),
      h('td', { class: 'num' }, b.options), h('td', { class: 'num' }, b.chosen),
      h('td', { class: 'muted' }, b.requested_by || b.origin),
      h('td', { class: 'num' }, money(b.cost_usd)), h('td', { class: 'muted' }, day(b.created_at))))))));
}

// ---- a brief -----------------------------------------------------------------

// briefPage shows a brief: the form, its performing ads, what they share,
// the options, the save bar and the making log. id null is a new brief,
// written to the server on its first change.
async function briefPage(id, aside) {
  const [r, vs] = await Promise.all([getRules(), getVerticals()]);
  let detail = id ? await api('GET', `/create/api/briefs/${id}`) : null;
  const params = new URLSearchParams(location.search);
  const view = { show: 'all', angle: '' };
  const againOpen = new Set();

  // A new brief is made on its first change, with the refs of the link that
  // opened it (?ref=spy:ad:123).
  let making = null;
  async function ensure() {
    if (detail) return detail.brief.id;
    if (!making) {
      making = api('POST', '/create/api/briefs', { ...formValues(), refs: params.getAll('ref') }).then((d) => {
        detail = d;
        history.replaceState(null, '', `/create/briefs/${d.brief.id}`);
        return d.brief.id;
      });
      making.catch(() => { making = null; });
    }
    return making;
  }
  if (!id && params.getAll('ref').length) await run(ensure);

  const b = () => (detail ? detail.brief : { images: 6, headlines: 10, angles: [], own_headlines: [], analysis: [], state: 'draft' });

  // ---- form ----
  const vertical = h('select', {},
    h('option', { value: '' }, 'Escolha'),
    vs.map((v) => h('option', { value: v.id }, v.name)));
  const ages = h('input', { type: 'text', placeholder: '55 a 75' });
  const images = h('input', { type: 'number', min: 0, max: r.max_images });
  const headlines = h('input', { type: 'number', min: 0, max: r.max_headlines });
  const own = h('textarea', { rows: 3, placeholder: 'Um por linha. Pesam mais; nunca são copiados.' });
  const extra = h('textarea', { rows: 3, placeholder: 'Cozinha de manhã, luz natural. Manter o pó marrom do primeiro anúncio.' });
  const name = h('input', { type: 'text', placeholder: 'BP · Colher · 29/09' });
  const angleBox = h('div', { class: 'chips' });
  const customAngle = h('input', { type: 'text', placeholder: 'Outro ângulo', style: 'width:160px' });
  let angles = [...b().angles];

  function fill() {
    const x = b();
    vertical.value = x.vertical_id || '';
    ages.value = x.ages || '';
    images.value = x.images;
    headlines.value = x.headlines;
    own.value = (x.own_headlines || []).join('\n');
    extra.value = x.extra || '';
    name.value = x.name || '';
    angles = [...(x.angles || [])];
    drawAngles();
  }

  function drawAngles() {
    const all = [...new Set([...r.angles, ...angles])];
    angleBox.replaceChildren(...all.map((a) => {
      const box = h('input', { type: 'checkbox', checked: angles.includes(a) });
      box.addEventListener('change', () => {
        angles = box.checked ? [...angles, a] : angles.filter((x) => x !== a);
        save();
      });
      return h('label', { class: 'chip' }, box, h('span', {}, a));
    }));
  }
  customAngle.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter') return;
    e.preventDefault();
    const a = customAngle.value.trim();
    if (a && !angles.includes(a)) { angles.push(a); drawAngles(); save(); }
    customAngle.value = '';
  });

  function formValues() {
    const v = vs.find((x) => x.id === vertical.value);
    return {
      name: name.value, vertical_id: vertical.value, vertical_name: v ? v.name : '', ages: ages.value,
      images: Number(images.value) || 0, headlines: Number(headlines.value) || 0, angles,
      own_headlines: own.value.split('\n').map((s) => s.trim()).filter(Boolean), extra: extra.value,
    };
  }

  const save = debounce(async () => {
    if (!detail) { await run(ensure); return; }
    const res = await run(() => api('PATCH', `/create/api/briefs/${detail.brief.id}`, formValues()));
    if (res) { detail.brief = res; drawHead(); }
  }, 500);
  for (const el of [vertical, ages, images, headlines, own, extra, name]) el.addEventListener('input', save);

  // ---- head ----
  const head = h('div', { class: 'page-head' });
  function drawHead() {
    const x = b();
    head.replaceChildren(
      h('div', {},
        h('div', { class: 'crumbs' }, h('a', { href: '/create/' }, 'Briefs'), h('span', { class: 'sep' }, '›'),
          h('span', { 'aria-current': 'page' }, detail ? briefTitle(x) : 'Novo brief')),
        h('div', { class: 'row' }, h('h1', {}, detail ? briefTitle(x) : 'Novo brief'), badge(x.state)),
        h('p', { class: 'lead' }, detail
          ? [x.requested_by || (x.origin === 'page' ? '' : x.origin), `${x.rounds} rodada(s)`, `${money(x.cost_usd)} gastos na OpenAI`].filter(Boolean).join(' · ')
          : 'Imagens e títulos são feitos com a OpenAI. Títulos sempre em inglês.')),
      h('div', { class: 'actions' },
        h('button', { type: 'button', class: 'primary', disabled: x.state === 'making' || x.state === 'reading' || !!status.openai_why, onclick: makeRound },
          x.rounds ? 'Fazer outra rodada' : 'Criar opções')));
  }

  async function makeRound() {
    const bid = await run(ensure);
    if (!bid) return;
    await flushSave();
    const res = await run(() => api('POST', `/create/api/briefs/${bid}/make`));
    if (res) await refresh();
  }
  async function flushSave() {
    if (!detail) return;
    await run(() => api('PATCH', `/create/api/briefs/${detail.brief.id}`, formValues()));
  }

  // ---- references ----
  const refsBox = h('div', { class: 'thumbs refs' });
  const refInput = h('input', { type: 'text', placeholder: 'spy:ad:81234 ou library:creative:43', style: 'width:260px' });
  const fileInput = h('input', { type: 'file', accept: 'image/jpeg,image/png,image/gif', multiple: true, style: 'display:none' });
  refInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); addRef(); } });
  fileInput.addEventListener('change', async () => {
    const bid = await run(ensure);
    if (!bid) return;
    for (const f of fileInput.files) {
      const fd = new FormData();
      fd.append('file', f);
      await run(() => api('POST', `/create/api/briefs/${bid}/references`, fd));
    }
    fileInput.value = '';
    await refresh();
  });
  async function addRef() {
    const ref = refInput.value.trim();
    if (!ref) return;
    const bid = await run(ensure);
    if (!bid) return;
    if (await run(() => api('POST', `/create/api/briefs/${bid}/references`, { ref }))) refInput.value = '';
    await refresh();
  }
  function drawRefs() {
    const refs = detail ? detail.references : [];
    if (!refs.length) {
      refsBox.replaceChildren(h('div', { class: 'empty', style: 'grid-column:1/-1' }, 'Nenhum anúncio ainda. Sem referências, o Create parte da vertical e das instruções.'));
      return;
    }
    refsBox.replaceChildren(...refs.map((x) => h('figure', { class: 'thumb' + (x.state === 'failed' ? ' failed' : '') },
      x.url ? h('img', { src: x.url, alt: '' }) : h('div', { class: 'wait' + (x.state === 'failed' ? ' failed' : '') }, x.state === 'failed' ? x.error : 'Buscando a imagem…'),
      h('div', { class: 'tools' }, h('button', { type: 'button', title: 'Tirar', onclick: async () => { await run(() => api('DELETE', `/create/api/references/${x.id}`)); await refresh(); } }, '×')),
      h('figcaption', {}, x.headline || { spy_ad: 'Spy · anúncio ', library_creative: 'Biblioteca · ', upload: 'Do computador' }[x.kind] + (x.kind === 'upload' ? '' : x.ref_id)))));
  }
  async function readAds() {
    const bid = await run(ensure);
    if (!bid) return;
    if (await run(() => api('POST', `/create/api/briefs/${bid}/read`))) await refresh();
  }

  // ---- analysis ----
  const analysisBox = h('div');
  let analysisDirty = false;
  function drawAnalysis() {
    const rows = (b().analysis || []).map((a) => ({ ...a }));
    const commit = debounce(async () => {
      if (!detail) return;
      const res = await run(() => api('PATCH', `/create/api/briefs/${detail.brief.id}`, { analysis: rows.filter((x) => x.aspect.trim()) }));
      if (res) detail.brief = res;
      analysisDirty = false;
    }, 600);
    const cell = (a, k) => {
      const i = h('input', { type: 'text', value: a[k] || '' });
      i.addEventListener('input', () => { a[k] = i.value; analysisDirty = true; commit(); });
      return h('td', {}, i);
    };
    analysisBox.replaceChildren(
      rows.length
        ? h('div', { class: 'table-wrap' }, h('table', { class: 'list analysis' },
          h('thead', {}, h('tr', {}, h('th', {}, 'Aspecto'), h('th', {}, 'Manter'), h('th', {}, 'Pode variar'))),
          h('tbody', {}, rows.map((a) => h('tr', {}, cell(a, 'aspect'), cell(a, 'fixed'), cell(a, 'variable'))))))
        : h('p', { class: 'hint' }, 'Adicione anúncios e clique em Ler anúncios: o Create escreve o que eles têm em comum, e você ajusta antes de criar.'),
      h('div', { class: 'actions' },
        h('button', { type: 'button', class: 'small', onclick: () => { rows.push({ aspect: '', fixed: '', variable: '' }); detail.brief.analysis = rows; drawAnalysis(); } }, 'Adicionar linha')));
  }

  // ---- options ----
  const optionsBox = h('div');
  const logBox = h('ul', { class: 'log' });
  const saveBar = h('div', { class: 'savebar' });
  const saveName = h('input', { type: 'text', placeholder: 'Nome do set na biblioteca' });
  const aiBox = h('input', { type: 'checkbox', checked: true });
  const aiWarn = h('span', { class: 'warn', hidden: true }, 'Imagens da OpenAI são feitas por IA: o Taboola pede o selo. Tem certeza?');
  aiBox.addEventListener('change', () => { aiWarn.hidden = aiBox.checked; });

  function visible(o) {
    if (view.show === 'chosen' && !o.chosen) return false;
    if (view.show === 'not' && o.chosen) return false;
    if (view.show === 'starred' && !o.starred) return false;
    if (view.angle && o.kind === 'image' && o.angle !== view.angle) return false;
    return true;
  }

  async function mark(o, m) {
    const res = await run(() => api('PATCH', `/create/api/options/${o.id}`, m));
    if (res) { Object.assign(o, res); drawOptions(); drawFilters(); }
  }

  function imageCard(o) {
    const chosen = h('input', { type: 'checkbox', checked: o.chosen, disabled: o.state !== 'done', 'aria-label': 'Escolher' });
    chosen.addEventListener('change', () => mark(o, { chosen: chosen.checked }));
    const star = h('button', { type: 'button', class: 'star', 'aria-pressed': String(o.starred), title: 'Favorita', onclick: () => mark(o, { starred: !o.starred }) }, '★');
    const pic = o.state === 'done'
      ? h('img', { src: o.url, alt: o.idea, loading: 'lazy' })
      : h('div', { class: 'wait' + (o.state === 'failed' ? ' failed' : '') }, o.state === 'failed' ? o.error || 'Falhou' : o.state === 'making' ? 'Fazendo…' : 'Na fila');
    const fig = h('figure', { class: 'thumb' + (o.chosen ? ' chosen' : '') + (o.state === 'failed' ? ' failed' : '') },
      h('a', { class: 'cover', href: o.url || null, target: '_blank', rel: 'noopener' }, pic),
      h('div', { class: 'pick' }, chosen, star),
      h('figcaption', {}, o.note ? `Refeita: ${o.note}` : o.idea));
    if (o.state === 'done') {
      if (againOpen.has(o.id)) {
        const note = h('textarea', { rows: 2, placeholder: 'Homem mais velho no jardim, segurando a colher, sem olhar para a câmera.' });
        fig.append(h('div', { class: 'again' }, note, h('div', { class: 'row' },
          h('button', { type: 'button', class: 'small primary', onclick: async () => {
            if (await run(() => api('POST', `/create/api/options/${o.id}/again`, { note: note.value }))) { againOpen.delete(o.id); await refresh(); }
          } }, 'Refazer'),
          h('button', { type: 'button', class: 'small ghost', onclick: () => { againOpen.delete(o.id); drawOptions(); } }, 'Cancelar'))));
      } else {
        fig.append(h('div', { class: 'again' }, h('button', { type: 'button', class: 'small ghost', onclick: () => { againOpen.add(o.id); drawOptions(); } }, 'De novo, com uma nota')));
      }
    }
    return fig;
  }

  function headlineRow(o) {
    const chosen = h('input', { type: 'checkbox', checked: o.chosen, 'aria-label': 'Escolher' });
    chosen.addEventListener('change', () => mark(o, { chosen: chosen.checked }));
    const text = h('input', { type: 'text', value: o.text });
    const count = h('span', { class: 'count' }, [...o.text].length);
    text.addEventListener('input', () => { count.textContent = [...text.value].length; });
    text.addEventListener('change', () => { if (text.value.trim() && text.value !== o.text) mark(o, { text: text.value }); });
    const star = h('button', { type: 'button', class: 'star', 'aria-pressed': String(o.starred), title: 'Favorito', onclick: () => mark(o, { starred: !o.starred }) }, '★');
    const warns = h('div', { class: 'warns' }, (o.warnings || []).map((w) => h('div', { class: 'warn' }, w.message,
      w.kind === 'blocked' ? (w.alternatives || []).slice(0, 3).map((alt) => h('button', { type: 'button', class: 'small', onclick: () => mark(o, { text: swap(o.text, w.blocked, alt) }) }, 'Trocar por ' + alt)) : null)));
    return h('div', { class: 'head-row' + (o.chosen ? ' chosen' : '') }, chosen, h('div', {}, text, warns), count, star);
  }

  function drawOptions() {
    if (!detail || !detail.options.length) {
      optionsBox.replaceChildren(h('div', { class: 'empty' }, detail && detail.brief.state === 'making' ? 'Fazendo as primeiras opções…' : 'As opções aparecem aqui.'));
      saveBar.hidden = true;
      return;
    }
    const opts = detail.options;
    const imgs = opts.filter((o) => o.kind === 'image');
    const heads = opts.filter((o) => o.kind === 'headline');
    const groups = new Map();
    for (const o of imgs.filter(visible)) {
      if (!groups.has(o.angle)) groups.set(o.angle, []);
      groups.get(o.angle).push(o);
    }
    const kids = [];
    kids.push(h('div', { class: 'spread' },
      h('h2', {}, `Imagens · ${imgs.filter((o) => o.chosen).length} de ${imgs.filter((o) => o.state === 'done').length} escolhidas`),
      h('button', { type: 'button', disabled: detail.brief.state === 'making' || !!status.openai_why, onclick: async () => {
        await flushSave();
        if (await run(() => api('POST', `/create/api/briefs/${detail.brief.id}/make`, { images: 3, headlines: 0, new_angle: true }))) await refresh();
      } }, 'Mais 3, mesmo brief, ângulo novo')));
    for (const [angle, list] of groups) {
      kids.push(h('div', { class: 'angle-head' }, h('h3', {}, angle || 'Sem ângulo'), h('span', { class: 'count' }, list.length)));
      kids.push(h('div', { class: 'thumbs' }, list.map(imageCard)));
    }
    const hv = heads.filter(visible);
    kids.push(h('h2', { style: 'margin-top:22px' }, `Títulos · ${heads.filter((o) => o.chosen).length} de ${heads.length} escolhidos`));
    kids.push(h('p', { class: 'hint' }, 'Os avisos do Taboola só avisam: você decide. Edite um título antes de salvar, se quiser.'));
    kids.push(h('div', { class: 'heads' }, hv.map(headlineRow)));
    optionsBox.replaceChildren(...kids);
    const chosen = opts.filter((o) => o.chosen);
    saveBar.hidden = false;
    saveBar.replaceChildren(
      h('b', {}, `${chosen.length} escolhida(s)`), saveName,
      h('label', { class: 'check' }, aiBox, 'Feitas com IA'), aiWarn,
      h('span', { class: 'fr-spacer' }),
      h('button', { type: 'button', disabled: !chosen.length, onclick: () => saveSet(false) }, 'Salvar na biblioteca'),
      h('button', { type: 'button', class: 'primary', disabled: !chosen.length, onclick: () => saveSet(true) }, 'Salvar e abrir no Launch'));
  }

  function drawFilters() {
    if (!aside || !detail) return;
    const imgs = detail.options.filter((o) => o.kind === 'image');
    const counts = new Map();
    for (const o of imgs) counts.set(o.angle, (counts.get(o.angle) || 0) + 1);
    const radio = (name, value, label, current, set) => {
      const i = h('input', { type: 'radio', name, value, checked: current === value });
      i.addEventListener('change', () => { set(value); drawOptions(); });
      return h('label', { class: 'chip' }, i, h('span', {}, label));
    };
    const keep = aside.querySelector('.fr-filters-head');
    aside.replaceChildren(...[keep].filter(Boolean),
      h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Mostrar'), h('div', { class: 'chips' },
        [['all', 'Todas'], ['chosen', 'Escolhidas'], ['not', 'Não escolhidas'], ['starred', 'Favoritas']]
          .map(([v, l]) => radio('show', v, l, view.show, (x) => { view.show = x; })))),
      h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Ângulo'), h('div', { class: 'chips' },
        radio('angle', '', 'Todos', view.angle, (x) => { view.angle = x; }),
        [...counts].map(([a, n]) => radio('angle', a, `${a || 'Sem ângulo'} ${n}`, view.angle, (x) => { view.angle = x; })))));
  }

  async function saveSet(openLaunch) {
    const chosen = detail.options.filter((o) => o.chosen).map((o) => o.id);
    let v = await run(() => api('POST', `/create/api/briefs/${detail.brief.id}/save`, {
      option_ids: chosen, name: saveName.value, ai_label: aiBox.checked ? 'ai' : 'not_ai',
    }));
    if (!v) return;
    toast('Salvando na biblioteca…', 'ok');
    for (let i = 0; i < 90 && (v.state === 'waiting' || v.state === 'saving'); i++) {
      await new Promise((r) => setTimeout(r, 1000));
      v = await api('GET', `/create/api/saves/${v.id}`).catch(() => v);
    }
    await refresh();
    if (v.state !== 'done') { toast(v.error || 'Ainda salvando: veja o log abaixo.'); return; }
    if (openLaunch) location.assign(`/launch/new?set=${v.library_set_id}`);
    else toast(`Salvo na biblioteca como “${v.name}”.`, 'ok');
  }

  function drawLog() {
    const ev = detail ? detail.events : [];
    logBox.replaceChildren(...(ev.length ? ev.map((e) => h('li', { class: e.failed ? 'failed' : '' }, h('time', {}, hhmm(e.at)), h('span', {}, e.line)))
      : [h('li', {}, h('time', {}, ''), h('span', { class: 'muted' }, 'Nada ainda.'))]));
  }

  // ---- layout ----
  const fieldsPanel = h('section', { class: 'panel' },
    h('h2', {}, 'Brief'),
    h('div', { class: 'fields' },
      h('label', { class: 'field' }, 'Vertical', vertical),
      h('label', { class: 'field' }, 'Idade das pessoas', ages, h('span', { class: 'hint' }, 'Sempre pessoas, espontâneas, sem olhar para a câmera.')),
      h('label', { class: 'field' }, 'Imagens', images, h('span', { class: 'hint' }, 'Umas duas em três são variações próximas; o resto, ângulos novos.')),
      h('label', { class: 'field' }, 'Títulos', headlines, h('span', { class: 'hint' }, 'Sempre em inglês. Irmãos dos títulos de referência, nunca cópias.'))),
    h('div', { class: 'field', style: 'margin-top:12px' }, 'Ângulos para tentar', angleBox, customAngle),
    h('label', { class: 'field', style: 'margin-top:12px' }, 'Títulos de referência', own),
    h('label', { class: 'field', style: 'margin-top:12px' }, 'Instruções extras', extra),
    h('label', { class: 'field', style: 'margin-top:12px' }, 'Nome do brief', name),
    h('p', { class: 'hint', style: 'margin-top:10px' }, 'As regras do Taboola só avisam aqui: sintomas, não doenças; sem promessa de cura; sem antes e depois. Palavras bloqueadas são evitadas e marcadas.'));
  const refsPanel = h('section', { class: 'panel' },
    h('div', { class: 'spread' }, h('h2', {}, 'Anúncios que performam'),
      h('button', { type: 'button', class: 'small', onclick: readAds, disabled: !!status.openai_why }, 'Ler anúncios')),
    h('p', { class: 'hint' }, 'Pesam mais. O Create lê o padrão deles; eles nunca vão para o gerador de imagens.'),
    refsBox,
    h('div', { class: 'actions' }, refInput, h('button', { type: 'button', class: 'small', onclick: addRef }, 'Adicionar'),
      h('button', { type: 'button', class: 'small', onclick: () => fileInput.click() }, 'Do computador'), fileInput));
  const analysisPanel = h('section', { class: 'panel' },
    h('h2', {}, 'O que os anúncios têm em comum'),
    h('p', { class: 'hint' }, 'Mude qualquer linha antes de criar: o que está em Manter fica em toda variação próxima.'),
    analysisBox);

  main.append(head);
  if (status.openai_why) main.append(h('div', { class: 'note warn', style: 'margin-bottom:14px' }, status.openai_why));
  main.append(h('div', { class: 'grid-2' }, h('div', { class: 'stack' }, fieldsPanel), h('div', { class: 'stack' }, refsPanel, analysisPanel)));
  main.append(h('section', { class: 'panel', style: 'margin-top:14px' }, optionsBox, saveBar));
  main.append(h('section', { class: 'panel', style: 'margin-top:14px' }, h('h2', {}, 'Fazendo'), logBox));

  fill();
  drawHead();
  drawRefs();
  drawAnalysis();
  drawOptions();
  drawFilters();
  drawLog();

  // ---- polling ----
  let lastOptions = '';
  async function refresh() {
    if (!detail) return;
    const d = await api('GET', `/create/api/briefs/${detail.brief.id}`).catch(() => null);
    if (!d) return;
    const hadAnalysis = JSON.stringify(detail.brief.analysis);
    const keepForm = detail.brief;
    detail = d;
    // The form is the person's while they type: only what the server
    // writes (state, cost, analysis from a read) is taken from it.
    detail.brief = { ...keepForm, state: d.brief.state, error: d.brief.error, rounds: d.brief.rounds, cost_usd: d.brief.cost_usd, analysis: analysisDirty ? keepForm.analysis : d.brief.analysis };
    drawHead();
    drawRefs();
    if (!analysisDirty && hadAnalysis !== JSON.stringify(d.brief.analysis)) drawAnalysis();
    const sig = JSON.stringify(d.options);
    const typingHere = optionsBox.contains(document.activeElement) && document.activeElement.tagName !== 'BUTTON';
    if (sig !== lastOptions && !typingHere) { lastOptions = sig; drawOptions(); drawFilters(); }
    drawLog();
  }
  lastOptions = detail ? JSON.stringify(detail.options) : '';
  setInterval(() => {
    if (!detail || document.hidden) return;
    const busy = ['making', 'reading'].includes(detail.brief.state) || detail.references.some((x) => x.state === 'waiting')
      || detail.saves.some((x) => x.state === 'waiting' || x.state === 'saving');
    if (busy) refresh();
  }, 2000);
}

// swap puts alt in place of a blocked word, in its case (the server's
// checks name the word; the person chose the alternative).
function swap(text, blocked, alt) {
  const esc = blocked.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/\s+/g, '\\s+');
  const ending = /\s/.test(blocked.trim()) ? '' : '(?:s|es|ing|ed|ting|ful)?';
  return text.replace(new RegExp(`(^|[^\\p{L}\\p{N}])(${esc}${ending})(?![\\p{L}\\p{N}])`, 'giu'), (_, before, word) => {
    const letters = word.replace(/[^\p{L}]/gu, '');
    let out = alt;
    if (letters.length > 1 && letters === letters.toUpperCase()) out = alt.toUpperCase();
    else if (/^\p{Lu}/u.test(word)) out = alt.replace(/(^|\s)(\p{Ll})/gu, (m, s, c) => s + c.toUpperCase());
    return before + out;
  });
}

// ---- the library ---------------------------------------------------------------

async function libraryPage(aside) {
  const vs = await getVerticals();
  const view = { tab: 'creatives', vertical: '', angle: '', q: '' };
  const body = h('div');
  const detailBox = h('div', { class: 'lib-detail' });

  function drawFilters() {
    const keep = aside.querySelector('.fr-filters-head');
    const radio = (group, value, label) => {
      const i = h('input', { type: 'radio', name: group, value, checked: view[group] === value });
      i.addEventListener('change', () => { view[group] = value; load(); });
      return h('label', { class: 'chip' }, i, h('span', {}, label));
    };
    const q = h('input', { type: 'text', placeholder: 'Buscar nome ou texto', value: view.q });
    q.addEventListener('input', debounce(() => { view.q = q.value; load(); }, 300));
    aside.replaceChildren(...[keep].filter(Boolean),
      h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Buscar'), q),
      h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Vertical'), h('div', { class: 'chips' },
        radio('vertical', '', 'Todas'), vs.map((v) => radio('vertical', v.id, v.name)))));
  }

  const tabs = h('div', { class: 'segmented' }, [['creatives', 'Criativos'], ['headlines', 'Títulos'], ['sets', 'Sets']].map(([v, l]) => {
    const i = h('input', { type: 'radio', name: 'tab', value: v, checked: view.tab === v });
    i.addEventListener('change', () => { view.tab = v; detailBox.replaceChildren(); load(); });
    return h('label', {}, i, h('span', {}, l));
  }));

  main.append(h('div', { class: 'page-head' },
    h('div', {}, h('h1', {}, 'Biblioteca'), h('p', { class: 'lead' }, 'Todo criativo e título que o time guarda, o mesmo do Launch.')),
    h('div', { class: 'actions' }, tabs)),
  h('div', { class: 'grid-2' }, body, detailBox));

  async function load() {
    const q = new URLSearchParams();
    if (view.vertical) q.set('vertical', view.vertical);
    if (view.q && view.tab !== 'sets') q.set('q', view.q);
    q.set('limit', '120');
    const base = '/create/library-api';
    try {
      if (view.tab === 'creatives') {
        const { creatives } = await api('GET', `${base}/api/creatives?${q}`);
        body.replaceChildren(creatives.length ? h('div', { class: 'thumbs' }, creatives.map((c) => h('figure', { class: 'thumb', style: 'cursor:pointer', onclick: () => showCreative(c) },
          h('img', { src: `${base}/thumbs/${c.id}`, alt: c.idea || c.name, loading: 'lazy' }),
          h('figcaption', {}, h('b', {}, c.name), ' · ', c.angle || '')))) : h('div', { class: 'empty' }, 'Nada aqui ainda.'));
      } else if (view.tab === 'headlines') {
        const { headlines } = await api('GET', `${base}/api/headlines?${q}`);
        body.replaceChildren(headlines.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
          h('thead', {}, h('tr', {}, h('th', {}, 'Título'), h('th', {}, 'Ângulo'), h('th', {}, 'Origem'))),
          h('tbody', {}, headlines.map((x) => h('tr', {}, h('td', {}, x.text), h('td', { class: 'muted' }, x.angle), h('td', { class: 'muted' }, x.origin))))))
          : h('div', { class: 'empty' }, 'Nada aqui ainda.'));
      } else {
        const { sets } = await api('GET', `${base}/api/sets?${q}`);
        body.replaceChildren(sets.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
          h('thead', {}, h('tr', {}, h('th', {}, 'Set'), h('th', { class: 'num' }, 'Criativos'), h('th', { class: 'num' }, 'Títulos'), h('th', {}, 'Por'), h('th', {}, ''))),
          h('tbody', {}, sets.map((x) => h('tr', {}, h('td', {}, x.name), h('td', { class: 'num' }, x.creatives), h('td', { class: 'num' }, x.headlines),
            h('td', { class: 'muted' }, x.made_by), h('td', {}, h('a', { href: `/launch/new?set=${x.id}` }, 'Abrir no Launch')))))))
          : h('div', { class: 'empty' }, 'Nada aqui ainda.'));
      }
    } catch (e) {
      body.replaceChildren(h('div', { class: 'note fail' }, 'A biblioteca não respondeu: ' + e.message));
    }
  }

  function showCreative(c) {
    detailBox.replaceChildren(h('section', { class: 'panel' },
      h('img', { src: `/create/library-api/files/${c.id}`, alt: c.idea || c.name }),
      h('h2', { style: 'margin-top:12px' }, c.name),
      h('dl', {},
        h('dt', {}, 'Ângulo'), h('dd', {}, c.angle || '—'),
        h('dt', {}, 'Origem'), h('dd', {}, { create: 'Feito no Create', upload: 'Enviado', drive: 'Do Drive' }[c.origin] || c.origin),
        h('dt', {}, 'Selo de IA'), h('dd', {}, { ai: 'Feito com IA', not_ai: 'Não é IA', unset: 'Não marcado' }[c.ai_label] || c.ai_label),
        h('dt', {}, 'Ideia'), h('dd', {}, c.idea || '—'),
        h('dt', {}, 'Tamanho'), h('dd', {}, `${c.width} × ${c.height} · ${(c.bytes / 1048576).toFixed(1).replace('.', ',')} MB`)),
      h('div', { class: 'actions' },
        h('a', { class: 'button primary', href: `/create/new?ref=library:creative:${c.id}` }, 'Fazer parecido no Create'),
        h('a', { class: 'button', href: `/create/library-api/files/${c.id}`, download: c.name }, 'Baixar'))));
  }

  drawFilters();
  await load();
}

// ---- the rules -----------------------------------------------------------------

async function rulesPage() {
  const r = await getRules();
  main.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Regras'),
    h('p', { class: 'lead' }, 'O que o Create segue ao escrever e o que ele marca. As regras do Taboola só avisam: quem decide é você.'))));
  main.append(h('section', { class: 'panel' }, h('h2', {}, 'Sempre'),
    h('ul', {},
      h('li', {}, 'Imagens e títulos feitos com a OpenAI. Títulos sempre em inglês.'),
      h('li', {}, 'Pessoas da idade pedida, espontâneas, sem olhar para a câmera, foto com cara de real.'),
      h('li', {}, 'Sem texto, logo, antes e depois, celebridades ou close de partes do corpo nas imagens.'),
      h('li', {}, 'Títulos de 34 a 45 caracteres (nunca mais de 60), sem palavras em maiúsculas, sem "!!", sintomas e não doenças, sem promessa de cura, sem valores.'),
      h('li', {}, 'O selo de IA é escolha sua ao salvar; o Create avisa se estiver desligado.'))));
  main.append(h('section', { class: 'panel' }, h('h2', {}, 'Ângulos do time'), h('div', { class: 'chips' }, r.angles.map((a) => h('span', { class: 'badge' }, a)))));
  main.append(h('section', { class: 'panel' }, h('h2', {}, `Palavras que o Taboola já bloqueou para o time (${r.blocked.length})`),
    h('ul', { class: 'words' }, r.blocked.map((b) => h('li', {}, h('b', {}, b.text),
      b.description ? h('span', { class: 'muted' }, ' · também em descrições') : null,
      b.alternatives && b.alternatives.length ? h('span', { class: 'muted' }, ' → ' + b.alternatives.join(', ')) : null)))));
}

boot().catch((e) => { main.replaceChildren(h('div', { class: 'note fail' }, 'Não carregou: ' + e.message)); });
