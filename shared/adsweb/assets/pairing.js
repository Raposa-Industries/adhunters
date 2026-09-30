// Pairing: how the chosen creatives, headlines and CTAs become ads.
//
// mixed ("Sortido") makes as many ads as the longer list. Every creative and
// every headline is used at least once: the longer list goes in order, once
// each, and the shorter one repeats in rounds. 10 creatives and 5 headlines
// give 10 ads, each headline twice; 10 and 8 give 10 ads, two headlines twice.
// Because the longer list is used once each, no pair repeats.
//
// every ("Todas as combinações") is every creative with every headline.
// Manual pairs ("Um a um") are whatever the person ticks, kept by the app.

// mixed returns [creativeIndex, headlineIndex] pairs. With rand (a function
// returning [0, 1)), each round of the shorter list is shuffled, so which
// headline lands on which creative varies while every one is still used and
// the counts differ by at most one. Without rand the rounds go in order.
export function mixed(nCreatives, nHeadlines, rand) {
  if (nCreatives <= 0 || nHeadlines <= 0) return [];
  const n = Math.max(nCreatives, nHeadlines);
  const m = Math.min(nCreatives, nHeadlines);
  const seq = [];
  while (seq.length < n) {
    const round = [...Array(m).keys()];
    if (rand) shuffle(round, rand);
    seq.push(...round);
  }
  const pairs = [];
  for (let k = 0; k < n; k++) {
    pairs.push(nCreatives >= nHeadlines ? [k, seq[k]] : [seq[k], k]);
  }
  return pairs;
}

// every returns all nCreatives × nHeadlines pairs, creative by creative.
export function every(nCreatives, nHeadlines) {
  const pairs = [];
  for (let c = 0; c < nCreatives; c++) {
    for (let h = 0; h < nHeadlines; h++) pairs.push([c, h]);
  }
  return pairs;
}

// uses counts how many ads each creative and each headline is in.
export function uses(pairs, nCreatives, nHeadlines) {
  const creatives = new Array(nCreatives).fill(0);
  const headlines = new Array(nHeadlines).fill(0);
  for (const [c, h] of pairs) {
    creatives[c]++;
    headlines[h]++;
  }
  return { creatives, headlines };
}

// seeded returns a small deterministic generator (mulberry32), so a shuffle
// can be repeated from its seed.
export function seeded(seed) {
  let a = seed >>> 0;
  return function () {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function shuffle(list, rand) {
  for (let i = list.length - 1; i > 0; i--) {
    const j = Math.floor(rand() * (i + 1));
    [list[i], list[j]] = [list[j], list[i]];
  }
}

// The same three ways over any number of lists (creatives, headlines, CTAs):
// each ad is one index per list.

// mixedN makes as many ads as the longest list. The longest goes in order,
// once each, so no two ads are the same; every other list repeats in rounds,
// shuffled with rand, so each of its items is used and the counts differ by
// at most one.
export function mixedN(sizes, rand) {
  if (!sizes.length || sizes.some((n) => n <= 0)) return [];
  const n = Math.max(...sizes);
  const lead = sizes.indexOf(n);
  const seqs = sizes.map((m, i) => {
    const seq = [];
    while (seq.length < n) {
      const round = [...Array(m).keys()];
      if (rand && i !== lead) shuffle(round, rand);
      seq.push(...round);
    }
    return seq;
  });
  return [...Array(n).keys()].map((k) => seqs.map((seq) => seq[k]));
}

// everyN is every combination, the first list slowest.
export function everyN(sizes) {
  if (!sizes.length || sizes.some((n) => n <= 0)) return [];
  let out = [[]];
  for (const n of sizes) {
    const next = [];
    for (const partial of out) for (let i = 0; i < n; i++) next.push([...partial, i]);
    out = next;
  }
  return out;
}

// usesN counts how many ads each item of each list is in.
export function usesN(combos, sizes) {
  const counts = sizes.map((n) => new Array(n).fill(0));
  for (const combo of combos) combo.forEach((i, l) => counts[l][i]++);
  return counts;
}
