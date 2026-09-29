// The launcher page: brief, generate, choose, pair, then the bundle for
// Taboola's bulk upload. Everything but generation runs in the browser, so
// the page also works as plain files with no server (generation off).

import { mixed, every, uses, seeded } from "./pairing.js";
import { clean, hasHidden, headlineWarnings, imageWarnings, urlWarnings, looksAIMade } from "./checks.js";
import { zip } from "./zip.js";
import { AD_COLUMNS, CTAS, MAX_ADS, adRows, campaignIds, uniqueNames, fingerprint, adId, tsv } from "./sheet.js";
import { readTemplate, fillTemplate } from "./template.js";

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const money = (usd) => `US$ ${usd.toFixed(usd < 0.1 ? 3 : 2).replace(".", ",")}`;

let nextId = 1;
const state = {
  creatives: [], // {id, name, blob, url, width, height, size, type, fp, ai, aiLikely, source, chosen, brief, pending, error}
  headlines: [], // {id, text, source, chosen}
  refs: [], // {id, name, blob, url}
  mode: "mixed",
  seed: 0, // 0 = rounds in order
  manual: new Set(), // "creativeId:headlineId"
  status: null,
  spent: 0,
  template: null, // {name, bytes, account}
};

// ---- saved fields (per browser, only text) ---------------------------------

const FIELDS = ["prompt", "examples", "language", "vertical", "n-images", "n-headlines", "campaigns", "url", "cta", "brand", "description"];
const STORE = "adhunters-create-launcher";

function loadFields() {
  try {
    const saved = JSON.parse(localStorage.getItem(STORE) || "{}");
    for (const f of FIELDS) if (typeof saved[f] === "string") $(f).value = saved[f];
  } catch { /* private window or blocked storage: start empty */ }
}

function saveFields() {
  try {
    const out = {};
    for (const f of FIELDS) out[f] = $(f).value;
    localStorage.setItem(STORE, JSON.stringify(out));
  } catch { /* not kept, nothing else depends on it */ }
}

// ---- server ------------------------------------------------------------------

async function loadStatus() {
  try {
    const res = await fetch("api/status");
    if (!res.ok) throw new Error(String(res.status));
    state.status = await res.json();
  } catch {
    state.status = { generation: false, reason: "Esta página está sem o servidor do Create: envie suas próprias imagens e headlines." };
  }
  const on = state.status.generation;
  $("gen-status").textContent = on
    ? `Gera com a OpenAI (${state.status.image_model}, cerca de ${money(state.status.image_cost_usd || 0)} por imagem; headlines com ${state.status.text_model}).`
    : `Geração desligada. ${state.status.reason || ""}`;
  for (const el of $("gen-form").querySelectorAll("input, textarea, select, button")) el.disabled = !on;
}

async function readError(res) {
  try {
    const body = await res.json();
    if (body.error) return body.error;
  } catch { /* not JSON */ }
  return `erro ${res.status}`;
}

async function postJSON(path, body) {
  const res = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  if (!res.ok) throw new Error(await readError(res));
  return res.json();
}

async function postImage(brief, refs) {
  const form = new FormData();
  form.append("brief", brief);
  refs.forEach((blob, i) => form.append("reference", blob, `referencia-${i + 1}.jpg`));
  const res = await fetch("api/image", { method: "POST", body: form });
  if (!res.ok) throw new Error(await readError(res));
  return res.json();
}

// downscale keeps reference pictures small on the way to the server.
async function downscale(blob, max = 1536) {
  const bmp = await createImageBitmap(blob);
  const scale = Math.min(1, max / Math.max(bmp.width, bmp.height));
  const canvas = document.createElement("canvas");
  canvas.width = Math.round(bmp.width * scale);
  canvas.height = Math.round(bmp.height * scale);
  canvas.getContext("2d").drawImage(bmp, 0, 0, canvas.width, canvas.height);
  bmp.close?.();
  return new Promise((resolve) => canvas.toBlob(resolve, "image/jpeg", 0.9));
}

