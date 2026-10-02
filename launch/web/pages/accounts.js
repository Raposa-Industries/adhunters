// Contas: the Taboola accounts Launch uses, one row each (Draw Designer
// IMPLEMENT d587e1b829; Figma "Launch · Contas" and "Contas · nova conta").
// They come from Taboola logins: the server's own (TABOOLA_* in its
// settings) and those people add with + Novo, pasting a client ID, user ID,
// client secret and a proxy. Every advertiser account the login sees joins;
// nobody ticks. Adding a login only reads from Taboola (a token and the
// account list, through the proxy), and the secret and proxy are sealed on
// the server and never shown again.
//
// Every request for an added login's accounts goes through its proxy, and
// is refused without one. An account of the server's own login goes direct
// unless a person sets it a proxy here. Pages say whether an account has a
// proxy, never which. Each row's ··· menu holds
// what can be done to the account and its login.
import { api, h, note, field, input, busy, plural } from './lib.js';
import { rowsFrom, inLaunch, dayMonth, joining, loginName, hasProxy } from './accountrows.js';

const PROXY_EXAMPLE = 'http://usuario:senha@host:porta';
const PROXY_HINT = 'http, https ou socks5; todo pedido à Taboola dessas contas passa por ele';

// plus is the + of "+ Novo" (Ember's plus icon, 16 px).
function plus() {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  for (const [k, v] of Object.entries({ viewBox: '0 0 16 16', width: '16', height: '16', 'aria-hidden': 'true', class: 'btn-icon' })) svg.setAttribute(k, v);
  const path = document.createElementNS(ns, 'path');
  path.setAttribute('d', 'M8 3v10M3 8h10');
  svg.append(path);
  return svg;
}

