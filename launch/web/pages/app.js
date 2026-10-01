// Launch's pages. Every page path gets index.html; route draws the page the
// path names inside the Frame (shared/frame): the Campaign Groups, Campaigns
// and Ads tables, a campaign, the new-item steps, presets, History, drafts
// and other services' requests.
import { mountFrame } from '/launch/_frame/frame.js';
import { api, h, note } from './lib.js';
import { manage, where, href } from './manage.js';
import { campaign } from './campaign.js';
import { newPair, drafts } from './newpair.js';
import { presets } from './presets.js';
import { history } from './history.js';
import { requests, request } from './requests.js';

// TABS are Realize's three levels, then Launch's own pages. The three levels
// keep the account and period the person picked.
function tabs() {
  const at = where(location.search);
  const keep = { account: at.account, w: at.w };
  return [
    { id: 'groups', label: 'Grupos de campanha', href: href('groups', keep) },
    { id: 'campaigns', label: 'Campanhas', href: href('campaigns', keep) },
    { id: 'ads', label: 'Anúncios', href: href('ads', keep) },
    { id: 'requests', label: 'Pedidos', href: '/launch/requests' },
    { id: 'history', label: 'Histórico', href: '/launch/history' },
    { id: 'presets', label: 'Presets', href: '/launch/presets' },
    { id: 'drafts', label: 'Rascunhos', href: '/launch/drafts' },
  ];
}

const LEVEL_PAGES = ['groups', 'campaigns', 'ads'];

// route reads a path into a page and its parts.
export function route(pathname) {
  const parts = pathname.replace(/^\/launch\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
  const [first] = parts;
  if (!first) return { page: 'manage', tab: 'campaigns', level: 'campaigns' };
  if (LEVEL_PAGES.includes(first) && parts.length === 1) return { page: 'manage', tab: first, level: first };
  if (first === 'new' && parts.length === 1) return { page: 'new', tab: 'campaigns' };
  if (['presets', 'history', 'drafts', 'requests'].includes(first) && parts.length === 1) {
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
  let status = { user: '', networks: [], limits: {} };
  try {
    status = await api('status');
  } catch (e) {
    status.error = e.message;
  }
  // Only pages with something to filter keep the left column.
  if (!['manage', 'history'].includes(r.page)) document.getElementById('filters').remove();
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
      case 'campaign': await campaign(ctx); break;
      case 'new': await newPair(ctx); break;
      case 'presets': await presets(ctx); break;
      case 'history': await history(ctx); break;
      case 'drafts': await drafts(ctx); break;
      case 'requests': await requests(ctx); break;
      case 'request': await request(ctx); break;
      default:
        main.append(h('h1', {}, 'Página não encontrada'), h('p', {}, h('a', { href: '/launch/campaigns' }, 'Voltar para as campanhas')));
    }
  } catch (e) {
    main.append(note('fail', e.message));
  }
}

if (typeof document !== 'undefined') start();