function base64Blob(b64, type) {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return new Blob([bytes], { type });
}

// ---- generation ----------------------------------------------------------------

async function generate() {
  const prompt = $("prompt").value.trim();
  if (!prompt) {
    $("gen-progress").textContent = "Escreva do que é o anúncio.";
    return;
  }
  const nImages = clamp(+$("n-images").value, 0, 12);
  const nHeadlines = clamp(+$("n-headlines").value, 0, 30);
  $("generate").disabled = true;
  progress("Escrevendo headlines e ideias de imagem…");
  try {
    const refs = await Promise.all(state.refs.map((r) => downscale(r.blob)));
    const plan = await postJSON("api/plan", {
      prompt,
      headline_examples: lines($("examples").value),
      language: $("language").value,
      vertical: $("vertical").value,
      headlines: nHeadlines,
      images: nImages,
      has_references: refs.length > 0,
      avoid: [...state.headlines.map((h) => h.text), ...state.creatives.filter((c) => c.brief).map((c) => c.brief)],
    });
    state.spent += plan.cost_usd || 0;
    const known = new Set(state.headlines.map((h) => clean(h.text).toLowerCase()));
    for (const text of plan.headlines || []) {
      if (known.has(clean(text).toLowerCase())) continue;
      state.headlines.push({ id: nextId++, text, source: "generated", chosen: false });
    }
    renderHeadlines();
    const cards = (plan.briefs || []).map((brief) => {
      const c = { id: nextId++, source: "generated", brief, pending: true, chosen: false, ai: true };
      state.creatives.push(c);
      return c;
    });
    renderCreatives();
    update();
    await runQueue(cards, 3, (c, done) => {
      progress(`Gerando imagens: ${done} de ${cards.length} prontas…`);
      return makeImage(c, refs);
    });
    const failed = cards.filter((c) => c.error).length;
    progress(`Pronto${failed ? `, ${failed} imagem(ns) com erro` : ""}. Gasto nesta página: ${money(state.spent)}.`);
  } catch (err) {
    progress(`Não deu: ${err.message}`);
  } finally {
    $("generate").disabled = !state.status?.generation;
  }
}

async function makeImage(c, refs) {
  c.pending = true;
  c.error = "";
  renderCreatives();
  try {
    const out = await postImage(c.brief, refs);
    state.spent += out.cost_usd || 0;
    const mime = out.mime || "image/jpeg";
    const blob = base64Blob(out.image, mime);
    await fill(c, blob, `gerada-${c.id}.${{ "image/png": "png", "image/webp": "webp" }[mime] || "jpg"}`);
  } catch (err) {
    c.error = err.message;
  }
  c.pending = false;
  renderCreatives();
  update();
}

async function runQueue(items, width, work) {
  let next = 0;
  let done = 0;
  const lane = async () => {
    while (next < items.length) {
      const item = items[next++];
      await work(item, done);
      done++;
    }
  };
  await Promise.all(Array.from({ length: Math.min(width, items.length) }, lane));
}

// ---- creatives -------------------------------------------------------------------

async function fill(c, blob, name) {
  if (c.url) URL.revokeObjectURL(c.url);
  Object.assign(c, { blob, name, url: URL.createObjectURL(blob), size: blob.size, type: blob.type });
  const bytes = new Uint8Array(await blob.arrayBuffer());
  c.fp = await fingerprint(bytes);
  if (c.source === "upload") c.aiLikely = looksAIMade(bytes);
  try {
    const bmp = await createImageBitmap(blob);
    c.width = bmp.width;
    c.height = bmp.height;
    bmp.close?.();
  } catch {
    c.error = "Não deu para ler esta imagem.";
  }
}

async function addUploads(files) {
  const images = [...files].filter((f) => f.type.startsWith("image/"));
  for (const f of images) {
    const c = { id: nextId++, source: "upload", chosen: true, pending: true };
    state.creatives.push(c);
    await fill(c, f, f.name);
    c.pending = false;
  }
  renderCreatives();
  update();
}

