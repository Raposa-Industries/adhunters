// Histórico: everything Launch did, newest first, with who did it and who
// asked (a person, or Intel's or Desk's suggestion).
import { api, h, link, date, input } from './lib.js';

const KINDS = {
  new_group: 'Grupo novo',
  new_pair: 'Par novo',
  copy: 'Duplicou',
  move: 'Mudou de grupo',
  move_done: 'Pausou a original',
  pause: 'Pausou',
  change: 'Mudou',
};

const RESULTS = { done: ['feito', 'running'], partial: ['em parte', 'review'], failed: ['falhou', 'rejected'], waiting: ['esperando', 'paused'] };

export function historyTable(list, withCampaign = true) {
  return h('div', { class: 'table-wrap' }, h('table', { class: 'list' },
    h('thead', {}, h('tr', {}, h('th', {}, 'Quando'), h('th', {}, 'O quê'), withCampaign ? h('th', {}, 'Campanha') : null, h('th', {}, 'Quem'), h('th', {}, 'Resultado'))),
    h('tbody', {}, list.map((c) => {
      const [label, cls] = RESULTS[c.result] || [c.result, ''];
      return h('tr', {},
        h('td', { class: 'muted nowrap' }, date(c.at)),
        h('td', {}, c.summary, (c.problems || []).length ? h('div', { class: 'warn-line' }, c.problems.join(' · ')) : null),
        withCampaign ? h('td', { class: 'mono' }, c.campaign_id ? h('a', { href: link(c.network, c.account, c.group_id || '-', c.campaign_id) }, c.campaign_id) : c.group_id ? h('a', { href: link(c.network, c.account, c.group_id) }, 'grupo ' + c.group_id) : '') : null,
        h('td', { class: 'muted' }, (c.who || '').split('@')[0], c.asked_by && c.asked_by !== c.who ? h('div', { class: 'faint' }, 'pedido: ' + c.asked_by) : null),
        h('td', {}, h('span', { class: 'badge ' + cls }, label)));
    }))));
}

export async function history({ main, aside }) {
  main.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Histórico'),
    h('p', { class: 'lead muted' }, 'Tudo o que o Launch fez nas redes, do mais novo ao mais antigo.'))));
  const data = await api('history?limit=500');
  const f = { kind: '', text: '' };
  const box = h('div');
  aside.append(
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'Busca'),
      input({ type: 'search', placeholder: 'nome, id ou pessoa', 'aria-label': 'Filtrar o histórico', oninput: (e) => { f.text = e.target.value.trim().toLowerCase(); draw(); } })),
    h('div', { class: 'filter-group' }, h('span', { class: 'fr-label' }, 'O quê'),
      h('div', { class: 'chips' }, [['', 'Tudo'], ...Object.entries(KINDS)].map(([k, label]) => h('label', { class: 'chip' },
        h('input', { type: 'radio', name: 'kind', value: k, checked: f.kind === k, onchange: () => { f.kind = k; draw(); } }), h('span', {}, label))))));
  if (data.moves.length) {
    main.append(h('p', { class: 'note' }, `${data.moves.length} mudança(s) de grupo esperando a cópia começar para pausar a original.`));
  }
  main.append(box);
  function draw() {
    const list = data.history.filter((c) => (!f.kind || c.kind === f.kind) &&
      (!f.text || [c.summary, c.campaign_id, c.group_id, c.who, c.asked_by].some((x) => (x || '').toLowerCase().includes(f.text))));
    box.replaceChildren(list.length ? historyTable(list) : h('p', { class: 'empty' }, data.history.length ? 'Nada com esses filtros.' : 'O Launch ainda não fez nada.'));
  }
  draw();
}
