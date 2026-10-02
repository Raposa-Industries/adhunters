// Create's page parts (create/internal/site/pages/static/parts.js).
import test from 'node:test';
import assert from 'node:assert/strict';

import * as P from '../pages/static/parts.js';

const folders = {
  totals: { creatives: 9, original: 3, generated: 6, headlines: 4 },
  verticals: [
    { id: 'memory-loss', name: 'Memory Loss', creatives: 7, headlines: 4,
      platforms: [{ id: 'taboola', name: 'Taboola', creatives: 5, headlines: 4 }],
      sets: [
        { id: 2, name: 'Sofá', vertical_id: 'memory-loss', platform: 'taboola', creatives: 2 },
        { id: 1, name: 'Colher', vertical_id: 'memory-loss', platform: 'taboola', creatives: 3 },
        { id: 3, name: 'Solta', vertical_id: 'memory-loss', platform: '', creatives: 2 },
        { id: 4, name: 'Notícia', vertical_id: 'memory-loss', platform: 'newsbreak', creatives: 0 },
      ] },
    { id: 'sleep', name: 'Sleep', creatives: 0, headlines: 0, platforms: [], sets: [] },
    { id: 'tinnitus', name: 'Tinnitus', creatives: 2, headlines: 0, platforms: [], sets: [] },
  ],
};

test('whenLabel says hoje, ontem, then dd/mm', () => {
  const now = new Date(2026, 9, 2, 10, 0);
  assert.equal(P.whenLabel(new Date(2026, 9, 2, 0, 5), now), 'hoje');
  assert.equal(P.whenLabel(new Date(2026, 9, 1, 23, 59), now), 'ontem');
  assert.equal(P.whenLabel(new Date(2026, 8, 28, 12), now), '28/09');
  assert.equal(P.hhmm(new Date(2026, 9, 2, 9, 5)), '09:05');
});

test('toggleRef adds, takes out, and stops at the most', () => {
  let r = P.toggleRef([], { type: 'creative', id: 1 });
  assert.equal(r.on, true);
  assert.equal(r.refs.length, 1);
  r = P.toggleRef(r.refs, { type: 'creative', id: 1, name: 'again' });
  assert.equal(r.on, false);
  assert.equal(r.refs.length, 0);
  const full = P.toggleRef([{ type: 'item', id: 1 }, { type: 'item', id: 2 }], { type: 'headline', id: 3 }, 2);
  assert.equal(full.full, true);
  assert.equal(full.refs.length, 2);
  // An item and a creative with the same id are different references.
  assert.notEqual(P.refKey({ type: 'item', id: 5 }), P.refKey({ type: 'creative', id: 5 }));
});

test('library references travel in an address and back', () => {
  const refs = [{ type: 'creative', id: 12 }, { type: 'item', id: 3 }, { type: 'headline', id: 5 }];
  assert.equal(P.refsParam(refs), 'c12,h5');
  assert.deepEqual(P.parseRefs('c12,h5,x9,c,h1e3, c7'), [{ type: 'creative', id: 12 }, { type: 'headline', id: 5 }, { type: 'creative', id: 7 }]);
});

test('libraryItemFor finds the session item already holding a reference', () => {
  const items = [
    { id: 1, kind: 'image', origin: 'library', state: 'done', library_ref: '12' },
    { id: 2, kind: 'headline', origin: 'library', state: 'done', library_ref: '12' },
  ];
  assert.equal(P.libraryItemFor(items, { type: 'creative', id: 12 }).id, 1);
  assert.equal(P.libraryItemFor(items, { type: 'headline', id: 12 }).id, 2);
  assert.equal(P.libraryItemFor(items, { type: 'creative', id: 13 }), null);
});