function renderCreatives() {
  const chosen = state.creatives.filter((c) => c.chosen && usable(c)).length;
  $("creatives-count").textContent = state.creatives.length ? `${chosen} de ${state.creatives.length} marcadas` : "";
  $("creatives").innerHTML = state.creatives.map((c) => {
    const pic = c.url && !c.pending
      ? `<img class="pic" src="${c.url}" alt="">`
      : `<div class="pic pending">${c.pending ? "gerando…" : "sem imagem"}</div>`;
    const warnings = usable(c) ? imageWarnings(c) : [];
    const tags = [
      c.source === "generated" ? "gerada com IA" : "enviada",
      c.source === "upload" && c.aiLikely ? "parece feita com IA" : "",
    ].filter(Boolean).map((t) => `<span class="tag">${t}</span>`).join("");
    return `<div class="card${c.chosen ? " chosen" : ""}" data-id="${c.id}">
      ${pic}
      <label><input type="checkbox" data-act="choose" ${c.chosen ? "checked" : ""} ${usable(c) ? "" : "disabled"}> Usar</label>
      <div>${tags}</div>
      <div class="meta">${esc(c.name || "")}${c.width ? ` · ${c.width}×${c.height}` : ""}${c.size ? ` · ${Math.round(c.size / 1024)} KB` : ""}</div>
      ${c.brief ? `<div class="meta" title="${esc(c.brief)}">Ideia: ${esc(c.brief.slice(0, 140))}${c.brief.length > 140 ? "…" : ""}</div>` : ""}
      ${c.error ? `<p class="warn">${esc(c.error)}</p>` : ""}
      ${warnings.length ? `<ul class="warn">${warnings.map((w) => `<li>${esc(w)}</li>`).join("")}</ul>` : ""}
      <p>${c.source === "generated" && !c.pending ? `<button data-act="again">Gerar de novo</button>` : ""}<button data-act="remove">Remover</button></p>
    </div>`;
  }).join("");
}

function usable(c) {
  return !!c.blob && !c.pending && !(c.error && !c.width);
}

$("creatives").addEventListener("click", async (e) => {
  const act = e.target.dataset?.act;
  const card = e.target.closest(".card");
  if (!act || !card) return;
  const c = state.creatives.find((x) => x.id === +card.dataset.id);
  if (!c) return;
  if (act === "choose") {
    c.chosen = e.target.checked;
    renderCreatives();
    update();
  } else if (act === "remove") {
    if (c.url) URL.revokeObjectURL(c.url);
    state.creatives = state.creatives.filter((x) => x !== c);
    renderCreatives();
    update();
  } else if (act === "again") {
    const refs = await Promise.all(state.refs.map((r) => downscale(r.blob)));
    await makeImage(c, refs);
    progress(`Gasto nesta página: ${money(state.spent)}.`);
  }
});

// ---- headlines ---------------------------------------------------------------------

function chosenHeadlines() {
  return state.headlines.filter((h) => h.chosen && clean(h.text));
}

function renderHeadlines() {
  const chosen = chosenHeadlines();
  $("headlines-count").textContent = state.headlines.length ? `${chosen.length} de ${state.headlines.length} marcadas` : "";
  $("headlines").innerHTML = state.headlines.map((h) => {
    const n = chosen.indexOf(h);
    return `<div class="headline${h.chosen ? " chosen" : ""}" data-id="${h.id}">
      <input type="checkbox" data-act="choose" ${h.chosen ? "checked" : ""} title="Usar">
      <span class="num">${n >= 0 ? `H${n + 1}` : ""}</span>
      <div>
        <input type="text" data-act="edit" value="${esc(h.text)}">
        <div class="hint">${headlineHint(h)}</div>
      </div>
      <div>${hasHidden(h.text) ? `<button data-act="clean">Tirar invisíveis</button>` : ""}<button data-act="remove">Remover</button></div>
    </div>`;
  }).join("");
}

