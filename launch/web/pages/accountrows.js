// accountrows: the plain-data part of Contas, with no page in it, so node can
// test it (launch/web/test): the logins' accounts as one list, what each has
// in Launch, and what a new login brings.

// rowsFrom turns the logins (GET logins) into one row per account, in the
// logins' order. An account two logins share belongs to the first, as on
// the server (the server's own login first).
export function rowsFrom(logins) {
  const out = [];
  const seen = new Set();
  for (const l of logins || []) {
    for (const a of l.accounts || []) {
      if (!a?.id || seen.has(a.id)) continue;
      seen.add(a.id);
      out.push({ id: a.id, name: a.name || a.id, net: l.network || 'taboola', added_at: a.added_at || l.added_at || '', proxy: a.proxy || '', problem: a.problem || l.problem || '', login: l });
    }
  }
  return out;
}

// goesVia says how an account's requests reach Taboola: its proxy's host and
// port, or "direto" for an account of the server's own login without one.
// An added login's account without a proxy gives '': its requests are
// refused, and its problem says why. Pages say only whether there is a
// proxy (hasProxy), never which.
export function goesVia(row) {
  if (row.proxy) return row.proxy;
  return row.login?.server ? 'direto' : '';
}

// hasProxy says whether an account's requests go through a proxy.
export function hasProxy(row) {
  return !!row.proxy;
}

// inLaunch says what an account holds, from its tree: "2 grupos · 5
// campanhas", or "nenhum grupo".
export function inLaunch(tree) {
  const g = tree?.groups?.length || 0;
  const c = tree?.campaigns?.length || 0;
  if (!g && !c) return 'nenhum grupo';
  const groups = g ? `${g} ${g === 1 ? 'grupo' : 'grupos'}` : 'nenhum grupo';
  const campaigns = c ? `${c} ${c === 1 ? 'campanha' : 'campanhas'}` : 'nenhuma campanha';
  return groups + ' · ' + campaigns;
}

// dayMonth is a date as dd/mm, in the person's time zone; "—" for none.
export function dayMonth(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (isNaN(d)) return '—';
  return String(d.getDate()).padStart(2, '0') + '/' + String(d.getMonth() + 1).padStart(2, '0');
}

// joining is what a checked login brings (POST logins/check): every
// advertiser account it sees (all), never its network account, and of
// those the ones not in Launch yet (fresh). All of them are added: nobody
// ticks.
export function joining(allowed, have = new Set()) {
  const all = (allowed || []).filter((a) => !a.network);
  return { all, fresh: all.filter((a) => !have.has(a.id)) };
}

// loginName is the name a new login is saved under (the server needs one):
// its network account's name, or else its accounts' names, at most 60
// letters.
export function loginName(allowed) {
  const net = (allowed || []).find((a) => a.network && a.name);
  const names = net ? [net.name] : (allowed || []).filter((a) => !a.network).map((a) => a.name || a.id);
  const name = names.join(' · ').replace(/\s+/g, ' ').trim() || 'Taboola';
  const letters = [...name];
  return letters.length > 60 ? letters.slice(0, 59).join('') + '…' : name;
}

// KEY is what Taboola's client IDs look like: 32 letters and digits (hex).
const KEY = /^[0-9a-f]{32}$/i;

// mixUp says, in Portuguese, when the client ID and the user ID look
// swapped or the client ID looks like an account name, so a person sees
// which is which before Taboola answers a bare 401. '' when nothing looks off.
export function mixUp(clientId, userId) {
  const c = (clientId || '').trim();
  const u = (userId || '').trim();
  if (!c || KEY.test(c)) return '';
  if (KEY.test(u)) return 'O client ID e o user ID parecem trocados: o client ID é a chave de 32 letras e números, e o user ID é o nome da conta (como empresa-network).';
  if (/[-_\s]/.test(c) || /network$/i.test(c)) return 'Esse client ID parece o nome de uma conta: o client ID é a chave de 32 letras e números da página de API do Backstage.';
  return '';
}