test('answers, saves, names and counts', () => {
  assert.equal(P.answerLabel(4, 3), 'Create · 4 imagens e 3 headlines');
  assert.equal(P.answerLabel(1, 0), 'Create · 1 imagem');
  const saved = P.savedIn([
    { state: 'done', item_ids: [1, 2], set_name: 'Colher' },
    { state: 'failed', item_ids: [3], set_name: 'X' },
    { state: 'done', item_ids: [2], set_name: 'Sofá' },
  ]);
  assert.equal(saved.get(1), 'Colher');
  assert.equal(saved.get(2), 'Sofá');
  assert.equal(saved.has(3), false);
  assert.equal(P.sessionNameFrom('mesma cena / mais quente, colher na mesa com luz'), 'Mesma cena mais quente, colher na');
  assert.match(P.sessionNameFrom('  ', new Date(2026, 9, 2, 14, 30)), /^Conversa 02\/10 14:30$/);
  assert.deepEqual(P.defaultCounts(['headline']), { images: 0, headlines: 5 });
  assert.deepEqual(P.defaultCounts(['image', 'headline']), { images: 4, headlines: 5 });
});

test('origins: originals are uploaded or from Drive', () => {
  assert.equal(P.isOriginal({ origin: 'upload' }), true);
  assert.equal(P.isOriginal({ origin: 'create' }), false);
  assert.equal(P.originParam('original'), 'upload,drive');
  assert.equal(P.originParam('generated'), 'create');
  assert.equal(P.originParam('all'), '');
});

test('folder keys go both ways', () => {
  for (const f of [P.ROOT, { vertical: 'sleep', platform: '', set: 0 }, { vertical: 'memory-loss', platform: 'taboola', set: 0 }]) {
    assert.deepEqual(P.parseFolderKey(P.folderKey(f), folders), f);
  }
  assert.deepEqual(P.parseFolderKey('s:1', folders), { vertical: 'memory-loss', platform: 'taboola', set: 1 });
  assert.deepEqual(P.parseFolderKey('s:99', folders), P.ROOT);
  assert.deepEqual(P.parseFolderKey('p:memory-loss:facebook', folders), P.ROOT);
  assert.deepEqual(P.folderPath(folders, { vertical: 'memory-loss', platform: 'taboola', set: 1 }), ['Memory Loss', 'Taboola', 'Colher']);
  assert.deepEqual(P.folderPath(folders, { vertical: 'new-one', platform: '', set: 0 }, { 'new-one': 'New One' }), ['New One']);
});

test('listQuery: a set wins over its folders', () => {
  assert.equal(P.listQuery({ vertical: 'memory-loss', platform: 'taboola', set: 1 }, { origin: 'original', q: ' colher ', sort: 'name' }),
    'set=1&origin=upload%2Cdrive&q=colher&sort=name&limit=120');
  assert.equal(P.listQuery({ vertical: 'memory-loss', platform: 'taboola', set: 0 }, { sort: 'new', limit: 500 }),
    'vertical=memory-loss&platform=taboola&limit=500');
});

test('tree: verticals with something, platforms, then sets by name', () => {
  const t = P.tree(folders);
  assert.deepEqual(t.map((n) => n.label), ['Memory Loss', 'Tinnitus']);
  const ml = t[0];
  assert.deepEqual(ml.kids.map((n) => n.label), ['Taboola', 'NewsBreak', 'Solta']);
  assert.deepEqual(ml.kids[0].kids.map((n) => n.label), ['Colher', 'Sofá']);
  assert.equal(ml.kids[0].kids[0].key, 's:1');
  // A vertical kept on purpose shows even when empty.
  assert.deepEqual(P.tree(folders, ['sleep']).map((n) => n.label), ['Memory Loss', 'Sleep', 'Tinnitus']);
  const found = P.filterTree(t, 'colh');
  assert.deepEqual(found.map((n) => n.label), ['Memory Loss']);
  assert.deepEqual(found[0].kids.map((n) => n.label), ['Taboola']);
  assert.deepEqual(found[0].kids[0].kids.map((n) => n.label), ['Colher']);
});

test('sameVertical and launchLabel', () => {
  assert.equal(P.sameVertical([{ vertical_id: 'sleep' }, { vertical_id: 'sleep' }, {}]), 'sleep');
  assert.equal(P.sameVertical([{}]), '');
  assert.equal(P.sameVertical([{ vertical_id: 'sleep' }, { vertical_id: 'tinnitus' }]), null);
  assert.equal(P.launchLabel(0), '');
  assert.equal(P.launchLabel(1), 'no Launch: 1 anúncio');
  assert.equal(P.launchLabel(3), 'no Launch: 3 anúncios');
});
