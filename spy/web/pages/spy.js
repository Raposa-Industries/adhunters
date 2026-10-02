// What every Spy page shares: the Frame with Spy's tabs and ⌘K, the API,
// the range picker, and the small drawings (sparklines, bars) and number
// formats the pages use. Every page builds its content with h() and text
// nodes, never from markup, so nothing an ad says can run.

import { mountFrame, h } from '/spy/_frame/frame.js';

export { h };

const TABS = [
  { id: 'ads', label: 'Anúncios', href: '/spy/' },
  { id: 'operators', label: 'Operadores', href: '/spy/operators/' },
  { id: 'publishers', label: 'Publishers', href: '/spy/publishers/' },
  { id: 'pulse', label: 'Mercado', href: '/spy/pulse/' },
];

// frame mounts the Frame with this page's tab active.
export function frame(active) {
  mountFrame({
    app: 'spy',
    tabs: TABS,
    active,
    ready: { spy: true },
    search,
    searchLabel: 'Buscar anúncios, operadores, publishers',
  });
}

async function search(q) {
  const r = await api('search', { q });
  return [
    ...r.ads.map((a) => ({ title: a.headline || 'Anúncio ' + a.id, sub: [a.brand, a.operator_name].filter(Boolean).join(' · '), href: '/spy/ads/' + a.id, img: a.image_url })),
    ...r.operators.map((o) => ({ title: o.name, sub: 'Operador ' + (o.code || '') + (o.live_creatives_count != null ? ' · ' + o.live_creatives_count + ' no ar' : ''), href: '/spy/operators/' + o.id })),
    ...r.publishers.map((p) => ({ title: p.name, sub: 'Publisher · ' + (p.domain || ''), href: '/spy/publishers/' + p.id })),
    ...r.verticals.map((v) => ({ title: v.name, sub: 'Vertical · ' + v.category_name, href: '/spy/?vertical=' + encodeURIComponent(v.id) })),
  ];
}

// api calls /spy/api/<path> with the page's range and the given params.
export async function api(path, params = {}, opts = {}) {
  const u = new URL('/spy/api/' + path, location.origin);
  for (const [k, v] of Object.entries(params)) {
    if (v == null || v === '') continue;
    for (const x of [].concat(v)) u.searchParams.append(k, x);
  }
  const res = await fetch(u, { headers: { Accept: 'application/json' }, ...opts });
  let body = null;
  try { body = await res.json(); } catch { /* an empty or broken answer */ }
  if (!res.ok) throw new Error((body && body.error) || 'Falhou (' + res.status + ')');
  return body;
}

// params are the page's own query parameters.
export function params() {
  return new URLSearchParams(location.search);
}

// setParams changes the address without reloading.
export function setParams(p) {
  const s = p.toString();
  history.replaceState(null, '', location.pathname + (s ? '?' + s : ''));
}

// rangeParams are the from/to of the page's range (none: the last 24 hours;
// from=48h: the last 48 hours), and vs when it is compared with the period
// just before rather than the usual weeks.
export function rangeParams() {
  const p = params();
  return { from: p.get('from') || '', to: p.get('to') || '', vs: p.get('vs') || '' };
}

const RANGES = [
  { id: '', label: '24 h' },
  { id: '48h', label: '48 h' },
  { id: '72h', label: '72 h' },
  { id: '7', label: '7 dias' },
  { id: '30', label: '30 dias' },
  { id: '90', label: '90 dias' },
];

// Dates are São Paulo days, as the API reads them.
const spDate = new Intl.DateTimeFormat('en-CA', { year: 'numeric', month: '2-digit', day: '2-digit', timeZone: 'America/Sao_Paulo' });
function isoDay(d) {
  return spDate.format(d);
}

// rangePicker draws the range choice; onchange runs after the address changes.
export function rangePicker(onchange) {
  const p = params();
  let current = '';
  const f = p.get('from') || '';
  if (/^\d+h$/.test(f) && !p.get('to')) {
    current = RANGES.some((r) => r.id === f) ? f : 'custom';
  } else if (f) {
    const days = Math.round((Date.parse(p.get('to') || isoDay(new Date())) - Date.parse(f)) / 864e5) + 1;
    current = RANGES.some((r) => r.id === String(days)) && !p.get('to') ? String(days) : 'custom';
  }
  const from = h('input', { type: 'date', value: /^\d+h$/.test(f) ? '' : f, 'aria-label': 'De' });
  const to = h('input', { type: 'date', value: p.get('to') || '', 'aria-label': 'Até' });
  const custom = h('span', { class: 'sp-custom', hidden: current !== 'custom' }, from, to,
    h('button', { type: 'button', class: 'small', onclick: () => apply('custom') }, 'Ver'));
  function apply(id) {
    const q = params();
    q.delete('offset');
    if (id === '') { q.delete('from'); q.delete('to'); }
    else if (id.endsWith('h')) { q.set('from', id); q.delete('to'); }
    else if (id === 'custom') {
      if (!from.value) return;
      q.set('from', from.value);
      if (to.value) q.set('to', to.value); else q.delete('to');
    } else {
      const d = new Date(Date.now() - (Number(id) - 1) * 864e5);
      q.set('from', isoDay(d));
      q.delete('to');
    }
    setParams(q);
    onchange();
  }
  const seg = h('div', { class: 'segmented', role: 'radiogroup', 'aria-label': 'Período' },
    [...RANGES, { id: 'custom', label: 'Outro' }].map((r) => h('label', {},
      h('input', { type: 'radio', name: 'range', value: r.id, checked: r.id === current, onchange: () => {
        custom.hidden = r.id !== 'custom';
        if (r.id !== 'custom') apply(r.id);
      } }),
      h('span', {}, r.label))));
  return h('div', { class: 'sp-range' }, seg, custom);
}

