// Create's pages, on the shared shell (/create/_frame). Create is a chat: a
// session is a vertical and a name (the library folders its saves go in),
// and every turn is a prompt sent with the pictures and headlines the person
// picked. Any picture or headline can be picked for the next turn, so it is
// iterated on for as long as they like; what they keep is saved into the
// library. /create/ opens or starts a session, /create/s/{id} is one,
// /create/library browses the library, /create/rules shows the rules. Every
// change goes to /create/api; the server's worker does the paid work and
// this page asks again every 2 seconds while something is being made.

import { mountFrame, h } from '/create/_frame/frame.js';

const TABS = [
  { id: 'chat', label: 'Criar', href: '/create/' },
  { id: 'library', label: 'Biblioteca', href: '/create/library' },
  { id: 'rules', label: 'Regras', href: '/create/rules' },
];

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

let categories = null;
async function getCategories() {
  if (!categories) categories = (await api('GET', '/create/api/verticals')).categories || [];
  return categories;
}

function remember(key, value) {
  try {
    if (value === undefined) return localStorage.getItem(key) || '';
    localStorage.setItem(key, value);
  } catch { /* private windows: the page just forgets */ }
  return '';
}

// ---- small helpers -----------------------------------------------------------

function toast(msg, kind = 'fail') {
  const n = h('div', { class: 'note toast ' + kind, role: 'status' }, msg);
  document.body.append(n);
  setTimeout(() => n.remove(), 6000);
}

async function run(fn) {
  try { return await fn(); } catch (e) { toast(e.message); return undefined; }
}

function money(v) { return 'US$ ' + (v || 0).toFixed(2).replace('.', ','); }
function when(t) {
  const d = new Date(t);
  return d.toLocaleDateString('pt-BR', { day: '2-digit', month: '2-digit' }) + ' ' + d.toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' });
}
function plural(n, one, many) { return `${n} ${n === 1 ? one : many}`; }

function verticalSelect(cats, value) {
  const sel = h('select', { 'aria-label': 'Vertical' }, h('option', { value: '' }, 'Escolha a vertical'),
    cats.map((c) => h('optgroup', { label: c.name }, c.verticals.map((v) => h('option', { value: v.id, selected: v.id === value }, v.name)))));
  return sel;
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

// ---- boot --------------------------------------------------------------------

const path = location.pathname.replace(/\/+$/, '') || '/create';
const main = document.querySelector('main');
let status = { user: '', openai_why: '' };

async function boot() {
  status = await api('GET', '/create/api/status').catch(() => ({ user: '', openai_why: '' }));
  let active = 'chat';
  let render;
  let m;
  const aside = h('aside', { 'data-frame': 'filters' });
  if ((m = path.match(/^\/create\/s\/(\d+)$/))) {
    render = () => sessionPage(Number(m[1]), aside);
  } else if (path === '/create/library') {
    active = 'library';
    render = () => libraryPage(aside);
  } else if (path === '/create/rules') {
    active = 'rules';
    render = rulesPage;
  } else {
    render = () => startPage(aside);
  }
  if (active !== 'rules') document.body.prepend(aside);
  mountFrame({ app: 'create', tabs: TABS, active, user: status.user, searchLabel: 'Buscar sessões', search: searchSessions });
  main.replaceChildren();
  await render();
}

async function searchSessions(q) {
  const { sessions } = await api('GET', '/create/api/sessions?limit=200');
  const want = q.toLowerCase();
  return sessions.filter((s) => `${s.name} ${s.vertical_name}`.toLowerCase().includes(want)).slice(0, 20)
    .map((s) => ({ title: s.name, sub: `${s.vertical_name} · ${plural(s.images, 'imagem', 'imagens')} · ${plural(s.headlines, 'headline', 'headlines')}`, href: `/create/s/${s.id}` }));
}

// ---- the session column ----------------------------------------------------------

// sessionColumn fills the left column: the vertical, a new session in it,
// and the sessions already there.
async function sessionColumn(aside, verticalID, currentID) {
  const cats = await getCategories();
  const keep = aside.querySelector('.fr-filters-head');
  const sel = verticalSelect(cats, verticalID);
  const list = h('nav', { class: 'sessions', 'aria-label': 'Sessões' });
  const name = h('input', { type: 'text', placeholder: 'Nome da sessão', maxlength: 120 });
  const start = h('button', { type: 'submit', class: 'primary small' }, 'Nova sessão');
  const form = h('form', { class: 'new-session', onsubmit: async (e) => {
    e.preventDefault();
    if (!sel.value) { toast('Escolha a vertical'); sel.focus(); return; }
    const s = await run(() => api('POST', '/create/api/sessions', { name: name.value, vertical_id: sel.value }));
    if (s) location.href = `/create/s/${s.id}`;
  } }, name, start);

  async function load() {
    remember('create.vertical', sel.value);
    const q = sel.value ? `?vertical=${encodeURIComponent(sel.value)}` : '';
    const { sessions } = await api('GET', `/create/api/sessions${q}`);
    list.replaceChildren(sessions.length
      ? h('ul', {}, sessions.map((s) => h('li', {}, h('a', { href: `/create/s/${s.id}`, 'aria-current': s.id === currentID ? 'page' : null },
        h('b', {}, s.name), h('small', {}, (sel.value ? '' : s.vertical_name + ' · ') + `${s.images} img · ${s.headlines} hl` + (s.making ? ' · fazendo' : ''))))))
      : h('p', { class: 'faint' }, sel.value ? 'Nenhuma sessão nesta vertical.' : 'Nenhuma sessão ainda.'));
  }
  sel.addEventListener('change', () => run(load));
  aside.replaceChildren(...[keep].filter(Boolean),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Vertical'), sel),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Nova sessão'), form,
      h('p', { class: 'hint' }, 'A sessão vira uma pasta dentro da pasta da vertical na biblioteca.')),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Sessões'), list));
  await load();
  return { sel, name, reload: () => run(load) };
}

