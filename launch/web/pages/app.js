// Launch's pages. Every page path gets index.html; route (route.js) says
// which page the path names and this draws it inside the Frame
// (shared/frame): Campanhas (groups, campaigns and ads in one table, a
// campaign open on the right), the new-item steps behind + Novo, and Contas
// (the Taboola accounts Launch uses). Pedidos, Histórico, Presets and
// Rascunhos are no longer screens (IMPLEMENT d587e1b829); their addresses
// open Campanhas.
import { mountFrame } from '/launch/_frame/frame.js';
import { api, h, note } from './lib.js';
import { manage, where, href } from './manage.js';
import { newPair } from './newpair.js';
import { accounts } from './accounts.js';
import { route, oldAddress } from './route.js';

// TABS: Campanhas keeps the account and period the person picked.
function tabs() {
  const at = where(location.search);
  return [
    { id: 'campaigns', label: 'Campanhas', href: href({ account: at.account, w: at.w }) },
    { id: 'accounts', label: 'Contas', href: '/launch/accounts' },
  ];
}

async function start() {
  const r = route(location.pathname);
  if (r.page === 'tree') {
    location.replace(oldAddress(r));
    return;
  }
  if (r.page === 'old') {
    location.replace('/launch/campaigns' + location.search);
    return;
  }
  let status = { user: '', networks: [], limits: {} };
  try {
    status = await api('status');
  } catch (e) {
    status.error = e.message;
  }
  // Only pages with something to filter keep the left column.
  if (!['manage', 'campaign'].includes(r.page)) document.getElementById('filters').remove();
  // The new-item steps put their own column there.
  if (r.page === 'new') document.body.classList.add('launch-steps');
  mountFrame({
    app: 'launch',
    tabs: tabs(),
    active: r.tab,
    user: status.user,
    searchLabel: 'Buscar campanha',
    search: async (q) => {
      const res = await api('search?q=' + encodeURIComponent(q));
      return res.results;
    },
  });
  const main = document.getElementById('page');
  const aside = document.getElementById('filters') || h('aside');
  main.replaceChildren();
  if (status.error) main.append(note('fail', status.error));
  const ctx = { main, aside, status, route: r };
  try {
    switch (r.page) {
      case 'manage': await manage(ctx); break;
      case 'campaign': await manage(ctx); break;
      case 'new': await newPair(ctx); break;
      case 'accounts': await accounts(ctx); break;
      default:
        main.append(h('h1', {}, 'Página não encontrada'), h('p', {}, h('a', { href: '/launch/campaigns' }, 'Voltar para as campanhas')));
    }
  } catch (e) {
    main.append(note('fail', e.message));
  }
}

if (typeof document !== 'undefined') start();
