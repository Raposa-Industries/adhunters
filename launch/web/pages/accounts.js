// Contas: the Taboola logins Launch uses. The server's own login comes from
// its settings; people add more here (client ID and secret), choose which of
// each login's advertiser accounts to use, and those accounts appear in the
// Conta picker on every table. Adding a login only reads from Taboola, and
// the secret is never shown again.
import { api, h, note, field, input, busy, date, plural } from './lib.js';

export async function accounts({ main }) {
  const list = h('div', { class: 'logins' });
  const adding = h('div');
  const addButton = h('button', { type: 'button', class: 'primary', onclick: () => openAdd() }, 'Adicionar login');
  main.append(
    h('div', { class: 'page-head' },
      h('div', {}, h('h1', {}, 'Contas'),
        h('p', { class: 'lead muted' }, 'Os logins da Taboola que o Launch usa. As contas que você marcar em cada login aparecem no seletor Conta das tabelas.')),
      h('div', { class: 'actions' }, addButton)),
    adding, list);

  async function draw() {
    const data = await api('logins');
    list.replaceChildren(...(data.logins.length
      ? data.logins.map(loginPanel)
      : [h('p', { class: 'empty' }, 'Nenhum login da Taboola ainda. Adicione um para carregar as contas dele.')]));
  }

  function loginPanel(l) {
    const out = h('div');
    const panel = h('section', { class: 'panel login', 'aria-label': l.name },
      h('div', { class: 'login-head' },
        h('div', { class: 'login-title' },
          h('h2', {}, l.name),
          h('div', { class: 'login-meta' },
            h('span', { class: 'badge' }, 'Taboola'),
            l.server ? h('span', { class: 'badge pair' }, 'do servidor') : null,
            h('span', { class: 'mono faint' }, 'client ID ' + l.client_id),
            l.added_at ? h('span', { class: 'faint' }, 'adicionado ' + date(l.added_at) + (l.added_by ? ' por ' + l.added_by.split('@')[0] : '')) : null)),
        l.server ? null : h('div', { class: 'actions' },
          h('button', { type: 'button', class: 'small', onclick: (e) => choose(l, panel, out, e.currentTarget) }, 'Escolher contas'),
          h('button', { type: 'button', class: 'small ghost', onclick: (e) => remove(l, out, e.currentTarget) }, 'Remover'))),
      l.problem ? note('fail', l.problem) : null,
      accountTable(l.accounts),
      l.server ? h('p', { class: 'hint faint' }, 'Este login vem das configurações do servidor (TABOOLA_*); só muda lá.') : null,
      out);
    return panel;
  }

  async function remove(l, out, button) {
    if (!confirm(`Remover o login “${l.name}”? As contas dele saem do Launch. Nada muda na Taboola.`)) return;
    await busy(button, out, async () => {
      await api('logins/' + l.id, { method: 'DELETE' });
      await draw();
    });
  }

  async function choose(l, panel, out, button) {
    const data = await busy(button, out, () => api(`logins/${l.id}/allowed`));
    if (!data) return;
    const name = input({ value: l.name, maxlength: 60, 'aria-label': 'Nome do login' });
    const picks = picker(data.accounts, l.accounts.map((a) => a.id));
    const msg = h('div');
    const save = h('button', { type: 'button', class: 'primary' }, 'Salvar');
    save.onclick = () => busy(save, msg, async () => {
      await api('logins/' + l.id, { method: 'PUT', body: { name: name.value, accounts: picks.chosen() } });
      await draw();
    });
    out.replaceChildren(h('div', { class: 'login-edit' },
      field('Nome', name), picks.el, msg,
      h('div', { class: 'actions' }, save, h('button', { type: 'button', class: 'ghost', onclick: () => out.replaceChildren() }, 'Cancelar'))));
  }

  function openAdd() {
    addButton.hidden = true;
    adding.replaceChildren(addForm(() => { adding.replaceChildren(); addButton.hidden = false; }, draw));
    adding.querySelector('input')?.focus();
  }

  await draw();
}

function accountTable(accounts) {
  if (!accounts?.length) return h('p', { class: 'muted' }, 'Nenhuma conta escolhida.');
  return h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
    h('thead', {}, h('tr', {}, h('th', {}, 'Conta'), h('th', {}, 'ID'))),
    h('tbody', {}, accounts.map((a) => h('tr', {}, h('td', {}, a.name || a.id), h('td', { class: 'mono muted' }, a.id))))));
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

// addForm is the steps of adding a login: credentials, Verificar (one read
// at Taboola), then the accounts to use and Salvar.
function addForm(close, done) {
  const name = input({ placeholder: 'ZoltaGroup 2', maxlength: 60, autocomplete: 'off' });
  const id = input({ autocomplete: 'off', spellcheck: 'false', class: 'mono' });
  const secret = input({ type: 'password', autocomplete: 'new-password', spellcheck: 'false' });
  const msg = h('div');
  const step2 = h('div');
  const check = h('button', { type: 'button' }, 'Verificar na Taboola');
  let picks = null;
  // Changing the login after a check asks for a new check.
  for (const el of [id, secret]) el.addEventListener('input', () => { picks = null; step2.replaceChildren(); });
  check.onclick = () => busy(check, msg, async () => {
    const data = await api('logins/check', { method: 'POST', body: { client_id: id.value, client_secret: secret.value } });
    const usable = data.accounts.filter((a) => !a.network);
    picks = picker(data.accounts, usable.map((a) => a.id));
    const save = h('button', { type: 'button', class: 'primary' }, 'Salvar login');
    save.onclick = () => busy(save, msg, async () => {
      await api('logins', { method: 'POST', body: { name: name.value, client_id: id.value, client_secret: secret.value, accounts: picks.chosen() } });
      secret.value = '';
      close();
      await done();
    });
    step2.replaceChildren(
      note('ok', `A Taboola aceitou o login: ${plural(usable.length, 'conta de anunciante', 'contas de anunciante')}. Marque as que o Launch vai usar.`),
      picks.el, h('div', { class: 'actions' }, save));
  });
  return h('section', { class: 'panel login-add', 'aria-label': 'Adicionar login' },
    h('h2', {}, 'Adicionar login da Taboola'),
    h('p', { class: 'muted' }, 'O client ID e o client secret ficam no Backstage da Taboola, em Account Settings > API. Ao verificar, o Launch só lê a lista de contas: nada é criado nem mudado na Taboola.'),
    h('div', { class: 'fields' },
      field('Nome', name, 'Como o login aparece aqui'),
      field('Client ID', id),
      field('Client secret', secret, 'Fica guardado criptografado e não aparece de novo')),
    h('div', { class: 'actions' }, check, h('button', { type: 'button', class: 'ghost', onclick: close }, 'Cancelar')),
    msg, step2);
}