// comparePicker chooses what momentum compares the range with: the same
// hours of the usual weeks, or the period of the same length just before.
export function comparePicker(onchange) {
  return h('label', { class: 'sp-inline', title: 'Contra o usual: com o que a presença do período é comparada' }, 'Comparar com',
    h('select', { 'aria-label': 'Comparar com', onchange: (e) => {
      const q = params();
      if (e.target.value) q.set('vs', e.target.value); else q.delete('vs');
      q.delete('offset');
      setParams(q);
      onchange();
    } },
    h('option', { value: '', selected: !params().get('vs') }, 'as semanas usuais'),
    h('option', { value: 'before', selected: params().get('vs') === 'before' }, 'o período anterior')));
}

// sortPicker is the sort choice and a button that turns it round (Z to A
// for names); onchange runs after the address changes.
export function sortPicker(sorts, def, onchange) {
  const cur = params().get('sort') || def;
  const rev = h('button', { type: 'button', class: 'ghost small', title: 'Inverter a ordem' }, '');
  const label = () => {
    const on = params().get('rev') === '1';
    const byName = (params().get('sort') || def) === 'name';
    rev.textContent = byName ? (on ? 'Z–A' : 'A–Z') : (on ? '↑ invertido' : '↓');
    rev.setAttribute('aria-pressed', String(on));
  };
  const change = (name, value) => {
    const q = params();
    if (!value) q.delete(name); else q.set(name, value);
    q.delete('offset');
    setParams(q);
    label();
    onchange();
  };
  rev.addEventListener('click', () => change('rev', params().get('rev') === '1' ? '' : '1'));
  const sel = h('select', { 'aria-label': 'Ordenar', onchange: (e) => change('sort', e.target.value === def ? '' : e.target.value) },
    sorts.map(([v, l]) => h('option', { value: v, selected: v === cur }, l)));
  label();
  return h('span', { class: 'sp-inline' }, sel, rev);
}

// post sends JSON to /spy/api/<path> and returns the answer.
export function post(path, body) {
  return api(path, {}, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  });
}

// load shows a waiting note while f runs (a range other than the last 24
// hours is counted when asked and can take a while), then its result or
// the error in place of the note.
export async function load(into, f) {
  const slow = rangeParams().from !== '' || rangeParams().to !== '' || rangeParams().vs !== '';
  into.replaceChildren(h('p', { class: 'muted sp-wait' }, slow ? 'Contando este período… pode levar alguns segundos.' : 'Carregando…'));
  try {
    const out = await f();
    return out;
  } catch (e) {
    into.replaceChildren(h('p', { class: 'note fail' }, e.message));
    return null;
  }
}

// ---- formats ----

const nf = new Intl.NumberFormat('pt-BR');
export function num(v, digits = 0) {
  if (v == null || v === '') return '–';
  const n = Number(v);
  if (!isFinite(n)) return '–';
  return digits ? n.toLocaleString('pt-BR', { minimumFractionDigits: digits, maximumFractionDigits: digits }) : nf.format(Math.round(n));
}

export function pct(v, digits = 1) {
  return v == null ? '–' : num(v, digits) + '%';
}

// presence is sightings per 100 checks of the network.
export function presence(v) {
  return v == null ? '–' : num(v, Number(v) < 10 ? 2 : 1);
}

// momentum is the ratio to the usual value (1 = no change), shown as a change.
export function momentum(v) {
  if (v == null) return '–';
  const c = (Number(v) - 1) * 100;
  return (c > 0 ? '+' : '') + num(c, 0) + '%';
}

export function money(v) {
  return v == null ? '–' : '$' + num(v, 3);
}

const df = new Intl.DateTimeFormat('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', timeZone: 'America/Sao_Paulo' });
// Days are UTC days, as the daily numbers are.
const dd = new Intl.DateTimeFormat('pt-BR', { day: '2-digit', month: '2-digit', year: '2-digit', timeZone: 'UTC' });
export function when(v) {
  return v ? df.format(new Date(v)) : '–';
}
export function day(v) {
  return v ? dd.format(new Date(v)) : '–';
}