export async function accounts({ main }) {
  const told = h('div', { class: 'result' });
  const body = h('div');
  main.append(
    h('div', { class: 'page-head accounts-head' },
      h('div', {}, h('h1', {}, 'Contas'),
        h('p', { class: 'lead' }, 'As contas da Taboola que o Launch usa. Elas aparecem no seletor Conta e na criação de grupos.')),
      h('button', { type: 'button', class: 'primary new-btn', onclick: () => newLogin(rows, added) }, plus(), 'Novo')),
    told, body);

  let rows = [];
  async function draw() {
    const data = await api('logins');
    rows = rowsFrom(data.logins);
    // A login Taboola did not answer for at all shows here, with what can be
    // done.
    const problems = data.logins.filter((l) => l.problem && !l.server).map((l) => problemNote(l));
    if (!rows.length) {
      body.replaceChildren(...problems, h('p', { class: 'empty' }, 'Nenhuma conta da Taboola ainda. Clique em + Novo e cole o client ID, o user ID, o client secret e o proxy.'));
      return;
    }
    const counts = new Map();
    body.replaceChildren(...problems,
      h('div', { class: 'table-card accounts-card' }, h('table', { class: 'list accounts' },
        h('thead', {}, h('tr', {}, h('th', { class: 'acct-name' }, 'Conta'), h('th', { class: 'acct-id' }, 'ID'), h('th', { class: 'acct-count' }, 'No Launch'), h('th', { class: 'acct-when' }, 'Adicionada'), h('th', { class: 'acct-more', 'aria-label': 'Ações' }))),
        h('tbody', {}, rows.map((r) => {
          const count = h('td', { class: 'acct-count' }, '…');
          counts.set(r, count);
          return h('tr', {},
            h('td', { class: 'acct-name' }, h('span', { class: 'acct-title' }, r.name), r.problem ? h('div', { class: 'warn-line' }, r.problem) : null),
            h('td', { class: 'acct-id' }, r.id),
            count,
            h('td', { class: 'acct-when' }, dayMonth(r.added_at)),
            h('td', { class: 'acct-more' }, rowMenu(r)));
        })))),
      h('p', { class: 'faint acct-total' }, plural(rows.length, 'conta', 'contas')));
    // What each account holds in Launch, from its tree (the server keeps it
    // 30 s); the rows show before the counts arrive.
    await Promise.all(rows.map(async (r) => {
      const cell = counts.get(r);
      try {
        cell.textContent = inLaunch(await api(`${r.net}/${encodeURIComponent(r.id)}/tree`));
        cell.className = 'acct-count';
      } catch (e) {
        cell.textContent = '—';
        cell.title = e.message;
      }
    }));
  }

  function problemNote(l) {
    const out = h('span');
    const drop = h('button', { type: 'button', class: 'small' }, 'Remover o acesso');
    drop.onclick = () => removeLogin(l, drop, out);
    return note('fail', h('b', {}, l.name + ': '), l.problem, ' ', drop, out);
  }

  async function added(n) {
    told.replaceChildren(note('ok', `${plural(n, 'conta entrou', 'contas entraram')} no Launch. Elas já aparecem no seletor Conta e na criação de grupos.`));
    await draw();
  }

  // ---- the ··· menu of an account ----
  function rowMenu(r) {
    const l = r.login;
    const menu = h('details', { class: 'row-menu' });
    const shut = () => { menu.open = false; };
    const item = (label, run) => h('button', { type: 'button', role: 'menuitem', onclick: () => { shut(); run(); } }, label);
    const items = l.server
      // The server's own login: only its proxy changes here.
      ? [item('Proxy', () => accountProxy(r))]
      : [
        item('Proxy', () => loginProxy(r)),
        item('Detalhes do acesso', () => details(r)),
        item('Escolher as contas deste acesso', () => chooseAccounts(l)),
        item('Tirar esta conta do Launch', () => dropAccount(r)),
        item(`Remover o acesso (${plural((l.accounts || []).length, 'conta', 'contas')})`, () => removeLogin(l, null, null)),
      ];
    menu.append(
      h('summary', { 'aria-label': 'Ações da conta ' + r.name, title: 'Ações' }, '···'),
      h('div', { class: 'menu', role: 'menu' }, items));
    // One menu open at a time; a click elsewhere closes it.
    menu.addEventListener('toggle', () => {
      if (!menu.open) return;
      for (const m of document.querySelectorAll('details.row-menu[open]')) if (m !== menu) m.open = false;
      const away = (e) => { if (!menu.contains(e.target)) { shut(); document.removeEventListener('click', away); } };
      setTimeout(() => document.addEventListener('click', away));
    });
    return menu;
  }

  // dropAccount takes one account out; the login's last one takes the login.
  async function dropAccount(r) {
    const others = (r.login.accounts || []).filter((a) => a.id !== r.id);
    if (!others.length) return removeLogin(r.login, null, null);
    if (!confirm(`Tirar a conta “${r.name}” do Launch? Ela sai do seletor Conta. Nada muda na Taboola.`)) return;
    await say(async () => {
      await api('logins/' + r.login.id, { method: 'PUT', body: { name: r.login.name, accounts: others.map((a) => a.id) } });
      told.replaceChildren(note('ok', `A conta “${r.name}” saiu do Launch.`));
    });
  }

  async function removeLogin(l, button, out) {
    const names = (l.accounts || []).map((a) => a.name || a.id).join(', ');
    if (!confirm(`Remover o acesso “${l.name}”?` + (names ? ` Saem do Launch: ${names}.` : '') + ' Nada muda na Taboola.')) return;
    const work = async () => {
      await api('logins/' + l.id, { method: 'DELETE' });
      told.replaceChildren(note('ok', `O acesso “${l.name}” foi removido.`));
    };
    if (button) await busy(button, out, async () => { await work(); await draw(); });
    else await say(work);
  }

  // say runs a menu action, shows its error on top, and draws the list again.
  async function say(work) {
    told.replaceChildren(note('', 'Aguarde…'));
    try {
      await work();
    } catch (e) {
      told.replaceChildren(note('fail', e.message));
    }
    await draw().catch((e) => told.append(note('fail', e.message)));
  }

  // proxyForm is a dialog with one proxy field; save gets what was typed.
  function proxyForm(title, lead, required, has, save, clear) {
    const box = input({ placeholder: PROXY_EXAMPLE, autocomplete: 'off', spellcheck: 'false', class: 'mono', 'aria-label': 'Proxy', required });
    const msg = h('div');
    const ok = h('button', { type: 'submit', class: 'primary' }, 'Salvar');
    const cancel = h('button', { type: 'button' }, 'Cancelar');
    const off = clear ? h('button', { type: 'button', class: 'ghost' }, 'Tirar o proxy') : null;
    const form = h('form', { class: 'fields-col', novalidate: true },
      h('p', { class: 'muted' }, has ? 'Agora: com proxy. O novo substitui o atual.' : required ? 'Agora: sem proxy; as contas não funcionam até ter um.' : 'Agora: sem proxy (vai direto à Taboola).'),
      field('Novo proxy', box, PROXY_HINT), msg, h('div', { class: 'actions' }, off, cancel, ok));
    const dlg = dialog(title, h('p', { class: 'lead muted' }, lead), form);
    dlg.addEventListener('close', () => { box.value = ''; });
    cancel.onclick = () => dlg.close();
    const done = async () => { dlg.close(); await draw(); };
    form.onsubmit = (e) => {
      e.preventDefault();
      if (!box.value.trim()) {
        msg.replaceChildren(note('fail', 'Cole o proxy: ' + PROXY_EXAMPLE));
        return;
      }
      busy(ok, msg, async () => { await save(box.value); await done(); });
    };
    if (off) off.onclick = () => busy(off, msg, async () => { if ((await clear()) !== false) await done(); });
    box.focus();
  }

  // loginProxy changes an added login's proxy: every account of the login
  // goes through it. It can be changed, never taken away.
  function loginProxy(r) {
    const l = r.login;
    const names = (l.accounts || []).map((a) => a.name || a.id).join(', ');
    proxyForm('Proxy do acesso', `Vale para todas as contas deste acesso (${names}). O Launch testa o novo proxy na Taboola (só lendo a lista de contas) antes de salvar.`,
      true, !!l.proxy, async (proxy) => {
        await api(`logins/${l.id}/proxy`, { method: 'PUT', body: { proxy } });
        told.replaceChildren(note('ok', `Proxy do acesso “${l.name}” trocado.`));
      }, null);
  }

  // accountProxy sets, changes or takes away the proxy of an account of the
  // server's own login; without one it goes direct.
  function accountProxy(r) {
    proxyForm('Proxy da conta', `Os pedidos da conta ${r.name} à Taboola passam por ele, e nunca direto enquanto ele existir; sem proxy, vão direto. O Launch testa o proxy na Taboola (só lendo a lista de contas) antes de salvar.`,
      false, hasProxy(r), async (proxy) => {
        await api(`accounts/${r.net}/${encodeURIComponent(r.id)}/proxy`, { method: 'PUT', body: { proxy } });
        told.replaceChildren(note('ok', `A conta ${r.name} agora passa pelo proxy.`));
      }, hasProxy(r) ? async () => {
        if (!confirm(`Tirar o proxy da conta ${r.name}? Os pedidos dela voltam a ir direto à Taboola.`)) return false;
        await api(`accounts/${r.net}/${encodeURIComponent(r.id)}/proxy`, { method: 'PUT', body: { proxy: '' } });
        told.replaceChildren(note('ok', `A conta ${r.name} vai direto à Taboola de novo.`));
      } : null);
  }

  // details shows an added login: its client ID (start and end), user ID
  // (which can be changed), proxy and accounts.
  function details(r) {
    const l = r.login;
    const user = input({ value: l.user_id || '', autocomplete: 'off', spellcheck: 'false', class: 'mono', 'aria-label': 'User ID' });
    const msg = h('div');
    const ok = h('button', { type: 'submit', class: 'primary' }, 'Salvar');
    const cancel = h('button', { type: 'button' }, 'Fechar');
    const form = h('form', { class: 'fields-col', novalidate: true },
      h('dl', { class: 'acct-facts' },
        h('dt', {}, 'Client ID'), h('dd', { class: 'mono' }, l.client_id),
        h('dt', {}, 'Proxy'), h('dd', {}, l.proxy ? 'definido (troque em ··· › Proxy)' : 'nenhum: as contas não funcionam até ter um'),
        h('dt', {}, 'Contas'), h('dd', {}, (l.accounts || []).map((a) => a.name || a.id).join(', ') || '—'),
        h('dt', {}, 'Adicionado'), h('dd', {}, dayMonth(l.added_at) + (l.added_by ? ' por ' + l.added_by.split('@')[0] : ''))),
      field('User ID', user, l.user_id ? 'O ID do usuário da Taboola deste acesso.' : 'Este acesso ainda não tem user ID; preencha.'),
      msg, h('div', { class: 'actions' }, cancel, ok));
    const dlg = dialog('Acesso “' + l.name + '”', form);
    cancel.onclick = () => dlg.close();
    form.onsubmit = (e) => {
      e.preventDefault();
      busy(ok, msg, async () => {
        await api(`logins/${l.id}/user-id`, { method: 'PUT', body: { user_id: user.value } });
        dlg.close();
        told.replaceChildren(note('ok', `User ID do acesso “${l.name}” salvo.`));
        await draw();
      });
    };
  }

  // chooseAccounts asks Taboola again (through the login's proxy) what the
  // login sees, and lets the person pick which of its accounts Launch uses.
  async function chooseAccounts(l) {
    const msg = h('div');
    const save = h('button', { type: 'button', class: 'primary', disabled: true }, 'Salvar');
    const cancel = h('button', { type: 'button' }, 'Cancelar');
    const list = h('div', {}, note('', 'Perguntando à Taboola…'));
    const box = dialog('Escolher as contas', h('p', { class: 'lead muted' }, `As contas que o acesso “${l.name}” vê na Taboola. As marcadas ficam no Launch.`), list, msg, h('div', { class: 'actions' }, cancel, save));
    cancel.onclick = () => box.close();
    try {
      const data = await api(`logins/${l.id}/allowed`);
      const picks = picker(data.accounts, (l.accounts || []).map((a) => a.id));
      list.replaceChildren(picks.el);
      save.disabled = false;
      save.onclick = () => busy(save, msg, async () => {
        await api('logins/' + l.id, { method: 'PUT', body: { name: l.name, accounts: picks.chosen() } });
        box.close();
        told.replaceChildren(note('ok', `As contas do acesso “${l.name}” foram salvas.`));
        await draw();
      });
    } catch (e) {
      list.replaceChildren(note('fail', e.message));
    }
  }

  await draw();
}

