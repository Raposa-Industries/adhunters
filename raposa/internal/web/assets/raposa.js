// Raposa's pages in the Frame. The server draws each page; this mounts the
// Frame around it (top bar, app selector, tabs, ⌘K, account) and wires the
// few things a page does in the browser: remembering the name people type,
// the burned lines filters, the print button and the section list.
import { mountFrame } from '../_frame/frame.js';
import { rank } from '../_frame/core.js';

const body = document.body;
const base = body.dataset.base || '';

function store(key, value) {
  try {
    if (value === undefined) return localStorage.getItem(key);
    localStorage.setItem(key, value);
  } catch {
    // Private windows and blocked storage: the name is just not remembered.
  }
  return null;
}

// ⌘K finds what the page lists (its rows carry data-search), and a number
// opens that investigation.
function search(q) {
  const rows = [...document.querySelectorAll('tr[data-search]')].map((r) => ({
    title: r.dataset.title || r.textContent.trim(),
    words: r.dataset.search,
    href: r.dataset.href || '#' + r.id,
  }));
  const found = rank(rows, q);
  if (/^\d+$/.test(q.trim())) found.unshift({ title: 'Investigação ' + q.trim(), sub: 'Abrir a investigação', href: base + '/i/' + q.trim() });
  return found.slice(0, 20);
}

mountFrame({
  app: 'raposa',
  tabs: [
    { id: 'investigations', label: 'Investigações', href: base + '/' },
    { id: 'cloaked', label: 'Anúncios com cloak', href: base + '/cloaked' },
    { id: 'burns', label: 'Linhas queimadas', href: base + '/burns' },
  ],
  active: body.dataset.tab,
  user: body.dataset.user || undefined,
  ready: { raposa: true },
  searchLabel: body.dataset.searchLabel,
  search: async (q) => search(q),
});

// The name a person types is remembered in this browser and filled in the
// next form: every form that asks for it says who asked.
const name = store('raposa.name') || '';
for (const input of document.querySelectorAll('input[data-name]')) {
  if (!input.value) input.value = name;
  input.form?.addEventListener('submit', () => {
    const typed = input.form.querySelector('input[data-name]:not([type=hidden])');
    if (typed && typed.value.trim()) store('raposa.name', typed.value.trim());
  });
}

for (const b of document.querySelectorAll('[data-print]')) b.addEventListener('click', () => window.print());

// Burned lines: the filters in the left column hide the rows they leave out.
const filters = document.getElementById('burn-filters');
const table = document.getElementById('burns');
if (filters && table) {
  const rows = [...table.querySelectorAll('tbody tr[data-site]')];
  rows.forEach((r, i) => { r.id = 'linha-' + (i + 1); });
  const none = table.querySelector('tr.none');
  const count = document.querySelector('[data-count]');
  const title = document.querySelector('[data-reach-title]');
  const apply = () => {
    const f = new FormData(filters);
    const site = String(f.get('site') || '').trim().toLowerCase();
    const rung = String(f.get('rung') || '');
    const reach = String(f.get('reach') || '');
    let shown = 0;
    for (const r of rows) {
      const ok = (!site || r.dataset.site.toLowerCase().includes(site))
        && (!rung || r.dataset.rung === rung)
        && (!reach || (reach === 'general') === (r.dataset.general === 'true'));
      r.hidden = !ok;
      if (ok) shown++;
    }
    if (none) none.hidden = shown > 0 || rows.length === 0;
    if (count) count.textContent = shown + ' ' + (shown === 1 ? count.dataset.one : count.dataset.many);
    if (title) title.textContent = { '': 'Todas', site: 'Só um site', general: 'Em geral' }[reach];
  };
  filters.addEventListener('input', apply);
  filters.addEventListener('submit', (e) => e.preventDefault());
}

// The section list on an investigation marks the section in view.
const nav = [...document.querySelectorAll('.rail-nav a[href^="#"]')];
if (nav.length && 'IntersectionObserver' in window) {
  const mark = (id) => nav.forEach((a) => {
    if (a.getAttribute('href') === '#' + id) a.setAttribute('aria-current', 'true');
    else a.removeAttribute('aria-current');
  });
  const seen = new Map();
  const io = new IntersectionObserver((entries) => {
    for (const e of entries) seen.set(e.target.id, e.isIntersecting);
    const first = nav.map((a) => a.getAttribute('href').slice(1)).find((id) => seen.get(id));
    if (first) mark(first);
  }, { rootMargin: '-48px 0px -60% 0px' });
  for (const a of nav) {
    const s = document.getElementById(a.getAttribute('href').slice(1));
    if (s) io.observe(s);
  }
}
