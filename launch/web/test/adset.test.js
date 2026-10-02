// Tests for Nova campanha's plain-data part: node --test launch/web/test/
import test from 'node:test';
import assert from 'node:assert/strict';

import { manualCombos, pairsFrom, reviewFrom, reviewAds, repeats, mobileSettings, mobileProblem } from '../pages/adset.js';

const I = [{ sha256: 'aaa1', name: 'a.jpg' }, { sha256: 'bbb2', name: 'b.jpg' }, { sha256: 'ccc3', name: 'c.jpg' }];
const H = [{ id: 'h1', text: 'One' }, { id: 'h2', text: 'Two' }];

test('pairsFrom: image k with headline k, the shorter list starting over', () => {
  assert.deepEqual(pairsFrom(I, H), [{ img: 'aaa1', hl: 'h1' }, { img: 'bbb2', hl: 'h2' }, { img: 'ccc3', hl: 'h1' }]);
  assert.deepEqual(pairsFrom([], H), []);
});

test("manualCombos: the person's pairs, once per button, leaving out what is no longer chosen", () => {
  const pairs = [{ img: 'ccc3', hl: 'h2' }, { img: 'aaa1', hl: 'h1' }, { img: 'gone', hl: 'h1' }, { img: 'bbb2', hl: 'hx' }];
  assert.deepEqual(manualCombos(pairs, I, H, 1), [[2, 1, 0], [0, 0, 0]]);
  assert.deepEqual(manualCombos(pairs, I, H, 2), [[2, 1, 0], [2, 1, 1], [0, 0, 0], [0, 0, 1]]);
  assert.deepEqual(manualCombos(undefined, I, H, 1), []);
});

test('review: swapping a picture or headline and taking a card out is exactly what is sent', () => {
  const made = [
    { img: I[0], hl: 'h1', title: 'One', cta: 'Learn More' },
    { img: I[1], hl: 'h2', title: 'Two', cta: 'Learn More' },
    { img: I[2], hl: 'h1', title: 'One', cta: 'Learn More' },
  ];
  const r = reviewFrom(made);
  assert.deepEqual(r[0], { img: 'aaa1', hl: 'h1', title: 'One', cta: 'Learn More' });
  r[0].img = 'ccc3'; // another picture
  r[1].hl = null; // a typed headline
  r[1].title = 'Typed\nby hand';
  r.splice(2, 1); // taken out
  const sent = reviewAds(r, I, H);
  assert.equal(sent.length, 2);
  assert.equal(sent[0].img, I[2]);
  assert.equal(sent[0].i, 2);
  assert.equal(sent[0].h, 0);
  assert.equal(sent[1].title, 'Typed by hand');
  assert.equal(sent[1].h, -1);
  // A picture later taken out of the list still goes, by its sha256.
  const kept = reviewAds([{ img: 'zzz9', hl: 'h1', title: 'One', cta: '' }], I, H);
  assert.equal(kept[0].img.sha256, 'zzz9');
  assert.equal(kept[0].i, -1);
  // Cards without a title or picture are not sent.
  assert.equal(reviewAds([{ img: 'aaa1', title: '  ' }, { img: '', title: 'x' }], I, H).length, 0);
});

test('repeats counts ads that are the same as an earlier one', () => {
  const a = { img: I[0], title: 'One', cta: '' };
  assert.equal(repeats([a, { ...a }, { ...a, title: 'ONE' }, { ...a, cta: 'Read More' }]), 2);
  assert.equal(repeats([]), 0);
});

test('mobileSettings: nothing asked keeps both alike; empty fields keep the desktop value', () => {
  const desk = { bid_strategy: 'FIXED', cpc: 0.4, target_cpa: 0, daily_cap: 20, start_date: '', brand: 'B', exclude_cities: ['3'] };
  assert.equal(mobileSettings(desk, null), null);
  const m = mobileSettings(desk, { cpc: 0.25, target_cpa: 5, daily_cap: '', start_date: '2030-01-05' });
  assert.equal(m.cpc, 0.25);
  assert.equal(m.target_cpa, 0, 'no target CPA with a CPC bid');
  assert.equal(m.daily_cap, 20);
  assert.equal(m.start_date, '2030-01-05');
  assert.equal(m.brand, 'B');
  assert.equal(desk.cpc, 0.4, 'the desktop settings are not changed');
  const conv = mobileSettings({ ...desk, bid_strategy: 'MAX_CONVERSIONS', cpc: 0 }, { cpc: 0.9, target_cpa: 7, daily_cap: 10 });
  assert.equal(conv.cpc, 0, 'no CPC with Maximize conversions');
  assert.equal(conv.target_cpa, 7);
  assert.equal(conv.daily_cap, 10);
});

test('mobileProblem: the mobile campaign meets the same ceilings', () => {
  const limits = { max_cpc: 1, max_daily_cap: 20, max_spend_limit: 20 };
  const ok = { bid_strategy: 'FIXED', cpc: 0.5, daily_cap: 15, spending_limit: 20 };
  assert.equal(mobileProblem(null, limits), '');
  assert.equal(mobileProblem(ok, limits), '');
  assert.match(mobileProblem({ ...ok, cpc: 1.5 }, limits), /CPC do mobile vai até/);
  assert.match(mobileProblem({ ...ok, daily_cap: 25 }, limits), /orçamento diário do mobile vai até/);
  assert.match(mobileProblem({ ...ok, daily_cap: 15, spending_limit: 10 }, limits), /passa do limite total/);
  assert.match(mobileProblem({ ...ok, cpc: NaN }, limits), /é um número/);
  assert.match(mobileProblem({ ...ok, cpc: 0 }, limits), /Diga o CPC/);
  assert.equal(mobileProblem({ bid_strategy: 'MAX_CONVERSIONS', cpc: 0, daily_cap: 20 }, limits), '');
  assert.match(mobileProblem({ ...ok, start_date: '2030-02-01', end_date: '2030-01-01' }, limits), /começaria depois/);
});