// A range's dates are São Paulo days.
const spDay = new Intl.DateTimeFormat('pt-BR', { day: '2-digit', month: '2-digit', year: '2-digit', timeZone: 'America/Sao_Paulo' });
export function windowText(w) {
  if (!w) return '';
  const vs = w.vs === 'before' ? ', contra o período anterior' : '';
  if (w.recent) return 'Últimas 24 horas, até ' + when(w.to) + vs;
  if (w.hours) return 'Últimas ' + w.hours + ' horas, até ' + when(w.to) + vs;
  return spDay.format(new Date(w.from)) + ' a ' + spDay.format(new Date(Date.parse(w.to) - 1)) + vs;
}

const WORDS = { rising: 'subindo', steady: 'estável', fading: 'caindo', stopped: 'parou', unclear: 'incerto', too_little: 'poucos dados', new: 'novo' };
export function word(w) {
  return WORDS[w] || w || '–';
}

// direction is a badge with the reason as its tooltip.
export function direction(d, text) {
  if (!d) return null;
  const cls = { rising: 'running', fading: 'paused', stopped: 'rejected', steady: 'review' }[d] || '';
  return h('span', { class: 'badge ' + cls, title: text || '' }, word(d));
}

// ---- drawings ----

const SVG = 'http://www.w3.org/2000/svg';
function svg(tag, attrs) {
  const el = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  return el;
}

// spark draws a line of values (nulls are days nobody looked).
export function spark(values, w = 120, hgt = 28) {
  const s = svg('svg', { viewBox: `0 0 ${w} ${hgt}`, width: w, height: hgt, class: 'sp-spark', 'aria-hidden': 'true' });
  const vs = (values || []).map((v) => (v == null ? null : Number(v)));
  const max = Math.max(0, ...vs.filter((v) => v != null));
  if (vs.length < 2 || max === 0) {
    s.append(svg('line', { x1: 0, x2: w, y1: hgt - 1, y2: hgt - 1, class: 'sp-spark-flat' }));
    return s;
  }
  const step = w / (vs.length - 1);
  let d = '';
  vs.forEach((v, i) => {
    if (v == null) return;
    const x = (i * step).toFixed(1);
    const y = (hgt - 2 - (v / max) * (hgt - 4)).toFixed(1);
    d += (d && vs[i - 1] != null ? 'L' : 'M') + x + ' ' + y;
  });
  s.append(svg('path', { d, class: 'sp-spark-line' }));
  return s;
}

// bars draws labelled bars: [{ label, value }].
export function bars(items, fmt = num) {
  const max = Math.max(0, ...items.map((i) => Number(i.value) || 0));
  return h('div', { class: 'sp-bars' }, items.map((i) => h('div', { class: 'sp-bar' },
    h('span', { class: 'sp-bar-label' }, i.label),
    h('span', { class: 'sp-bar-track' }, h('span', { class: 'sp-bar-fill', style: `width:${max ? (100 * (Number(i.value) || 0) / max).toFixed(1) : 0}%` })),
    h('span', { class: 'num' }, fmt(i.value)))));
}

// dayBars draws one bar per day: rows are { day, value, title }. Days with
// no value (nobody looked) are gaps.
export function dayBars(rows) {
  const max = Math.max(0, ...rows.map((r) => Number(r.value) || 0));
  const first = rows.length ? day(rows[0].day) : '';
  const last = rows.length ? day(rows[rows.length - 1].day) : '';
  return h('div', {},
    h('div', { class: 'sp-days', role: 'img', 'aria-label': 'Por dia, ' + first + ' a ' + last },
      rows.map((r) => h('span', { title: r.title, class: r.value == null ? 'none' : null,
        style: `height:${max && r.value != null ? Math.max(1, 100 * Number(r.value) / max).toFixed(1) : 0}%` }))),
    h('div', { class: 'sp-days-axis' }, h('span', {}, first), h('span', {}, last)));
}

// table draws a list: cols are [header, (row) => cell, 'num'?].
export function table(cols, rows, empty = 'Nada neste período.') {
  if (!rows || rows.length === 0) return h('p', { class: 'empty' }, empty);
  return h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
    h('thead', {}, h('tr', {}, cols.map(([t, , c]) => h('th', { class: c || null }, t)))),
    h('tbody', {}, rows.map((r) => h('tr', {}, cols.map(([, f, c]) => h('td', { class: c || null }, f(r))))))));
}

// stats draws a row of labelled numbers: [[label, value]].
export function stats(pairs) {
  return h('dl', { class: 'sp-stats' }, pairs.filter(Boolean).map(([k, v]) => h('div', {}, h('dt', {}, k), h('dd', { class: 'num' }, v))));
}

// fill replaces an element's children, skipping the ones left out (null).
export function fill(el, ...kids) {
  el.replaceChildren(...kids.flat().filter((k) => k != null && k !== false));
}

const KINDS = { direct: 'direto', affiliate: 'afiliado', arbitrage: 'arbitragem' };
export function kind(k) {
  return KINDS[k] || k;
}

export function link(href, text) {
  return h('a', { href }, text);
}

// keepRange is the page's range as a query string, for links to other pages.
export function keepRange() {
  const r = rangeParams();
  const p = new URLSearchParams();
  if (r.from) p.set('from', r.from);
  if (r.to) p.set('to', r.to);
  if (r.vs) p.set('vs', r.vs);
  const s = p.toString();
  return s ? '?' + s : '';
}
