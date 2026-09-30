// Helpers every Launch page uses: the API, money and dates in pt-BR, and
// the small parts pages are built from. Pages build elements with h, so a
// person's text is always text, never markup.
import { h } from '/launch/_frame/frame.js';

export { h };

export const API = '/launch/api/';

// api calls Launch's API and returns its JSON; an error carries the
// server's pt-BR line.
export async function api(path, opts = {}) {
  const init = { method: opts.method || 'GET', headers: {} };
  if (opts.form) {
    init.body = opts.form;
  } else if (opts.body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(opts.body);
  }
  let res;
  try {
    res = await fetch(API + path, init);
  } catch {
    throw new Error('sem conexão com o Launch; tente de novo');
  }
  let data = null;
  try {
    data = await res.json();
  } catch {
    // An answer without JSON is a proxy's or the sign-in's page.
  }
  if (!res.ok) {
    const e = new Error(data?.error || `o Launch respondeu ${res.status}`);
    e.status = res.status;
    throw e;
  }
  return data;
}

const brl = new Intl.NumberFormat('pt-BR', { minimumFractionDigits: 2, maximumFractionDigits: 3 });

// money shows USD the way the team reads it: US$ 0,35.
export function money(usd) {
  if (!usd) return '—';
  return 'US$ ' + brl.format(usd);
}

const when = new Intl.DateTimeFormat('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' });

export function date(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  return isNaN(d) ? '' : when.format(d);
}

export function plural(n, one, many) {
  return `${n} ${n === 1 ? one : many}`;
}

// state names and badges. Taboola's status words, shown in Portuguese.
const STATES = {
  RUNNING: ['Rodando', 'running'],
  PAUSED: ['Pausada', 'paused'],
  PENDING_APPROVAL: ['Em revisão', 'review'],
  PENDING_START_DATE: ['Aguardando início', 'review'],
  REJECTED: ['Recusada', 'rejected'],
  DEPLETED: ['Sem orçamento', 'rejected'],
  DEPLETED_MONTHLY: ['Sem orçamento no mês', 'rejected'],
  EXPIRED: ['Encerrada', ''],
  TERMINATED: ['Encerrada', ''],
  FROZEN: ['Congelada', 'rejected'],
  CRAWLING: ['Em análise', 'review'],
  APPROVED: ['Aprovado', 'running'],
  PENDING: ['Em revisão', 'review'],
  STOPPED: ['Parado', 'paused'],
};

export function stateName(s) {
  return STATES[s]?.[0] || (s ? s.toLowerCase().replace(/_/g, ' ') : '—');
}

export function badge(s) {
  const [label, cls] = STATES[s] || [stateName(s), ''];
  return h('span', { class: 'badge ' + cls }, label);
}

export const DEVICES = { desktop: 'Desktop', mobile: 'Mobile', both: 'Vários' };

// path builds a Launch link: path('taboola', acct, group, campaign).
export function link(net, acct, group, campaign) {
  let p = `/launch/${net}/${encodeURIComponent(acct)}`;
  if (group) p += `/g/${encodeURIComponent(group)}`;
  if (campaign) p += `/c/${encodeURIComponent(campaign)}`;
  return p;
}

// crumbs is the trail at the top of a page: [[label, href], …, [label]].
export function crumbs(list) {
  const out = h('nav', { class: 'crumbs', 'aria-label': 'Onde você está' });
  list.forEach(([label, href], i) => {
    if (i) out.append(h('span', { class: 'sep' }, '/'));
    out.append(href ? h('a', { href }, label) : h('span', { 'aria-current': 'page' }, label));
  });
  return out;
}

export function note(kind, ...text) {
  return h('div', { class: 'note ' + (kind || ''), role: kind === 'fail' ? 'alert' : null }, ...text);
}

// field wraps a control with its label and an optional hint.
export function field(label, control, hint) {
  return h('label', { class: 'field' }, label, control, hint ? h('span', { class: 'hint' }, hint) : null);
}

export function input(attrs) {
  return h('input', { type: 'text', ...attrs });
}

export function select(options, value, attrs = {}) {
  return h('select', attrs, options.map(([v, label]) => h('option', { value: v, selected: String(v) === String(value ?? '') }, label)));
}

// segmented is one choice of a few: segmented('mode', [['a','A'],…], 'a', onchange).
export function segmented(name, options, value, onchange) {
  return h('div', { class: 'segmented', role: 'radiogroup' }, options.map(([v, label]) =>
    h('label', {}, h('input', { type: 'radio', name, value: v, checked: v === value, onchange: () => onchange(v) }), h('span', {}, label))));
}

// busy runs work while button shows it is running, and shows an error line
// in out (an element) when it fails.
export async function busy(button, out, work) {
  const was = button.textContent;
  button.disabled = true;
  button.textContent = 'Aguarde…';
  if (out) out.replaceChildren();
  try {
    return await work();
  } catch (e) {
    if (out) out.replaceChildren(note('fail', e.message));
    else alert(e.message);
    return undefined;
  } finally {
    button.disabled = false;
    button.textContent = was;
  }
}

// numberOf reads a pt-BR or en number from a field: "0,35" and "0.35".
export function numberOf(text) {
  const t = String(text ?? '').trim().replace(/\s/g, '');
  if (!t) return 0;
  const n = Number(t.includes(',') ? t.replace(/\./g, '').replace(',', '.') : t);
  return Number.isFinite(n) ? n : NaN;
}

export function store(key, value) {
  try {
    if (value === undefined) return JSON.parse(localStorage.getItem(key) || 'null');
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // Private windows: the page just forgets.
  }
  return null;
}
