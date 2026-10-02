// Contas' rows and Nova conta's accounts (launch/web/pages/accountrows.js).
import test from 'node:test';
import assert from 'node:assert/strict';

import { rowsFrom, inLaunch, dayMonth, joining, loginName, goesVia } from '../pages/accountrows.js';

const logins = [
  { id: 0, server: true, network: 'taboola', name: 'x', accounts: [{ id: 'zoltagroup-1-sc', name: 'ZoltaGroup 1', proxy: '' }, { id: 'zoltagroup-2-sc', name: 'ZoltaGroup 2', proxy: 'zg.example:1080' }] },
  { id: 3, network: 'taboola', name: 'Acme', added_at: '2026-10-02T15:00:00Z', proxy: 'px.example:8080', accounts: [{ id: 'acme-health-1-sc', name: 'Acme Health 1', proxy: 'px.example:8080' }, { id: 'zoltagroup-2-sc', name: 'Shared', proxy: 'px.example:8080' }, { id: 'acme-health-2-sc', name: '', proxy: 'px.example:8080', problem: 'o proxy px.example:8080 não respondeu a tempo' }] },
  { id: 4, network: 'taboola', name: 'Broken', problem: 'a chave mudou', accounts: [] },
];

test('rowsFrom: one row per account, an account two logins share stays with the first', () => {
  const rows = rowsFrom(logins);
  assert.deepEqual(rows.map((r) => [r.id, r.name, r.login.id]), [
    ['zoltagroup-1-sc', 'ZoltaGroup 1', 0], ['zoltagroup-2-sc', 'ZoltaGroup 2', 0],
    ['acme-health-1-sc', 'Acme Health 1', 3], ['acme-health-2-sc', 'acme-health-2-sc', 3],
  ]);
  assert.equal(rows[0].added_at, '');
  assert.equal(rows[3].problem, 'o proxy px.example:8080 não respondeu a tempo');
  assert.equal(rows[2].problem, '');
  assert.equal(rows[2].added_at, '2026-10-02T15:00:00Z');
  assert.deepEqual(rowsFrom(undefined), []);
});

test('goesVia: the proxy, direto for a server account without one, nothing for an added one without', () => {
  const rows = rowsFrom(logins);
  assert.deepEqual(rows.map(goesVia), ['direto', 'zg.example:1080', 'px.example:8080', 'px.example:8080']);
  assert.equal(goesVia({ proxy: '', login: { id: 9 } }), '');
});

test('inLaunch: groups and campaigns, or nenhum grupo', () => {
  assert.equal(inLaunch({ groups: [1, 2], campaigns: [1, 2, 3, 4, 5] }), '2 grupos · 5 campanhas');
  assert.equal(inLaunch({ groups: [1], campaigns: [1] }), '1 grupo · 1 campanha');
  assert.equal(inLaunch({ groups: [], campaigns: [] }), 'nenhum grupo');
  assert.equal(inLaunch({ groups: [1], campaigns: [] }), '1 grupo · nenhuma campanha');
  assert.equal(inLaunch({ groups: [], campaigns: [1, 2] }), 'nenhum grupo · 2 campanhas');
});

test('dayMonth: dd/mm, a dash without a date', () => {
  assert.equal(dayMonth(new Date(2026, 9, 2, 12).toISOString()), '02/10');
  assert.equal(dayMonth(''), '—');
  assert.equal(dayMonth('nope'), '—');
});

const allowed = [
  { id: 'zolta-network', name: 'ZoltaGroup', network: true },
  { id: 'zoltagroup-3-sc', name: 'ZoltaGroup 3', network: false },
  { id: 'zoltagroup-4-sc', name: 'ZoltaGroup 4', network: false },
  { id: 'zoltagroup-1-sc', name: 'ZoltaGroup 1', network: false },
];

test('joining: every advertiser account joins, never the network account; fresh leaves out those already in', () => {
  const j = joining(allowed, new Set(['zoltagroup-1-sc']));
  assert.deepEqual(j.all.map((a) => a.id), ['zoltagroup-3-sc', 'zoltagroup-4-sc', 'zoltagroup-1-sc']);
  assert.deepEqual(j.fresh.map((a) => a.id), ['zoltagroup-3-sc', 'zoltagroup-4-sc']);
  assert.equal(joining(allowed).fresh.length, 3);
});

test('loginName: the network account, else the accounts, at most 60 letters', () => {
  assert.equal(loginName(allowed), 'ZoltaGroup');
  assert.equal(loginName(allowed.slice(1, 3)), 'ZoltaGroup 3 · ZoltaGroup 4');
  const long = loginName(Array.from({ length: 10 }, (_, i) => ({ id: 'a' + i, name: 'Account number ' + i })));
  assert.equal([...long].length, 60);
  assert.ok(long.endsWith('…'));
  assert.equal(loginName([]), 'Taboola');
});
