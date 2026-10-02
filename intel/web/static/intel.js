// Intel's pages in the Frame. The server draws the page; this mounts the
// Frame around it and wires ⌘K to campaign search.
import { mountFrame } from '../_frame/frame.js';

const body = document.body;
mountFrame({
  app: 'intel',
  tabs: [
    { id: 'home', label: 'Visão geral', href: '/intel/' },
    { id: 'suggestions', label: 'Sugestões', href: '/intel/suggestions' },
    { id: 'alerts', label: 'Alertas', href: '/intel/alerts' },
  ],
  active: body.dataset.tab,
  user: body.dataset.user || undefined,
  ready: { intel: true },
  searchLabel: 'Buscar campanhas',
  search: async (q) => {
    const r = await fetch('/intel/api/search?q=' + encodeURIComponent(q));
    return r.ok ? r.json() : [];
  },
});

// The overview's campaign table: an account's caret opens and closes its
// campaigns.
document.addEventListener('click', (e) => {
  const caret = e.target.closest('.nested-accounts .row-account .caret');
  if (!caret) return;
  const body = caret.closest('tbody.account');
  const open = body.classList.toggle('closed') === false;
  const name = body.dataset.account;
  caret.classList.toggle('open', open);
  caret.setAttribute('aria-expanded', String(open));
  caret.setAttribute('aria-label', (open ? 'Fechar a conta ' : 'Abrir a conta ') + name);
});