// dialog opens a modal with a title and its parts; it removes itself when
// closed (Esc, or a button that calls close()).
function dialog(title, ...parts) {
  const id = 'dlg-' + Math.random().toString(36).slice(2);
  const box = h('dialog', { class: 'dialog', 'aria-labelledby': id }, h('h2', { id }, title), ...parts);
  box.addEventListener('close', () => box.remove());
  document.body.append(box);
  box.showModal();
  return box;
}

// newLogin is + Novo: paste the client ID, user ID, client secret and
// proxy (the owner's: every login's requests go through its own proxy).
// Once all four are in, Launch asks Taboola through that proxy for the
// login's accounts (reads only) and the green box says which join; the one
// button adds them all. Pressed before that, it asks and adds in one go.
// rows is the list on the page, so accounts already in Launch are not
// counted again.
function newLogin(rows, added) {
  const id = input({ autocomplete: 'off', spellcheck: 'false', class: 'mono', 'aria-label': 'Client ID' });
  const user = input({ autocomplete: 'off', spellcheck: 'false', class: 'mono', 'aria-label': 'User ID' });
  const secret = input({ type: 'password', autocomplete: 'new-password', spellcheck: 'false', 'aria-label': 'Client secret' });
  const proxy = input({ autocomplete: 'off', spellcheck: 'false', class: 'mono', placeholder: PROXY_EXAMPLE, 'aria-label': 'Proxy' });
  const found = h('div');
  const msg = h('div');
  const go = h('button', { type: 'submit', class: 'primary' }, 'Adicionar contas');
  const cancel = h('button', { type: 'button' }, 'Cancelar');
  const have = new Set(rows.map((r) => r.id));
  let checked = null; // {key, allowed, all, fresh} once Taboola answered for these fields
  let asking = null; // the check under way: {key, promise}
  const form = h('form', { class: 'fields-col', novalidate: true },
    field('Client ID', id),
    field('User ID', user),
    field('Client secret', secret, 'fica guardado criptografado e não aparece de novo'),
    field('Proxy', proxy, PROXY_HINT),
    found, msg, h('div', { class: 'actions' }, cancel, go));
  const box = dialog('Nova conta',
    h('p', { class: 'lead' }, 'Cole o client ID e o client secret da Taboola (Backstage › Account Settings › API). Todas as contas desse acesso entram no Launch.'),
    form);
  // The secret and the proxy never stay in the page once the dialog is gone.
  box.addEventListener('close', () => { secret.value = ''; proxy.value = ''; clearTimeout(wait); });
  cancel.onclick = () => box.close();
  const key = () => JSON.stringify([id.value.trim(), secret.value, proxy.value.trim()]);
  const missing = () => [[id, 'o client ID'], [user, 'o user ID'], [secret, 'o client secret'], [proxy, 'o proxy']].filter(([el]) => !el.value.trim()).map(([, what]) => what);
  const label = () => {
    const n = checked?.fresh.length;
    go.textContent = checked ? (n ? 'Adicionar ' + plural(n, 'conta', 'contas') : 'Nada novo para adicionar') : 'Adicionar contas';
    go.disabled = !!checked && !n;
  };
  // check asks Taboola, through the proxy, what this login sees.
  function check() {
    const k = key();
    if (checked?.key === k) return Promise.resolve(checked);
    if (asking?.key === k) return asking.promise;
    found.replaceChildren(h('p', { class: 'faint', role: 'status' }, 'Perguntando à Taboola pelo proxy…'));
    const promise = api('logins/check', { method: 'POST', body: { client_id: id.value, client_secret: secret.value, proxy: proxy.value } }).then((data) => {
      if (key() !== k) return null;
      checked = { key: k, allowed: data.accounts, ...joining(data.accounts, have) };
      const n = checked.fresh.length;
      found.replaceChildren(h('div', { class: 'joined', role: 'status' },
        h('b', {}, n ? `Conectou: ${plural(n, 'conta vai', 'contas vão')} entrar` : 'Conectou, mas todas as contas desse acesso já estão no Launch'),
        h('div', { class: 'names' }, checked.all.map((a) => h('span', { class: 'acct-chip' + (have.has(a.id) ? ' have' : ''), title: a.id + (have.has(a.id) ? ' · já está no Launch' : '') }, a.name || a.id)))));
      label();
      return checked;
    }, (e) => {
      if (key() === k) found.replaceChildren(note('fail', e.message));
      throw e;
    }).finally(() => { if (asking?.key === k) asking = null; });
    asking = { key: k, promise };
    return promise;
  }
  // Changing the login or its proxy asks again once all four are in.
  let wait = 0;
  for (const el of [id, user, secret, proxy]) {
    el.addEventListener('input', () => {
      msg.replaceChildren();
      if (checked && checked.key !== key()) { checked = null; found.replaceChildren(); label(); }
      clearTimeout(wait);
      if (!missing().length) wait = setTimeout(() => check().catch(() => {}), 700);
    });
  }
  form.onsubmit = (e) => {
    e.preventDefault();
    if (go.disabled) return;
    const lacking = missing();
    if (lacking.length) {
      msg.replaceChildren(note('fail', 'Preencha ' + lacking.join(', ') + '.'));
      return;
    }
    clearTimeout(wait);
    busy(go, msg, async () => {
      let c;
      try {
        c = await check();
      } catch {
        return; // the box says why
      }
      if (!c?.fresh.length) return;
      await api('logins', { method: 'POST', body: { name: loginName(c.allowed), client_id: id.value, user_id: user.value, client_secret: secret.value, proxy: proxy.value, accounts: c.all.map((a) => a.id) } });
      box.close();
      await added(c.fresh.length);
    }).then(() => { if (box.open) label(); });
  };
  id.focus();
}

// picker is the login's accounts as checkboxes; the network account is shown
// but can never be picked.
export function picker(allowed, ticked) {
  const boxes = [];
  const rows = allowed.map((a) => {
    const box = h('input', { type: 'checkbox', value: a.id, checked: !a.network && ticked.includes(a.id), disabled: a.network });
    if (!a.network) boxes.push(box);
    return h('label', { class: 'check login-pick' + (a.network ? ' faint' : '') }, box,
      h('span', {}, a.name || a.id, ' ', h('span', { class: 'mono faint' }, a.id)),
      a.network ? h('span', { class: 'badge' }, 'conta de rede, nunca usada') : null);
  });
  const el = h('fieldset', { class: 'login-picks' }, h('legend', { class: 'fr-label' }, 'Contas'), rows);
  return { el, chosen: () => boxes.filter((b) => b.checked).map((b) => b.value) };
}