// ---- start -------------------------------------------------------------------------

async function startPage(aside) {
  const col = await sessionColumn(aside, remember('create.vertical'), 0);
  main.append(h('div', { class: 'start' },
    h('h1', {}, 'Criar'),
    h('p', { class: 'lead' }, 'Escolha a vertical e abra uma sessão nova à esquerda, ou continue uma das sessões da lista. Numa sessão você pede imagens e headlines, escolhe as que gostou e pede variações delas quantas vezes quiser. O que você salvar vai para a biblioteca, na pasta da sessão.'),
    status.openai_why ? h('div', { class: 'note warn' }, 'Fazer está desligado: ' + status.openai_why) : null));
  col.name.focus();
}

// ---- a session -------------------------------------------------------------------

async function sessionPage(id, aside) {
  let d = await api('GET', `/create/api/sessions/${id}`).catch((e) => { main.append(h('div', { class: 'note fail' }, e.message)); return null; });
  if (!d) return;
  remember('create.vertical', d.session.vertical_id);
  let column = null;
  sessionColumn(aside, d.session.vertical_id, id).then((c) => { column = c; }).catch(() => {});
  const r = await getRules();

  // picked: the item ids picked for the next turn or a save, in the order
  // they were picked (a prompt can say "the first").
  let picked = [];
  let counts = null; // the person's own counts, once they touch them
  let ai = true;
  let polling = null;
  const byID = () => new Map(d.items.map((it) => [it.id, it]));

  const head = h('header', { class: 'chat-head' });
  const log = h('div', { class: 'chat-log', 'aria-live': 'polite' });
  const composer = h('div', { class: 'composer' });
  main.append(head, log, composer);

  // -- the head --
  function drawHead() {
    const s = d.session;
    const title = h('h1', { title: 'Clique para renomear', tabindex: 0 }, s.name);
    const rename = () => {
      const input = h('input', { type: 'text', value: s.name, maxlength: 120, 'aria-label': 'Nome da sessão' });
      const done = async (ok) => {
        if (ok && input.value.trim() && input.value !== s.name) {
          const res = await run(() => api('PATCH', `/create/api/sessions/${id}`, { name: input.value }));
          if (res) d.session = { ...d.session, ...res };
        }
        drawHead();
      };
      input.addEventListener('keydown', (e) => { if (e.key === 'Enter') done(true); if (e.key === 'Escape') done(false); });
      input.addEventListener('blur', () => done(true));
      title.replaceWith(input);
      input.focus();
      input.select();
    };
    title.addEventListener('click', rename);
    title.addEventListener('keydown', (e) => { if (e.key === 'Enter') rename(); });
    head.replaceChildren(...[
      h('div', { class: 'crumbs' }, h('span', {}, 'Biblioteca'), h('span', { class: 'sep' }, '›'), h('span', {}, s.vertical_name), h('span', { class: 'sep' }, '›'),
        h('span', { 'aria-current': 'page' }, s.library_set_id ? 'pasta da sessão' : 'pasta criada no primeiro salvar')),
      h('div', { class: 'spread' }, title,
        h('div', { class: 'actions' },
          h('span', { class: 'faint mono' }, money(s.cost_usd)),
          s.library_set_id ? h('a', { class: 'button small', href: `/launch/new?set=${s.library_set_id}` }, 'Abrir no Launch') : null)),
      status.openai_why ? h('div', { class: 'note warn' }, 'Fazer está desligado: ' + status.openai_why) : null].filter(Boolean));
  }

  // -- the log --
  function itemTile(it) {
    const on = picked.includes(it.id);
    const n = on ? picked.indexOf(it.id) + 1 : 0;
    if (it.kind === 'image') {
      const pic = it.state === 'done'
        ? h('img', { src: it.image_url, alt: it.brief || 'imagem', loading: 'lazy' })
        : h('div', { class: 'wait' + (it.state === 'failed' ? ' failed' : '') }, it.state === 'failed' ? (it.error || 'Falhou') : it.state === 'making' ? 'Fazendo…' : 'Na fila');
      const fig = h('figure', { class: 'thumb tile' + (on ? ' chosen' : '') + (it.state === 'failed' ? ' failed' : ''), dataset: { item: it.id } },
        it.state === 'done'
          ? h('button', { type: 'button', class: 'cover', 'aria-pressed': String(on), title: on ? 'Tirar da escolha' : 'Escolher', onclick: () => toggle(it) }, pic)
          : pic,
        on ? h('span', { class: 'pick-n' }, n) : null,
        it.saved ? h('span', { class: 'saved' }, 'Salvo') : null,
        it.state === 'done' ? h('button', { type: 'button', class: 'zoom small', title: 'Ver grande', onclick: () => zoom(it) }, '⤢') : null);
      return fig;
    }
    const text = h('span', { class: 'hl-text' }, it.text);
    const len = [...it.text].length;
    const row = h('div', { class: 'hl' + (on ? ' chosen' : ''), dataset: { item: it.id } },
      h('button', { type: 'button', class: 'hl-pick', 'aria-pressed': String(on), title: on ? 'Tirar da escolha' : 'Escolher', onclick: () => toggle(it) },
        on ? String(n) : ''),
      h('div', { class: 'hl-body' }, text,
        (it.warnings || []).length ? h('div', { class: 'warns' }, it.warnings.map((w) => h('div', { class: 'warn' }, w.message,
          !it.saved && w.kind === 'blocked' ? (w.alternatives || []).slice(0, 3).map((alt) => h('button', { type: 'button', class: 'small', onclick: () => edit(it, swap(it.text, w.blocked, alt)) }, 'Trocar por ' + alt)) : null))) : null),
      h('span', { class: 'count' + (len > 60 ? ' over' : '') }, len),
      it.saved ? h('span', { class: 'saved' }, 'Salvo') : h('button', { type: 'button', class: 'ghost small', title: 'Editar', onclick: () => editInline(it, row) }, 'Editar'));
    return row;
  }

  function editInline(it, row) {
    const input = h('input', { type: 'text', value: it.text, maxlength: 200, 'aria-label': 'Headline' });
    const body = row.querySelector('.hl-body');
    body.replaceChildren(input);
    input.focus();
    const done = (ok) => { if (ok && input.value.trim() && input.value !== it.text) edit(it, input.value); else draw(); };
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') done(true); if (e.key === 'Escape') done(false); });
    input.addEventListener('blur', () => done(true));
  }

  async function edit(it, text) {
    const res = await run(() => api('PATCH', `/create/api/items/${it.id}`, { text }));
    if (res) Object.assign(it, res);
    draw();
  }

  function results(items) {
    const imgs = items.filter((it) => it.kind === 'image');
    const heads = items.filter((it) => it.kind === 'headline');
    return [
      imgs.length ? h('div', { class: 'tiles' }, imgs.map(itemTile)) : null,
      heads.length ? h('div', { class: 'hls' }, heads.map(itemTile)) : null,
    ];
  }

  function pickedChips(ids) {
    const all = byID();
    return h('div', { class: 'mini' }, ids.map((pid) => {
      const it = all.get(pid);
      if (!it) return null;
      return it.kind === 'image'
        ? h('img', { src: it.image_url, alt: '', title: it.brief || '' })
        : h('span', { class: 'mini-hl' }, it.text);
    }));
  }

  function draw() {
    const items = d.items;
    const byTurn = new Map();
    const loose = [];
    for (const it of items) {
      if (it.turn_id) {
        if (!byTurn.has(it.turn_id)) byTurn.set(it.turn_id, []);
        byTurn.get(it.turn_id).push(it);
      } else loose.push(it);
    }
    // One timeline: turns, and what the person added between them.
    const events = [
      ...d.turns.map((t) => ({ at: t.created_at, id: t.id, turn: t })),
      ...groupLoose(loose).map((g) => ({ at: g[0].created_at, id: 0, added: g })),
    ].sort((a, b) => (a.at < b.at ? -1 : a.at > b.at ? 1 : a.id - b.id));
    const kids = [];
    if (!events.length) {
      kids.push(h('div', { class: 'empty chat-empty' },
        h('p', {}, 'Escreva o que quer embaixo e escolha quantas imagens e headlines.'),
        h('p', { class: 'faint' }, 'Depois clique nas que gostou e peça variações delas. Também dá para trazer imagens do computador ou da biblioteca, e escrever suas próprias headlines.')));
    }
    for (const ev of events) {
      if (ev.added) {
        kids.push(h('div', { class: 'msg you added' }, h('div', { class: 'bubble' }, h('span', { class: 'faint' }, 'Você adicionou'))),
          h('div', { class: 'msg out' }, results(ev.added)));
        continue;
      }
      const t = ev.turn;
      const asked = [t.images ? plural(t.images, 'imagem', 'imagens') : '', t.headlines ? plural(t.headlines, 'headline', 'headlines') : ''].filter(Boolean).join(' e ');
      kids.push(h('div', { class: 'msg you' }, h('div', { class: 'bubble' },
        t.picked.length ? pickedChips(t.picked) : null,
        t.prompt ? h('p', {}, t.prompt) : h('p', { class: 'faint' }, 'Variações, sem pedido escrito'),
        h('small', { class: 'faint' }, `${asked} · ${when(t.created_at)}${t.made_by ? ' · ' + t.made_by : ''}`))));
      const its = byTurn.get(t.id) || [];
      const out = h('div', { class: 'msg out' });
      if (t.state === 'failed' && !its.some((it) => it.state === 'done')) out.append(h('div', { class: 'note fail' }, 'Não saiu nada: ' + (t.error || 'falhou')));
      else if (t.state === 'making' && !its.length) out.append(h('div', { class: 'making' }, 'Escrevendo…'));
      out.append(...results(its).filter(Boolean));
      kids.push(out);
    }
    log.replaceChildren(...kids);
    drawComposer();
  }

  // groupLoose puts items added one after another into one message.
  function groupLoose(loose) {
    const groups = [];
    for (const it of loose) {
      const last = groups[groups.length - 1];
      const after = last && d.turns.some((t) => t.created_at > last[last.length - 1].created_at && t.created_at < it.created_at);
      if (last && !after) last.push(it);
      else groups.push([it]);
    }
    return groups;
  }

  function toggle(it) {
    if (picked.includes(it.id)) picked = picked.filter((x) => x !== it.id);
    else picked.push(it.id);
    draw();
  }

  // -- the composer --
  const prompt = h('textarea', { rows: 3, maxlength: 4000, 'aria-label': 'Pedido' });
  prompt.addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); send(); } });
  prompt.value = remember(`create.draft.${id}`);
  prompt.addEventListener('input', () => remember(`create.draft.${id}`, prompt.value));

  function defaultCounts() {
    const all = byID();
    const kinds = new Set(picked.map((p) => all.get(p)?.kind));
    if (kinds.has('image') && !kinds.has('headline')) return { images: 2, headlines: 0 };
    if (kinds.has('headline') && !kinds.has('image')) return { images: 0, headlines: 5 };
    return { images: 2, headlines: 5 };
  }

  function stepper(label, key, max) {
    const c = counts || defaultCounts();
    const out = h('output', { class: 'mono' }, c[key]);
    const set = (v) => {
      counts = { ...(counts || defaultCounts()) };
      counts[key] = Math.max(0, Math.min(max, v));
      drawComposer();
    };
    return h('div', { class: 'stepper', role: 'group', 'aria-label': label },
      h('span', {}, label),
      h('button', { type: 'button', class: 'small ghost', 'aria-label': 'Menos ' + label, onclick: () => set(c[key] - 1) }, '−'),
      out,
      h('button', { type: 'button', class: 'small ghost', 'aria-label': 'Mais ' + label, onclick: () => set(c[key] + 1) }, '+'));
  }

  function drawComposer() {
    const all = byID();
    picked = picked.filter((p) => all.get(p)?.state === 'done');
    const pickedItems = picked.map((p) => all.get(p));
    const pics = pickedItems.filter((it) => it.kind === 'image').length;
    const heads = pickedItems.length - pics;
    prompt.placeholder = pics
      ? 'O que mudar nas imagens escolhidas? Em branco: variações parecidas.'
      : heads ? 'Como variar as headlines escolhidas? (opcional)'
        : 'O que você quer? Ex.: homem de 65 anos na cozinha, colher na mão, luz da manhã.';
    const c = counts || defaultCounts();
    const chips = pickedItems.length ? h('div', { class: 'chips-row' },
      pickedItems.map((it, i) => h('span', { class: 'pchip' }, h('span', { class: 'mono faint' }, i + 1),
        it.kind === 'image' ? h('img', { src: it.image_url, alt: '' }) : h('span', { class: 'pchip-hl' }, it.text),
        h('button', { type: 'button', class: 'ghost small', 'aria-label': 'Tirar', onclick: () => toggle(it) }, '×'))),
      h('button', { type: 'button', class: 'ghost small', onclick: () => { picked = []; draw(); } }, 'Limpar')) : null;

    const label = h('input', { type: 'checkbox', checked: ai });
    label.addEventListener('change', () => { ai = label.checked; drawComposer(); });
    const saveBar = pickedItems.length ? h('div', { class: 'savebar' },
      h('button', { type: 'button', class: 'button', onclick: save }, `Salvar ${pickedItems.length} na biblioteca`),
      h('label', { class: 'check' }, label, 'Imagens feitas com IA'),
      !ai && pics ? h('span', { class: 'warn' }, 'Sem o selo de IA, o Taboola pode reprovar imagens feitas por IA.') : null,
      h('span', { class: 'hint' }, `Vai para ${d.session.vertical_name} › ${d.session.name}`)) : null;

    const upload = h('input', { type: 'file', accept: 'image/jpeg,image/png,image/gif', multiple: true, hidden: true });
    upload.addEventListener('change', async () => {
      for (const f of upload.files) {
        const fd = new FormData();
        fd.append('file', f);
        const it = await run(() => api('POST', `/create/api/sessions/${id}/items`, fd));
        if (it) { d.items.push(it); picked.push(it.id); }
      }
      draw();
      scrollEnd();
    });
    const busy = d.turns.some((t) => t.state === 'making');
    composer.replaceChildren(...[
      chips,
      h('div', { class: 'compose-row' }, prompt,
        h('div', { class: 'compose-side' },
          stepper('Imagens', 'images', r.max_images),
          stepper('Headlines', 'headlines', r.max_headlines),
          h('button', { type: 'button', class: 'primary', disabled: !!status.openai_why || (c.images + c.headlines === 0), onclick: send, title: 'Ctrl+Enter' }, busy ? 'Enviar (fazendo outra)' : 'Enviar'))),
      h('div', { class: 'compose-tools' },
        h('button', { type: 'button', class: 'ghost small', onclick: () => upload.click() }, '+ Imagem do computador'),
        h('button', { type: 'button', class: 'ghost small', onclick: () => fromLibrary() }, '+ Da biblioteca'),
        h('button', { type: 'button', class: 'ghost small', onclick: () => typeHeadline() }, '+ Escrever headline'),
        upload,
        h('span', { class: 'hint' }, 'Clique numa imagem ou headline para escolher. Headlines sempre em inglês.')),
      saveBar].filter(Boolean));
  }

  async function send() {
    const c = counts || defaultCounts();
    const body = { prompt: prompt.value, picked, images: c.images, headlines: c.headlines };
    const t = await run(() => api('POST', `/create/api/sessions/${id}/turns`, body));
    if (!t) return;
    prompt.value = '';
    remember(`create.draft.${id}`, '');
    picked = [];
    counts = null;
    await refresh();
    scrollEnd();
  }

  async function save() {
    const ids = [...picked];
    const v = await run(() => api('POST', `/create/api/sessions/${id}/saves`, { item_ids: ids, ai_label: ai ? 'ai' : 'unset' }));
    if (!v) return;
    toast(`Salvando ${plural(ids.length, 'item', 'itens')} em ${d.session.vertical_name} › ${d.session.name}…`, 'ok');
    picked = [];
    await refresh();
  }

  async function typeHeadline() {
    const input = h('input', { type: 'text', maxlength: 200, placeholder: 'Your headline, in English' });
    const dlg = dialog('Escrever headline', h('form', { method: 'dialog', class: 'stack', onsubmit: async (e) => {
      e.preventDefault();
      const it = await run(() => api('POST', `/create/api/sessions/${id}/items`, { headline: input.value }));
      if (it) { d.items.push(it); picked.push(it.id); dlg.close(); draw(); scrollEnd(); }
    } }, input, h('div', { class: 'actions' }, h('button', { type: 'submit', class: 'primary' }, 'Adicionar'))));
    input.focus();
  }

  async function fromLibrary() {
    const base = '/create/library-api';
    const box = h('div', { class: 'lib-pick' }, h('p', { class: 'faint' }, 'Carregando…'));
    const tabs = h('div', { class: 'segmented' }, [['creatives', 'Imagens'], ['headlines', 'Headlines']].map(([v, l], i) => {
      const inp = h('input', { type: 'radio', name: 'libtab', value: v, checked: i === 0 });
      inp.addEventListener('change', () => load(v));
      return h('label', {}, inp, h('span', {}, l));
    }));
    const dlg = dialog(`Da biblioteca · ${d.session.vertical_name}`, h('div', { class: 'stack' }, tabs, box));
    async function add(body) {
      const it = await run(() => api('POST', `/create/api/sessions/${id}/items`, body));
      if (it) { d.items.push(it); picked.push(it.id); draw(); toast('Adicionado e escolhido', 'ok'); }
    }
    async function load(kind) {
      const q = `vertical=${encodeURIComponent(d.session.vertical_id)}&limit=120`;
      try {
        if (kind === 'creatives') {
          const { creatives } = await api('GET', `${base}/api/creatives?${q}`);
          box.replaceChildren(creatives.length ? h('div', { class: 'thumbs' }, creatives.map((c) => h('figure', { class: 'thumb' },
            h('button', { type: 'button', class: 'cover', onclick: () => add({ library_creative: c.id }) }, h('img', { src: `${base}/thumbs/${c.id}`, alt: c.name, loading: 'lazy' })),
            h('figcaption', {}, c.name)))) : h('div', { class: 'empty' }, 'Nenhuma imagem desta vertical na biblioteca.'));
        } else {
          const { headlines } = await api('GET', `${base}/api/headlines?${q}`);
          box.replaceChildren(headlines.length ? h('div', { class: 'hls' }, headlines.map((x) => h('div', { class: 'hl' },
            h('button', { type: 'button', class: 'hl-pick', title: 'Adicionar', onclick: () => add({ library_headline: x.id, headline: x.text }) }, '+'),
            h('div', { class: 'hl-body' }, x.text)))) : h('div', { class: 'empty' }, 'Nenhuma headline desta vertical na biblioteca.'));
        }
      } catch (e) {
        box.replaceChildren(h('div', { class: 'note fail' }, 'A biblioteca não respondeu: ' + e.message));
      }
    }
    load('creatives');
    return dlg;
  }

  function zoom(it) {
    const all = byID();
    const from = it.from_ids.map((f) => all.get(f)).filter(Boolean);
    dialog(it.angle || 'Imagem', h('div', { class: 'zoom-box' },
      h('img', { src: it.image_url, alt: it.brief || '' }),
      h('dl', {},
        it.brief ? [h('dt', {}, 'O que o modelo recebeu'), h('dd', {}, it.brief)] : null,
        from.length ? [h('dt', {}, 'Feita a partir de'), h('dd', {}, pickedChips(it.from_ids))] : null,
        h('dt', {}, 'Origem'), h('dd', {}, { made: 'Feita no Create', upload: 'Do computador', library: 'Da biblioteca', typed: 'Escrita' }[it.origin] || it.origin),
        it.cost_usd ? [h('dt', {}, 'Custo'), h('dd', {}, money(it.cost_usd))] : null),
      h('div', { class: 'actions' },
        h('button', { type: 'button', class: 'primary', onclick: (e) => { if (!picked.includes(it.id)) toggle(it); e.target.closest('dialog').close(); prompt.focus(); } }, 'Escolher para variar'),
        h('a', { class: 'button', href: it.image_url, download: `create-${it.id}` }, 'Baixar'))), 'wide');
  }

  function scrollEnd() { window.scrollTo({ top: document.body.scrollHeight }); }

  async function refresh() {
    const next = await api('GET', `/create/api/sessions/${id}`).catch(() => null);
    if (!next) return;
    const wasDone = new Set(d.saves.filter((v) => v.state === 'done').map((v) => v.id));
    for (const v of next.saves) {
      if (v.state === 'done' && !wasDone.has(v.id) && d.saves.some((o) => o.id === v.id)) toast(`Salvo na biblioteca: ${next.session.vertical_name} › ${next.session.name}`, 'ok');
      if (v.state === 'failed' && d.saves.some((o) => o.id === v.id && o.state !== 'failed')) toast('Salvar falhou: ' + v.error);
    }
    const finished = d.turns.some((t) => t.state === 'making' && next.turns.some((n) => n.id === t.id && n.state !== 'making'));
    d = next;
    if (finished && column) column.reload();
    drawHead();
    draw();
    poll();
  }

  function poll() {
    const busy = d.turns.some((t) => t.state === 'making') || d.saves.some((v) => v.state === 'waiting' || v.state === 'saving');
    clearTimeout(polling);
    if (busy) polling = setTimeout(refresh, 2000);
  }

  drawHead();
  draw();
  poll();
  scrollEnd();
  prompt.focus();
}