function headlineHint(h) {
  const warnings = headlineWarnings(h.text);
  const tag = h.source === "generated" ? "gerada com IA · " : "";
  return `<span class="muted">${tag}${clean(h.text).length} caracteres</span>` +
    (warnings.length ? `<ul class="warn">${warnings.map((w) => `<li>${esc(w)}</li>`).join("")}</ul>` : "");
}

$("headlines").addEventListener("input", (e) => {
  if (e.target.dataset?.act !== "edit") return;
  const row = e.target.closest(".headline");
  const h = state.headlines.find((x) => x.id === +row.dataset.id);
  h.text = e.target.value;
  row.querySelector(".hint").innerHTML = headlineHint(h);
  update();
});

$("headlines").addEventListener("change", (e) => {
  const act = e.target.dataset?.act;
  if (act === "edit") renderHeadlines();
  if (act !== "choose") return;
  const h = state.headlines.find((x) => x.id === +e.target.closest(".headline").dataset.id);
  h.chosen = e.target.checked;
  renderHeadlines();
  update();
});

$("headlines").addEventListener("click", (e) => {
  const act = e.target.dataset?.act;
  if (act !== "clean" && act !== "remove") return;
  const h = state.headlines.find((x) => x.id === +e.target.closest(".headline").dataset.id);
  if (act === "clean") h.text = clean(h.text);
  else state.headlines = state.headlines.filter((x) => x !== h);
  renderHeadlines();
  update();
});

// ---- pairing --------------------------------------------------------------------------

function chosenCreatives() {
  return state.creatives.filter((c) => c.chosen && usable(c));
}

// pairs is the list of [creative, headline] the bundle will hold.
function pairs() {
  const C = chosenCreatives();
  const H = chosenHeadlines();
  if (state.mode === "manual") {
    const out = [];
    C.forEach((c) => H.forEach((h) => {
      if (state.manual.has(`${c.id}:${h.id}`)) out.push([c, h]);
    }));
    return out;
  }
  const idx = state.mode === "every" ? every(C.length, H.length) : mixed(C.length, H.length, state.seed ? seeded(state.seed) : null);
  return idx.map(([c, h]) => [C[c], H[h]]);
}

function setMode(mode) {
  if (mode === "manual" && state.mode !== "manual") {
    state.manual = new Set(pairs().map(([c, h]) => `${c.id}:${h.id}`));
  }
  state.mode = mode;
  document.querySelector(`input[name=mode][value=${mode}]`).checked = true;
  update();
}

function renderPairing(list) {
  const C = chosenCreatives();
  const H = chosenHeadlines();
  $("reshuffle").hidden = state.mode !== "mixed";
  const note = $("pairing-note");
  if (!C.length || !H.length) {
    note.textContent = "Marque pelo menos uma imagem e uma headline.";
    $("grid").innerHTML = "";
    return;
  }
  const idxC = new Map(C.map((c, i) => [c.id, i]));
  const idxH = new Map(H.map((h, i) => [h.id, i]));
  const u = uses(list.map(([c, h]) => [idxC.get(c.id), idxH.get(h.id)]), C.length, H.length);
  const times = (n) => (n === 1 ? "1 vez" : `${n} vezes`);
  const range = (a) => (Math.min(...a) === Math.max(...a) ? times(a[0]) : `${Math.min(...a)} a ${times(Math.max(...a))}`);
  const unused = u.creatives.filter((n) => n === 0).length + u.headlines.filter((n) => n === 0).length;
  const explain = {
    mixed: "Sortido: a lista maior entra uma vez cada e a menor se repete até completar.",
    manual: "Um a um: clique nos quadrados para ligar ou desligar cada par.",
    every: "Todas as combinações: cada imagem com cada headline.",
  }[state.mode];
  note.textContent = `${C.length} imagens × ${H.length} headlines → ${list.length} anúncios. Cada imagem ${range(u.creatives)}, cada headline ${range(u.headlines)}.` +
    (unused ? ` ${unused} sem uso.` : "") + ` ${explain}`;
  const on = new Set(list.map(([c, h]) => `${c.id}:${h.id}`));
  const head = `<tr><th></th>${H.map((h, i) => `<th title="${esc(h.text)}">H${i + 1}<br><span class="muted">${u.headlines[i]}×</span></th>`).join("")}</tr>`;
  const rows = C.map((c, ci) => `<tr><th class="row-head"><img src="${c.url}" alt="">${esc(c.name)} <span class="muted">${u.creatives[ci]}×</span></th>${H.map((h) => {
    const key = `${c.id}:${h.id}`;
    return `<td class="${on.has(key) ? "on" : ""}"><button data-key="${key}" title="${esc(h.text)}">${on.has(key) ? "✓" : ""}</button></td>`;
  }).join("")}</tr>`).join("");
  $("grid").innerHTML = head + rows;
}

