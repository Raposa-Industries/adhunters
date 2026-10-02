// historyTable: what Launch did to a campaign, newest first, with who did it
// and who asked (a person, or Intel's or Desk's suggestion). The campaign
// panel's Histórico tab shows it; the Histórico screen is gone (IMPLEMENT
// d587e1b829), its rows and its API call are kept.
import { h, link, date } from './lib.js';

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
