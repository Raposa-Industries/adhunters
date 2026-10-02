// route: which page an address under /launch/ draws, with no page in it, so
// node can test it (launch/web/test). Launch has two screens, Campanhas and
// Contas, plus the new-item steps behind + Novo.

// GONE are the screens Launch no longer draws: the tables before batch 126
// (groups, ads) and, since Draw Designer IMPLEMENT d587e1b829, Pedidos (with
// one request's page), Histórico, Presets and Rascunhos. Their addresses
// open Campanhas, so an old link or bookmark never lands on a blank page.
// Their API calls and rows are kept.
export const GONE = ['groups', 'ads', 'requests', 'history', 'presets', 'drafts'];

// route reads a path into a page and its parts.
export function route(pathname) {
  const parts = pathname.replace(/^\/launch\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
  const [first] = parts;
  if (!first) return { page: 'manage', tab: 'campaigns' };
  if (first === 'campaigns' && parts.length === 1) return { page: 'manage', tab: 'campaigns' };
  if (GONE.includes(first) && (parts.length === 1 || (first === 'requests' && parts.length === 2))) return { page: 'old', tab: 'campaigns' };
  if (first === 'new' && parts.length === 1) return { page: 'new', tab: 'campaigns' };
  if (first === 'accounts' && parts.length === 1) return { page: 'accounts', tab: 'accounts' };
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