$("grid").addEventListener("click", (e) => {
  const key = e.target.dataset?.key;
  if (!key) return;
  if (state.mode !== "manual") setMode("manual");
  if (state.manual.has(key)) state.manual.delete(key);
  else state.manual.add(key);
  update();
});

$("modes").addEventListener("change", (e) => {
  if (e.target.name === "mode") setMode(e.target.value);
});

$("reshuffle").addEventListener("click", () => {
  state.seed = 1 + Math.floor(Math.random() * 2 ** 31);
  update();
});

// ---- ads, warnings and the bundle -------------------------------------------------------

let adsRun = 0;
let currentAds = [];

// buildAds names each creative's file in the ZIP and gives every ad its id.
async function buildAds(list) {
  const used = [...new Set(list.map(([c]) => c))];
  const names = uniqueNames(used.map((c) => c.name || "imagem.jpg"));
  const fileOf = new Map(used.map((c, i) => [c, names[i]]));
  const H = chosenHeadlines();
  return Promise.all(list.map(async ([c, h]) => {
    const file = fileOf.get(c);
    const title = clean(h.text);
    return {
      creative: c,
      headline: h,
      creativeFile: file,
      title,
      adName: `${file.replace(/\.[^.]+$/, "")} - H${H.indexOf(h) + 1}`,
      customId: await adId(c.fp, title),
    };
  }));
}

function settings() {
  return {
    campaigns: campaignIds($("campaigns").value),
    url: $("url").value.trim(),
    cta: $("cta").value,
    description: $("description").value.trim(),
    ai: { yes: "Yes", no: "No" }[aiAnswer()] || "",
  };
}

function aiAnswer() {
  return document.querySelector("input[name=ai]:checked")?.value || "";
}

async function renderAds(list) {
  const run = ++adsRun;
  const ads = await buildAds(list);
  if (run !== adsRun) return;
  currentAds = ads;
  const s = settings();
  const brand = $("brand").value.trim() || "Sua marca";
  $("ads-count").textContent = ads.length
    ? `${ads.length} anúncios${s.campaigns.length > 1 ? `, cada um nas ${s.campaigns.length} campanhas` : ""}`
    : "";
  $("ads").innerHTML = ads.map((a, i) => `<div class="card">
      <img class="pic" src="${a.creative.url}" alt="">
      <div class="title">${esc(a.title)}</div>
      <div class="brand">${esc(brand)}</div>
      <div class="meta">#${i + 1} · ${esc(a.creativeFile)} · ${esc(a.customId)}</div>
    </div>`).join("");

  const problems = [];
  const badHeadlines = new Set(ads.filter((a) => headlineWarnings(a.headline.text).length).map((a) => a.headline));
  const badImages = new Set(ads.filter((a) => imageWarnings(a.creative).length).map((a) => a.creative));
  if (badHeadlines.size) problems.push(`${badHeadlines.size} headline(s) com aviso das regras do Taboola (veja a seção 3).`);
  if (badImages.size) problems.push(`${badImages.size} imagem(ns) com aviso (veja a seção 2).`);
  if (!s.campaigns.length) problems.push("Sem ID de campanha: a coluna Campaign ID vai vazia.");
  for (const w of urlWarnings(s.url)) problems.push(w);
  if (!aiAnswer()) problems.push("Responda se os anúncios serão marcados como feitos com IA.");
  if (ads.length > MAX_ADS) problems.push(`${ads.length} anúncios: o modelo do Taboola tem espaço para ${MAX_ADS}. Divida em mais de uma planilha.`);
  $("warnings-summary").innerHTML = problems.length ? `<ul class="warn">${problems.map((p) => `<li>${esc(p)}</li>`).join("")}</ul>` : "";
  $("url-warnings").textContent = $("url").value ? urlWarnings(s.url).join(" ") : "";

  const ready = ads.length > 0 && !!aiAnswer();
  for (const id of ["download-zip", "copy-rows"]) $(id).disabled = !ready;
  $("download-sheet").disabled = !ready || !state.template;
  $("download-note").textContent = !ads.length ? "Monte pelo menos um anúncio."
    : !aiAnswer() ? "Responda a pergunta sobre IA na seção 5."
    : !state.template ? "Escolha o modelo do Taboola acima para baixar a planilha."
    : "Os avisos não impedem: a decisão é sua.";
}

