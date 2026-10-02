// rows: the plain-data part of Campanhas' table, with no page in it, so
// node can test it (launch/web/test): which groups and campaigns show
// under the filters.

export const RUNNING = new Set(['RUNNING', 'APPROVED']);
export const PAUSED = new Set(['PAUSED', 'STOPPED']);

// inState says whether a Taboola status is in the state filter: all,
// running, paused or other.
export function inState(s, which) {
  if (which === 'all') return true;
  if (which === 'running') return RUNNING.has(s);
  if (which === 'paused') return PAUSED.has(s);
  return !RUNNING.has(s) && !PAUSED.has(s);
}

// nest is the table's rows before drawing: each group ({id, name, status,
// account}) with its campaigns ({id, name, status, device, group_id,
// account}) that pass the filters f: state, device (all, desktop, mobile)
// and text (lower case, or ''). A group shows when one of its campaigns
// does, or, with no device asked, when it passes by itself; a group found
// by its own name keeps all its campaigns that pass the state and device.
// A campaign targeting several devices ("both") passes either device.
export function nest(groups, campaigns, f) {
  const key = (acct, id) => acct + '/' + (id || '-');
  const inGroup = new Map();
  for (const c of campaigns) {
    const k = key(c.account, c.group_id);
    if (!inGroup.has(k)) inGroup.set(k, []);
    inGroup.get(k).push(c);
  }
  const text = (s) => !f.text || String(s || '').toLowerCase().includes(f.text);
  const device = (c) => f.device === 'all' || c.device === f.device || c.device === 'both';
  const out = [];
  for (const g of groups) {
    const all = inGroup.get(key(g.account, g.id)) || [];
    const named = !!f.text && (text(g.name) || text(g.id));
    const cs = all.filter((c) => inState(c.status, f.state) && device(c) && (named || text(c.name) || text(c.id)));
    const self = f.device === 'all' && inState(g.status, f.state) && (text(g.name) || text(g.id));
    if (cs.length || self) out.push({ g, key: key(g.account, g.id), cs });
  }
  return out;
}

// standIns are the groups to add for campaigns whose group is not in the
// list: "Grupo apagado · id" when the group was deleted (Taboola keeps its
// campaigns, Realize says "Campaign Group Was Deleted"), "Sem grupo" when
// the campaign has none. One per account and group.
export function standIns(groups, campaigns) {
  const known = new Set(groups.map((g) => g.account + '/' + g.id));
  const out = [];
  for (const c of campaigns) {
    const key = c.account + '/' + (c.group_id || '');
    if (known.has(key)) continue;
    known.add(key);
    out.push(c.group_id ?
      { id: c.group_id, name: 'Grupo apagado · ' + c.group_id, status: 'GROUP_DELETED', account: c.account, gone: true } :
      { id: '', name: 'Sem grupo', status: '', account: c.account, none: true });
  }
  return out;
}
