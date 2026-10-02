// Novos anúncios' matrix and the team's names (launch/web/pages/matrix.js).
import test from 'node:test';
import assert from 'node:assert/strict';

import { CHIPS, moreCTAs, letter, cell, matrixAds, toggle, forget, groupPrefix, campaignName, adName, namesLine, sheetName } from '../pages/matrix.js';

const rows = [{ id: 'h1' }, { id: 'h2' }, { id: 'h3' }];
const cols = [{ sha256: 'a' }, { sha256: 'b' }, { sha256: 'c' }, { sha256: 'd' }];

test('letters name the columns', () => {
  assert.deepEqual([0, 1, 25, 26, 27, 51, 52].map(letter), ['A', 'B', 'Z', 'AA', 'AB', 'AZ', 'BA']);
});

test('matrixAds: exactly the ticked cells, AD01… in reading order', () => {
  // The design's matrix: 8 ticked of 12.
  const ticked = new Set([cell('h1', 'a'), cell('h1', 'b'), cell('h1', 'c'), cell('h2', 'a'), cell('h2', 'c'), cell('h2', 'd'), cell('h3', 'b'), cell('h3', 'd')]);
  const ads = matrixAds(rows, cols, ticked);
  assert.equal(ads.length, 8);
  assert.deepEqual(ads.map((a) => [a.row, a.col, a.n]), [[0, 0, 1], [0, 1, 2], [0, 2, 3], [1, 0, 4], [1, 2, 5], [1, 3, 6], [2, 1, 7], [2, 3, 8]]);
  // A cell whose row or column is gone is no ad.
  ticked.add(cell('h9', 'a'));
  assert.equal(matrixAds(rows, cols, ticked).length, 8);
  forget(ticked, 'a');
  assert.deepEqual(matrixAds(rows, cols, ticked).map((a) => [a.row, a.col]), [[0, 1], [0, 2], [1, 2], [1, 3], [2, 1], [2, 3]]);
});

test('a header ticks its whole row or column, or unticks it when all are ticked', () => {
  const t = new Set([cell('h1', 'a')]);
  const row = cols.map((c) => cell('h1', c.sha256));
  toggle(t, row);
  assert.equal(t.size, 4);
  toggle(t, row);
  assert.equal(t.size, 0);
  toggle(t, rows.map((r) => cell(r.id, 'b')));
  assert.deepEqual(matrixAds(rows, cols, t).map((a) => a.col), [1, 1, 1]);
});

test('Botão: the chips and the rest under Mais', () => {
  assert.deepEqual(CHIPS.map(([v]) => v), ['Learn More', 'Read More', 'Shop Now', 'Get Offer', 'Sign Up', '']);
  assert.deepEqual(moreCTAs(['', 'Learn More', 'Buy Now', 'Get Offer', 'Download']), ['Buy Now', 'Download']);
});

test('names go down from the group', () => {
  assert.equal(groupPrefix('GRP03'), 'GRP03');
  assert.equal(groupPrefix('7'), 'GRP07');
  assert.equal(groupPrefix(' grp12 '), 'GRP12');
  assert.equal(groupPrefix('Memory  Loss'), 'Memory Loss');
  assert.equal(campaignName('GRP03', 1, 'desktop'), 'GRP03-CMP01-Desk-pp-bl');
  assert.equal(campaignName('GRP03', 12, 'mobile'), 'GRP03-CMP12-Mobile-pp-bl');
  assert.equal(adName('GRP03-CMP01-Desk-pp-bl', 1), 'GRP03-CMP01-AD01-Desk-pp-bl');
  assert.equal(adName('CMP04-1-Mobile-pp-bl', 12), 'CMP04-1-AD12-Mobile-pp-bl');
  assert.equal(adName('Memory Phones', 3), 'Memory Phones-AD03');
});

test('namesLine says the names the ads get', () => {
  assert.equal(namesLine(['GRP01-CMP01-Desk-pp-bl', 'GRP01-CMP01-Mobile-pp-bl'], 8), 'Nomes: GRP01-CMP01-AD01 a AD08, com -Desk-pp-bl e -Mobile-pp-bl');
  assert.equal(namesLine(['GRP01-CMP01-Desk-pp-bl'], 1), 'Nomes: GRP01-CMP01-AD01, com -Desk-pp-bl');
  assert.equal(namesLine(['GRP01-CMP01-Desk-pp-bl', 'GRP02-CMP04-Desk-pp-bl', 'Own'], 2), 'Nomes: GRP01-CMP01-AD01 a AD02, GRP02-CMP04-AD01 a AD02 e Own-AD01 a AD02, com -Desk-pp-bl');
  assert.equal(namesLine([], 3), '');
  assert.equal(namesLine(['GRP01-CMP01-Desk-pp-bl'], 0), '');
  assert.equal(sheetName(['GRP01-CMP01-Desk-pp-bl', 'GRP01-CMP01-Mobile-pp-bl'], 3), 'GRP01-CMP01-AD03');
  assert.equal(sheetName(['GRP01-CMP01-Desk-pp-bl', 'GRP02-CMP01-Desk-pp-bl'], 3), 'AD03');
  assert.equal(sheetName([], 3), 'AD03');
});