function renderAI(list) {
  const answer = aiAnswer();
  const aiImages = new Set(list.filter(([c]) => c.ai || c.aiLikely).map(([c]) => c)).size;
  const aiHeadlines = new Set(list.filter(([, h]) => h.source === "generated").map(([, h]) => h)).size;
  let warning = "";
  if (answer === "no" && (aiImages || aiHeadlines)) {
    const parts = [];
    if (aiImages) parts.push(`${aiImages} imagem(ns) feita(s) ou que parece(m) feita(s) com IA`);
    if (aiHeadlines) parts.push(`${aiHeadlines} headline(s) escrita(s) com IA sobre saúde`);
    warning = `Há ${parts.join(" e ")}. O Taboola pede para declarar conteúdo de IA e pode rejeitar anúncios ou advertir a conta. A escolha é sua.`;
  }
  $("ai-warning").textContent = warning;
}

function update() {
  const list = pairs();
  renderPairing(list);
  renderAI(list);
  renderAds(list);
  saveFields();
}

function stamp() {
  const d = new Date();
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
}

function save(blob, name) {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 10000);
}

function rows() {
  return adRows(currentAds, settings());
}

$("download-sheet").addEventListener("click", async () => {
  try {
    const out = await fillTemplate(state.template.bytes, AD_COLUMNS, rows());
    save(new Blob([out], { type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" }), `taboola-anuncios-${stamp()}.xlsx`);
  } catch (err) {
    $("download-note").textContent = `Não deu para montar a planilha: ${err.message}`;
  }
});

// ---- Realize's template (kept in this browser) ----------------------------

const DB = "adhunters-create";

function idb(mode, work) {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB, 1);
    req.onupgradeneeded = () => req.result.createObjectStore("files");
    req.onerror = () => reject(req.error);
    req.onsuccess = () => {
      const tx = req.result.transaction("files", mode);
      const r = work(tx.objectStore("files"));
      tx.oncomplete = () => resolve(r.result);
      tx.onerror = () => reject(tx.error);
    };
  });
}

async function useTemplate(name, bytes, keep) {
  try {
    const t = await readTemplate(bytes);
    state.template = { name, bytes, account: t.account };
    $("template-status").textContent = `Modelo: ${name}${t.account ? ` (conta ${t.account})` : ""}. As campanhas precisam ser dessa conta.`;
    if (keep) await idb("readwrite", (s) => s.put({ name, bytes }, "template")).catch(() => {});
  } catch (err) {
    state.template = null;
    $("template-status").textContent = err.message;
  }
  update();
}

$("template-input").addEventListener("change", async (e) => {
  const f = e.target.files[0];
  if (f) await useTemplate(f.name, new Uint8Array(await f.arrayBuffer()), true);
  e.target.value = "";
});

async function loadTemplate() {
  try {
    const saved = await idb("readonly", (s) => s.get("template"));
    if (saved?.bytes) await useTemplate(saved.name, new Uint8Array(saved.bytes), false);
  } catch { /* no storage here: the person picks the file again */ }
}

