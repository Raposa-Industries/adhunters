// adset: the plain-data part of Nova campanha's ads and settings, with no
// page in it, so node can test it (launch/web/test). Images are known by
// their sha256 and headlines by an id the page gives each one, so a pair or
// an edited ad keeps pointing at the same picture and line when the lists
// change.

// manualCombos turns the person's own pairs ("Um a um": this headline with
// this image) into [image, headline, cta] index triples over the chosen
// images I, headlines H and nCTAs buttons. Each pair goes out once per
// button. A pair whose image or headline is no longer chosen is left out.
export function manualCombos(pairs, I, H, nCTAs) {
  const out = [];
  for (const p of pairs || []) {
    const i = I.findIndex((x) => x.sha256 === p.img);
    const h = H.findIndex((x) => x.id === p.hl);
    if (i < 0 || h < 0) continue;
    for (let t = 0; t < nCTAs; t++) out.push([i, h, t]);
  }
  return out;
}

// pairsFrom starts the person's own pairs from image k with headline k, as
// many as the longer list (the shorter one starting over).
export function pairsFrom(I, H) {
  if (!I.length || !H.length) return [];
  const n = Math.max(I.length, H.length);
  return [...Array(n).keys()].map((k) => ({ img: I[k % I.length].sha256, hl: H[k % H.length].id }));
}

// reviewFrom freezes the ads made by a pairing into a list the person can
// edit card by card before sending: {img: sha256, hl: headline id or null,
// title, cta}.
export function reviewFrom(list) {
  return list.map((a) => ({ img: a.img.sha256, hl: a.hl ?? null, title: a.title, cta: a.cta }));
}

// reviewAds is the edited list as the ads the page sends: each card's
// picture by its sha256 among images (a picture taken out of the list
// stays, as far as the server keeps it), its own title and button. i and h
// are the picture's and headline's places in images and headlines (-1 when
// not there), for names in the bulk sheet. Cards without a picture or a
// title are left out.
export function reviewAds(review, images, headlines) {
  const out = [];
  for (const r of review || []) {
    const title = (r.title || '').replace(/[\r\n]+/g, ' ').trim();
    if (!r.img || !title) continue;
    const i = images.findIndex((x) => x.sha256 === r.img);
    const img = i >= 0 ? images[i] : { sha256: r.img, name: r.img.slice(0, 10) + '.jpg' };
    out.push({ img, i, h: headlines.findIndex((x) => x.id === r.hl), hl: r.hl, title, cta: r.cta ?? '' });
  }
  return out;
}

// repeats counts the ads that are the same picture, title and button as an
// earlier one (a warning: the person decides).
export function repeats(list) {
  const seen = new Set();
  let n = 0;
  for (const a of list) {
    const k = [a.img.sha256, a.title.toLowerCase(), a.cta].join('\n');
    if (seen.has(k)) n++;
    seen.add(k);
  }
  return n;
}

// MOBILE are the settings a pair's mobile campaign may have of its own; the
// rest are always the same as the desktop's.
export const MOBILE = ['bid_strategy', 'cpc', 'target_cpa', 'daily_cap', 'spending_limit', 'start_date'];

// mobileSettings is the mobile campaign's settings: the desktop's with the
// person's own values in over (empty ones keep the desktop's). A CPC goes
// only with a CPC bid and a target CPA only with Maximize conversions, the
// mobile's own bid kind when it has one. Nothing over: null, both the same.
export function mobileSettings(desktop, over) {
  if (!over) return null;
  const m = { ...desktop };
  const own = (k) => over[k] !== undefined && over[k] !== null && over[k] !== '';
  if (own('bid_strategy')) m.bid_strategy = over.bid_strategy;
  const maxConv = m.bid_strategy === 'MAX_CONVERSIONS';
  if (maxConv) m.cpc = 0;
  else m.target_cpa = 0;
  for (const k of MOBILE) {
    if (k === 'bid_strategy' || !own(k)) continue;
    if (k === 'cpc' && maxConv) continue;
    if (k === 'target_cpa' && !maxConv) continue;
    m[k] = over[k];
  }
  return m;
}

// CONVERSION_OBJECTIVES are the objectives Taboola allows Maximize
// conversions on.
const CONVERSION_OBJECTIVES = new Set(['LEADS_GENERATION', 'ONLINE_PURCHASES']);

// mobileProblem checks the mobile campaign's own values against the same
// ceilings as the desktop's (the server refuses them anyway); '' when fine.
export function mobileProblem(m, limits = {}) {
  if (!m) return '';
  for (const [k, name] of [['cpc', 'O CPC do mobile'], ['target_cpa', 'O CPA alvo do mobile'], ['daily_cap', 'O orçamento diário do mobile'], ['spending_limit', 'O limite total do mobile']]) {
    if (Number.isNaN(m[k])) return name + ' é um número, como 0,35.';
  }
  if (m.bid_strategy !== 'MAX_CONVERSIONS' && !(m.cpc > 0)) return 'Diga o CPC do mobile.';
  if (m.bid_strategy === 'MAX_CONVERSIONS' && m.objective && !CONVERSION_OBJECTIVES.has(m.objective)) return 'Maximizar conversões no mobile pede o objetivo Leads ou Compras.';
  if (limits.max_cpc && m.cpc > limits.max_cpc) return `O CPC do mobile vai até ${usd(limits.max_cpc)}.`;
  if (!(m.daily_cap > 0)) return 'Diga o orçamento diário do mobile.';
  if (limits.max_daily_cap && m.daily_cap > limits.max_daily_cap) return `O orçamento diário do mobile vai até ${usd(limits.max_daily_cap)}.`;
  if (limits.max_spend_limit && m.spending_limit > limits.max_spend_limit) return `O limite total do mobile vai até ${usd(limits.max_spend_limit)}.`;
  const total = m.spending_limit || limits.max_spend_limit || 0;
  if (total && m.daily_cap > total) return `O orçamento diário do mobile (${usd(m.daily_cap)}) passa do limite total (${usd(total)}): o Taboola recusa.`;
  if (m.start_date && m.end_date && m.end_date < m.start_date) return 'O mobile começaria depois de terminar.';
  return '';
}

function usd(v) {
  return 'US$ ' + Number(v).toFixed(2).replace('.', ',');
}
