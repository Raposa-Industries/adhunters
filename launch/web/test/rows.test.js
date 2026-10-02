// Campanhas' rows under the filters (launch/web/pages/rows.js).
import test from 'node:test';
import assert from 'node:assert/strict';

import { nest, inState } from '../pages/rows.js';

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