$("download-zip").addEventListener("click", async () => {
  const seen = new Map();
  for (const a of currentAds) if (!seen.has(a.creativeFile)) seen.set(a.creativeFile, a.creative.blob);
  const files = [];
  for (const [name, blob] of seen) files.push({ name, data: new Uint8Array(await blob.arrayBuffer()) });
  save(new Blob(zip(files), { type: "application/zip" }), `taboola-imagens-${stamp()}.zip`);
});

$("copy-rows").addEventListener("click", async () => {
  const text = tsv(rows());
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const t = document.createElement("textarea");
    t.value = text;
    document.body.append(t);
    t.select();
    document.execCommand("copy");
    t.remove();
  }
  $("download-note").textContent = `${currentAds.length ? rows().length : 0} linhas copiadas: cole na aba Ads do modelo do Taboola, na célula A3.`;
});

// ---- wiring --------------------------------------------------------------------------------

function progress(text) {
  $("gen-progress").textContent = text;
}

function lines(text) {
  return text.split("\n").map((l) => l.trim()).filter(Boolean);
}

function clamp(n, lo, hi) {
  return Math.max(lo, Math.min(hi, Number.isFinite(n) ? Math.round(n) : lo));
}

function renderRefs() {
  $("refs").innerHTML = state.refs.map((r) => `<figure data-id="${r.id}"><img src="${r.url}" alt=""><figcaption>${esc(r.name)} <button data-act="remove-ref">×</button></figcaption></figure>`).join("");
}

$("refs-input").addEventListener("change", (e) => {
  for (const f of e.target.files) {
    if (state.refs.length >= 6) break;
    state.refs.push({ id: nextId++, name: f.name, blob: f, url: URL.createObjectURL(f) });
  }
  e.target.value = "";
  renderRefs();
});

$("refs").addEventListener("click", (e) => {
  if (e.target.dataset?.act !== "remove-ref") return;
  const id = +e.target.closest("figure").dataset.id;
  state.refs = state.refs.filter((r) => r.id !== id);
  renderRefs();
});

$("upload-input").addEventListener("change", (e) => {
  addUploads(e.target.files);
  e.target.value = "";
});

for (const zone of [$("creatives-section")]) {
  zone.addEventListener("dragover", (e) => e.preventDefault());
  zone.addEventListener("drop", (e) => {
    e.preventDefault();
    addUploads(e.dataTransfer.files);
  });
}

$("add-headlines").addEventListener("click", () => {
  const known = new Set(state.headlines.map((h) => clean(h.text).toLowerCase()));
  for (const text of lines($("paste-headlines").value)) {
    if (known.has(clean(text).toLowerCase())) continue;
    known.add(clean(text).toLowerCase());
    state.headlines.push({ id: nextId++, text, source: "typed", chosen: true });
  }
  $("paste-headlines").value = "";
  renderHeadlines();
  update();
});

const setAll = (list, value) => () => {
  for (const x of list()) x.chosen = value;
  renderCreatives();
  renderHeadlines();
  update();
};
$("creatives-all").addEventListener("click", setAll(() => state.creatives.filter(usable), true));
$("creatives-none").addEventListener("click", setAll(() => state.creatives, false));
$("headlines-all").addEventListener("click", setAll(() => state.headlines, true));
$("headlines-none").addEventListener("click", setAll(() => state.headlines, false));

$("generate").addEventListener("click", generate);
for (const id of ["campaigns", "url", "cta", "brand", "description", "prompt", "examples", "language", "vertical", "n-images", "n-headlines"]) {
  $(id).addEventListener("input", update);
  $(id).addEventListener("change", update);
}
$("ai-label").addEventListener("change", update);

$("cta").innerHTML = CTAS.map((c) => `<option value="${c}">${c || "Sem botão"}</option>`).join("");
loadFields();
renderCreatives();
renderHeadlines();
update();
loadStatus();
loadTemplate();
