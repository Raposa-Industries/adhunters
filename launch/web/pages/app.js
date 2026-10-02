// Launch's pages. Every page path gets index.html; route draws the page the
// path names inside the Frame (shared/frame): Campanhas (groups, campaigns
// and ads in one table, a campaign open on the right), the new-item steps,
// presets, History, drafts, other services' requests and the Taboola logins
// (Contas).
import { mountFrame } from '/launch/_frame/frame.js';
import { api, h, note } from './lib.js';
import { manage, where, href } from './manage.js';
import { newPair, drafts } from './newpair.js';
import { presets } from './presets.js';
import { history } from './history.js';
import { requests, request } from './requests.js';
import { accounts } from './accounts.js';

// TABS: Campanhas keeps the account and period the person picked. Presets
// has no tab; the new-item steps link to it.
function tabs() {
  const at = where(location.search);
  return [
    { id: 'campaigns', label: 'Campanhas', href: href({ account: at.account, w: at.w }) },
    { id: 'requests', label: 'Pedidos', href: '/launch/requests' },
    { id: 'history', label: 'Histórico', href: '/launch/history' },
    { id: 'drafts', label: 'Rascunhos', href: '/launch/drafts' },
    { id: 'accounts', label: 'Contas', href: '/launch/accounts' },
  ];
}

// route reads a path into a page and its parts. /launch/groups and
// /launch/ads, the tables before batch 126, are Campanhas now.
export function route(pathname) {
  const parts = pathname.replace(/^\/launch\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
  const [first] = parts;
  if (!first) return { page: 'manage', tab: 'campaigns' };
  if (first === 'campaigns' && parts.length === 1) return { page: 'manage', tab: 'campaigns' };
  if ((first === 'groups' || first === 'ads') && parts.length === 1) return { page: 'old', tab: 'campaigns' };
  if (first === 'new' && parts.length === 1) return { page: 'new', tab: 'campaigns' };
  if (['presets', 'history', 'drafts', 'requests', 'accounts'].includes(first) && parts.length === 1) {
    return { page: first, tab: first };
  }
  if (first === 'requests' && parts.length === 2 && /^\d+$/.test(parts[1])) return { page: 'request', tab: 'requests', id: parts[1] };
  // /<net>/<account>[/g/<group>][/c/<campaign>]: a campaign has its own page;
  // an account or a group is now the Campaigns table narrowed to it.
  const r = { page: 'tree', tab: 'campaigns', net: parts[0], account: parts[1] };
  if (!r.account) return { page: 'missing', tab: 'campaigns' };
  for (let i = 2; i < parts.length; i += 2) {
    const v = parts[i + 1];
    if (!v) return { page: 'missing', tab: 'campaigns' };
    if (parts[i] === 'g') r.group = v;
    else if (parts[i] === 'c') r.campaign = v;
    else return { page: 'missing', tab: 'campaigns' };
  }
  if (r.campaign) r.page = 'campaign';
  return r;
}

// oldAddress is where an account or group link from before the tables goes.
export function oldAddress(r) {
  const q = new URLSearchParams({ account: r.account });
  if (r.group) q.set('group', r.group);
  return '/launch/campaigns?' + q;
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
  if (!['manage', 'campaign', 'history'].includes(r.page)) document.getElementById('filters').remove();
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
      case 'presets': await presets(ctx); break;
      case 'history': await history(ctx); break;
      case 'drafts': await drafts(ctx); break;
      case 'requests': await requests(ctx); break;
      case 'request': await request(ctx); break;
      case 'accounts': await accounts(ctx); break;
      default:
        main.append(h('h1', {}, 'Página não encontrada'), h('p', {}, h('a', { href: '/launch/campaigns' }, 'Voltar para as campanhas')));
    }
  } catch (e) {
    main.append(note('fail', e.message));
  }
}

if (typeof document !== 'undefined') start();
