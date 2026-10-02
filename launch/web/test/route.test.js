// Which page an address draws (launch/web/pages/route.js). Launch has two
// screens, Campanhas and Contas; the removed ones open Campanhas.
import test from 'node:test';
import assert from 'node:assert/strict';

import { route, oldAddress } from '../pages/route.js';

test('route: Campanhas, the steps and Contas', () => {
  assert.deepEqual(route('/launch/'), { page: 'manage', tab: 'campaigns' });
  assert.deepEqual(route('/launch/campaigns'), { page: 'manage', tab: 'campaigns' });
  assert.deepEqual(route('/launch/new'), { page: 'new', tab: 'campaigns' });
  assert.deepEqual(route('/launch/accounts'), { page: 'accounts', tab: 'accounts' });
});

test('route: Pedidos, Pedido, Histórico, Presets and Rascunhos open Campanhas, like the old tables', () => {
  for (const p of ['/launch/requests', '/launch/requests/12', '/launch/history', '/launch/presets', '/launch/drafts', '/launch/groups', '/launch/ads', '/launch/history/']) {
    assert.deepEqual(route(p), { page: 'old', tab: 'campaigns' }, p);
  }
});

test('route: a campaign link opens it; an account or group link narrows Campanhas', () => {
  assert.deepEqual(route('/launch/taboola/acme-sc/g/12/c/34'), { page: 'campaign', tab: 'campaigns', net: 'taboola', account: 'acme-sc', group: '12', campaign: '34' });
  const r = route('/launch/taboola/acme-sc/g/12');
  assert.equal(r.page, 'tree');
  assert.equal(oldAddress(r), '/launch/campaigns?account=acme-sc&group=12');
  assert.equal(route('/launch/taboola').page, 'missing');
  assert.equal(route('/launch/taboola/acme-sc/x/1').page, 'missing');
});
