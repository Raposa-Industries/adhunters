// Trackers and landing links. Taboola escapes {macros} in an ad's URL, so a
// tracker link pasted whole is split: the address goes in every ad, the
// parameters in the campaign's tracking code, which Taboola appends to the
// ad's URL and fills per click (write tests, 2026-09-29).

// The trackers the page knows. template is the tracking code to start from
// when a person gives only an address; null means paste the whole link.
export const TRACKERS = {
  redtrack: {
    name: "RedTrack",
    // RedTrack's Taboola preset, as the sandbox campaign made it on
    // 2026-09-29 (research/taboola-api/write-tests-2026-09-29/funnel).
    template: "sub1={campaign_id}&utm_source=Taboola&utm_campaign={campaign_name}&sub4={campaign_item_id}&sub5={site}&sub6={title}&sub7={platform}&sub8={site_id}&sub9={thumbnail}&sub10={timestamp}&ref_id={click_id}",
    hint: "Cole o link da campanha no RedTrack (Campaigns › o link de tracking com os parâmetros do Taboola).",
  },
  voluum: {
    name: "Voluum",
    template: null,
    hint: "Cole o link da campanha no Voluum, com os parâmetros do Taboola.",
  },
  other: {
    name: "Outro",
    template: null,
    hint: "Cole o link do seu tracker. Os {macros} vão para o tracking code.",
  },
  none: {
    name: "Sem tracker",
    template: null,
    hint: "O link direto da página. Sem tracker, os resultados só aparecem no Taboola.",
  },
};

// splitLink separates a pasted link into the address every ad uses and the
// parameters that belong in the tracking code. Only parameters with a
// {macro} move; plain ones (an id the tracker needs) stay in the address.
export function splitLink(link) {
  const text = (link || "").trim();
  const q = text.indexOf("?");
  if (q < 0) return { url: text, tracking: "" };
  const base = text.slice(0, q);
  const [query, hash] = text.slice(q + 1).split("#");
  const keep = [];
  const move = [];
  for (const part of query.split("&").filter(Boolean)) (/[{}]/.test(part) ? move : keep).push(part);
  return {
    url: base + (keep.length ? `?${keep.join("&")}` : "") + (hash ? `#${hash}` : ""),
    tracking: move.join("&"),
  };
}