function dialog(title, body, cls = '') {
  const dlg = h('dialog', { class: 'sheet ' + cls },
    h('div', { class: 'spread' }, h('h2', {}, title), h('button', { type: 'button', class: 'ghost small', 'aria-label': 'Fechar', onclick: () => dlg.close() }, '×')),
    body);
  dlg.addEventListener('close', () => dlg.remove());
  dlg.addEventListener('click', (e) => { if (e.target === dlg) dlg.close(); });
  document.body.append(dlg);
  dlg.showModal();
  return dlg;
}

// ---- the library ---------------------------------------------------------------

async function libraryPage(aside) {
  const cats = await getCategories();
  const view = { tab: 'creatives', vertical: remember('create.vertical'), q: '' };
  const body = h('div');
  const detailBox = h('div', { class: 'lib-detail' });

  function drawFilters() {
    const keep = aside.querySelector('.fr-filters-head');
    const sel = verticalSelect(cats, view.vertical);
    sel.firstChild.textContent = 'Todas';
    sel.addEventListener('change', () => { view.vertical = sel.value; load(); });
    const q = h('input', { type: 'text', placeholder: 'Buscar nome ou texto', value: view.q });
    let t;
    q.addEventListener('input', () => { clearTimeout(t); t = setTimeout(() => { view.q = q.value; load(); }, 300); });
    aside.replaceChildren(...[keep].filter(Boolean),
      h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Buscar'), q),
      h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Vertical'), sel));
  }

  const tabs = h('div', { class: 'segmented' }, [['creatives', 'Imagens'], ['headlines', 'Headlines'], ['sets', 'Pastas']].map(([v, l]) => {
    const i = h('input', { type: 'radio', name: 'tab', value: v, checked: view.tab === v });
    i.addEventListener('change', () => { view.tab = v; detailBox.replaceChildren(); load(); });
    return h('label', {}, i, h('span', {}, l));
  }));

  main.append(h('div', { class: 'page-head' },
    h('div', {}, h('h1', {}, 'Biblioteca'), h('p', { class: 'lead' }, 'Toda imagem e headline que o time guarda, a mesma do Launch.')),
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
          h('figcaption', {}, h('b', {}, c.name), c.angle ? ' · ' + c.angle : '')))) : h('div', { class: 'empty' }, 'Nada aqui ainda.'));
      } else if (view.tab === 'headlines') {
        const { headlines } = await api('GET', `${base}/api/headlines?${q}`);
        body.replaceChildren(headlines.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
          h('thead', {}, h('tr', {}, h('th', {}, 'Headline'), h('th', {}, 'Origem'))),
          h('tbody', {}, headlines.map((x) => h('tr', {}, h('td', {}, x.text), h('td', { class: 'muted' }, x.origin))))))
          : h('div', { class: 'empty' }, 'Nada aqui ainda.'));
      } else {
        const { sets } = await api('GET', `${base}/api/sets?${q}`);
        body.replaceChildren(sets.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
          h('thead', {}, h('tr', {}, h('th', {}, 'Pasta'), h('th', { class: 'num' }, 'Imagens'), h('th', { class: 'num' }, 'Headlines'), h('th', {}, 'Por'), h('th', {}, ''))),
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
        h('dt', {}, 'Origem'), h('dd', {}, { create: 'Feito no Create', upload: 'Enviado', drive: 'Adicionado na pasta' }[c.origin] || c.origin),
        h('dt', {}, 'Selo de IA'), h('dd', {}, { ai: 'Feito com IA', not_ai: 'Não é IA', unset: 'Não marcado' }[c.ai_label] || c.ai_label),
        h('dt', {}, 'Ideia'), h('dd', {}, c.idea || '—'),
        h('dt', {}, 'Tamanho'), h('dd', {}, `${c.width} × ${c.height} · ${(c.bytes / 1048576).toFixed(1).replace('.', ',')} MB`)),
      h('div', { class: 'actions' },
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
      h('li', {}, 'Imagens e headlines feitas com a OpenAI. Headlines sempre em inglês.'),
      h('li', {}, 'Pessoas espontâneas, sem olhar para a câmera, foto com cara de real, a menos que o pedido diga outra coisa.'),
      h('li', {}, 'Sem texto, logo, antes e depois, celebridades ou close de partes do corpo nas imagens.'),
      h('li', {}, 'Headlines de 34 a 45 caracteres (nunca mais de 60), sem palavras em maiúsculas, sem "!!", sintomas e não doenças, sem promessa de cura, sem valores.'),
      h('li', {}, `Até ${r.max_images} imagens e ${r.max_headlines} headlines por pedido, a partir de até ${r.max_picked} escolhidas.`),
      h('li', {}, 'Nada vai para a biblioteca até você salvar. O selo de IA é escolha sua ao salvar; o Create avisa se estiver desligado.'))));
  main.append(h('section', { class: 'panel' }, h('h2', {}, `Palavras que o Taboola já bloqueou para o time (${r.blocked.length})`),
    h('ul', { class: 'words' }, r.blocked.map((b) => h('li', {}, h('b', {}, b.text),
      b.description ? h('span', { class: 'muted' }, ' · também em descrições') : null,
      b.alternatives && b.alternatives.length ? h('span', { class: 'muted' }, ' → ' + b.alternatives.join(', ')) : null)))));
}

boot().catch((e) => { main.replaceChildren(h('div', { class: 'note fail' }, 'Não carregou: ' + e.message)); });
