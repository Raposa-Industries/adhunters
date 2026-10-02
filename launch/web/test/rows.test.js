// Campanhas' rows under the filters (launch/web/pages/rows.js).
import test from 'node:test';
import assert from 'node:assert/strict';

import { nest, inState, standIns, byAccount } from '../pages/rows.js';

const groups = [
  { id: '1001', name: 'Memory Loss US', status: 'RUNNING', account: 'a' },
  { id: '1002', name: 'Blood Pressure US', status: 'PAUSED', account: 'a' },
  { id: '1003', name: 'Empty', status: 'RUNNING', account: 'a' },
  { id: '1001', name: 'Same id, other account', status: 'RUNNING', account: 'b' },
];
const campaigns = [
  { id: '6', name: 'Memory Loss US · Mobile', status: 'RUNNING', device: 'mobile', group_id: '1001', account: 'a' },
  { id: '5', name: 'Memory Loss US · Desktop', status: 'RUNNING', device: 'desktop', group_id: '1001', account: 'a' },
  { id: '7', name: 'Memory old test', status: 'PAUSED', device: 'both', group_id: '1001', account: 'a' },
  { id: '10', name: 'BP Seniors · Desktop', status: 'PENDING_APPROVAL', device: 'desktop', group_id: '1002', account: 'a' },
  { id: '20', name: 'Other account', status: 'RUNNING', device: 'desktop', group_id: '1001', account: 'b' },
];
const all = { state: 'all', device: 'all', text: '' };
const ids = (rows) => rows.map((r) => r.g.account + '/' + r.g.id + ':' + r.cs.map((c) => c.id).join(','));

test('inState: running, paused and everything else', () => {
  assert.ok(inState('APPROVED', 'running'));
  assert.ok(inState('STOPPED', 'paused'));
  assert.ok(inState('PENDING_APPROVAL', 'other'));
  assert.ok(!inState('RUNNING', 'other'));
});

test('nest: every group with its own campaigns, groups of each account apart', () => {
  assert.deepEqual(ids(nest(groups, campaigns, all)), ['a/1001:6,5,7', 'a/1002:10', 'a/1003:', 'b/1001:20']);
});

test('nest: a state keeps a group that holds a matching campaign, and a matching empty group', () => {
  assert.deepEqual(ids(nest(groups, campaigns, { ...all, state: 'paused' })), ['a/1001:7', 'a/1002:']);
  assert.deepEqual(ids(nest(groups, campaigns, { ...all, state: 'other' })), ['a/1002:10']);
});

test('nest: a device shows only groups with such a campaign; "both" passes either', () => {
  assert.deepEqual(ids(nest(groups, campaigns, { ...all, device: 'mobile' })), ['a/1001:6,7']);
});

test("nest: text finds a campaign, or a group with all its campaigns", () => {
  assert.deepEqual(ids(nest(groups, campaigns, { ...all, text: 'seniors' })), ['a/1002:10']);
  assert.deepEqual(ids(nest(groups, campaigns, { ...all, text: 'memory loss' })), ['a/1001:6,5,7']);
  assert.deepEqual(ids(nest(groups, campaigns, { ...all, text: '1003' })), ['a/1003:']);
});

test('nest: ticked groups and campaigns keep only those; nothing ticked keeps all', () => {
  assert.deepEqual(ids(nest(groups, campaigns, { ...all, groups: new Set(['a/1002', 'b/1001']) })), ['a/1002:10', 'b/1001:20']);
  assert.deepEqual(ids(nest(groups, campaigns, { ...all, camps: new Set(['5', '10']) })), ['a/1001:5', 'a/1002:10']);
  assert.deepEqual(ids(nest(groups, campaigns, { ...all, groups: new Set(['a/1001']), camps: new Set(['10']) })), []);
  assert.deepEqual(ids(nest(groups, campaigns, { ...all, groups: new Set(), camps: new Set() })), ids(nest(groups, campaigns, all)));
});

test('standIns: one stand-in per missing group of each account, and "Sem grupo"', () => {
  const groups = [{ id: '1', account: 'a' }];
  const campaigns = [
    { id: '10', group_id: '1', account: 'a' },
    { id: '11', group_id: '9', account: 'a' },
    { id: '12', group_id: '9', account: 'a' },
    { id: '13', group_id: '9', account: 'b' },
    { id: '14', group_id: '', account: 'a' },
  ];
  const out = standIns(groups, campaigns);
  assert.deepEqual(out.map((g) => [g.account, g.id, g.name, !!g.gone, !!g.none]), [
    ['a', '9', 'Grupo apagado · 9', true, false],
    ['b', '9', 'Grupo apagado · 9', true, false],
    ['a', '', 'Sem grupo', false, true],
  ]);
  assert.deepEqual(standIns(groups, campaigns.slice(0, 1)), []);
});

test('byAccount: each account on top with its own groups, in the order given', () => {
  const accounts = [{ id: 'b', name: 'Beta' }, { id: 'a', name: 'Acme Health 1' }, { id: 'c', name: 'Empty' }];
  const acc = (out) => out.map((x) => x.a.id + '[' + ids(x.rows).join(' ') + ']');
  // No filter: every account, even one with no group yet.
  assert.deepEqual(acc(byAccount(accounts, groups, campaigns, all)), ['b[b/1001:20]', 'a[a/1001:6,5,7 a/1002:10 a/1003:]', 'c[]']);
  // A filter keeps only the accounts that still have a group.
  assert.deepEqual(acc(byAccount(accounts, groups, campaigns, { ...all, text: 'seniors' })), ['a[a/1002:10]']);
  assert.deepEqual(acc(byAccount(accounts, groups, campaigns, { ...all, groups: new Set(['b/1001']) })), ['b[b/1001:20]']);
  // An account found by its name or id keeps every group that passes the rest.
  assert.deepEqual(acc(byAccount(accounts, groups, campaigns, { ...all, text: 'acme' })), ['a[a/1001:6,5,7 a/1002:10 a/1003:]']);
  assert.deepEqual(acc(byAccount(accounts, groups, campaigns, { ...all, text: 'acme', state: 'paused' })), ['a[a/1001:7 a/1002:]']);
  assert.deepEqual(acc(byAccount(accounts, groups, campaigns, { ...all, text: 'empty' })), ['a[a/1003:]', 'c[]']);
});
