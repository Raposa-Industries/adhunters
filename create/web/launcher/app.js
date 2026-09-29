// The launcher page: vertical and references, generate, choose images,
// headlines and CTAs, combine, brand, AI label, campaign, then CREATE on
// Taboola through create-web (or the Bulk Upload bundle when Taboola is not
// connected). Everything but generation and the Taboola calls runs in the
// browser.

import { mixedN, everyN, usesN, seeded } from "./pairing.js";
import { clean, hasHidden, headlineWarnings, blockedWarnings, imageWarnings, urlWarnings, looksAIMade } from "./checks.js";
import { zip } from "./zip.js";
import { AD_COLUMNS, CTAS, MAX_ADS, adRows, campaignIds, ctaType, uniqueNames, fingerprint, adId, tsv } from "./sheet.js";
import { readTemplate, fillTemplate } from "./template.js";
import { TRACKERS, splitLink } from "./tracking.js";

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const money = (usd) => `US$ ${usd.toFixed(usd < 0.1 ? 3 : 2).replace(".", ",")}`;
const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;

const VERTICALS = ["Blood Pressure", "Memory Loss", "Weight Loss", "Tinnitus", "Diabetes", "Neuropathy",
  "Prostate Health", "Joint Pain/Arthritis", "Vision/Eye Health"];
const MAX_REFS = 6;
const PREVIEW = 12;

let nextId = 1;
const state = {
  vertical: "",
  refs: [], // {id, name, blob, url, send}: analysed; with send, also given to the image model
  creatives: [], // {id, name, blob, url, width, height, size, type, fp, ai, aiLikely, source, chosen, brief, angle, pending, error}
  headlines: [], // {id, text, source, chosen}
  ctas: new Set(["Learn More"]),
  mode: "mixed",
  seed: 0,
  manual: new Set(), // "creativeId:headlineId"
  status: null,
  spent: 0,
  template: null,
  tb: { status: null, account: "", campaigns: [], groups: [], chosen: new Set(), mode: "existing", loading: false },
  tracker: "redtrack",
  showAll: false,
  creating: false,
};

// ---- saved fields (per browser, text only) -----------------------------------

const FIELDS = ["prompt", "examples", "ages", "n-images", "n-headlines", "campaigns", "link", "url", "tracking", "brand", "description",
  "nc-name", "nc-objective", "nc-bid", "nc-cpc", "nc-daily", "nc-total", "nc-countries", "nc-start", "nc-end", "ng-name", "ng-limit", "ng-model"];
const STORE = "adhunters-create-launcher";

function loadFields() {
  try {
    const saved = JSON.parse(localStorage.getItem(STORE) || "{}");
    for (const f of FIELDS) if (typeof saved[f] === "string") $(f).value = saved[f];
    if (VERTICALS.includes(saved.vertical)) state.vertical = saved.vertical;
    if (Array.isArray(saved.ctas)) state.ctas = new Set(saved.ctas.filter((c) => CTAS.includes(c)));
    if (typeof saved.account === "string") state.tb.account = saved.account;
    if (TRACKERS[saved.tracker]) state.tracker = saved.tracker;
  } catch { /* private window or blocked storage: start empty */ }
}

function saveFields() {
  try {
    const out = { vertical: state.vertical, ctas: [...state.ctas], account: state.tb.account, tracker: state.tracker };
    for (const f of FIELDS) out[f] = $(f).value;
    localStorage.setItem(STORE, JSON.stringify(out));
  } catch { /* not kept, nothing else depends on it */ }
}

// ---- server -------------------------------------------------------------------

async function readError(res) {
  try {
    const body = await res.json();
    if (body.error) return body.error;
  } catch { /* not JSON */ }
  return `erro ${res.status}`;
}

async function getJSON(path) {
  const res = await fetch(path);
  if (!res.ok) throw new Error(await readError(res));
  return res.json();
}

async function postJSON(path, body) {
  const res = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  if (!res.ok) throw new Error(await readError(res));
  return res.json();
}

async function postForm(path, form) {
  const res = await fetch(path, { method: "POST", body: form });
  if (!res.ok) throw new Error(await readError(res));
  return res.json();
}

async function loadStatus() {
  try {
    state.status = await getJSON("api/status");
  } catch {
    state.status = { generation: false, reason: "Esta página está sem o servidor do Create: envie suas próprias imagens e headlines." };
  }
  try {
    state.tb.status = await getJSON("api/taboola/status");
  } catch {
    state.tb.status = { connected: false, reason: "Sem conexão com o Taboola neste servidor." };
  }
  const on = state.status.generation;
  $("gen-status").textContent = on
    ? `OpenAI ${state.status.image_model}, cerca de ${money(state.status.image_cost_usd || 0)} por imagem.`
    : `Geração desligada. ${state.status.reason || ""}`;
  renderConnections();
  renderExamplesNote();
  renderTaboola();
  if (state.tb.status.connected) await loadCampaigns();
  renderHeadlines();
  update();
}

function renderConnections() {
  const gen = state.status?.generation;
  const tb = state.tb.status?.connected;
  $("connections").innerHTML =
    `<span class="pill${gen ? " on" : ""}" title="${esc(gen ? "" : state.status?.reason)}">OpenAI ${gen ? "ligada" : "desligada"}</span>` +
    `<span class="pill${tb ? " on" : ""}" title="${esc(tb ? "" : state.tb.status?.reason)}">Taboola ${tb ? "conectado" : "não conectado"}</span>`;
}

// ---- vertical ---------------------------------------------------------------------

function renderVerticals() {
  $("verticals").innerHTML = VERTICALS.map((v) => `<label class="chip"><input type="radio" name="vertical" value="${esc(v)}" ${v === state.vertical ? "checked" : ""}><span>${esc(v)}</span></label>`).join("");
}

$("verticals").addEventListener("change", (e) => {
  state.vertical = e.target.value;
  renderExamplesNote();
  update();
});

function renderExamplesNote() {
  const has = (state.status?.example_verticals || []).includes(state.vertical);
  $("examples-note").textContent = has
    ? `A IA também aprende com as headlines do time para ${state.vertical}; as suas pesam mais.`
    : "";
}

// ---- references -------------------------------------------------------------------

function addRefs(files) {
  for (const f of files) {
    if (!f.type.startsWith("image/") || state.refs.length >= MAX_REFS) continue;
    state.refs.push({ id: nextId++, name: f.name, blob: f, url: URL.createObjectURL(f), send: false });
  }
  renderRefs();
}

function renderRefs() {
  $("refs").innerHTML = state.refs.map((r) => `<figure data-id="${r.id}">
      <img src="${r.url}" alt="">
      <button class="x" data-act="remove-ref" title="Remover">×</button>
      <figcaption><label><input type="checkbox" data-act="send" ${r.send ? "checked" : ""}> Manter produto</label></figcaption>
    </figure>`).join("");
}

$("refs-input").addEventListener("change", (e) => {
  addRefs(e.target.files);
  e.target.value = "";
});

$("refs").addEventListener("click", (e) => {
  if (e.target.dataset?.act !== "remove-ref") return;
  e.preventDefault();
  const id = +e.target.closest("figure").dataset.id;
  const gone = state.refs.find((r) => r.id === id);
  if (gone) URL.revokeObjectURL(gone.url);
  state.refs = state.refs.filter((r) => r.id !== id);
  renderRefs();
});

$("refs").addEventListener("change", (e) => {
  if (e.target.dataset?.act !== "send") return;
  const r = state.refs.find((x) => x.id === +e.target.closest("figure").dataset.id);
  if (r) r.send = e.target.checked;
});

dropZone($("refs-drop"), addRefs);

function dropZone(el, onFiles) {
  el.addEventListener("dragover", (e) => {
    e.preventDefault();
    el.classList.add("over");
  });
  el.addEventListener("dragleave", () => el.classList.remove("over"));
  el.addEventListener("drop", (e) => {
    e.preventDefault();
    el.classList.remove("over");
    onFiles(e.dataTransfer.files);
  });
}

// ---- generation ----------------------------------------------------------------------

function dataURL(blob) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(r.result);
    r.onerror = () => reject(r.error);
    r.readAsDataURL(blob);
  });
}

// downscale keeps pictures small on the way to the server.
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

function progress(text, done, total) {
  $("gen-progress").textContent = text;
  $("gen-bar").hidden = total === undefined;
  if (total !== undefined) $("gen-bar").firstElementChild.style.width = `${total ? Math.round((100 * done) / total) : 0}%`;
}

async function generate() {
  if (!state.vertical) {
    progress("Escolha a vertical na etapa 1.");
    $("s-vertical").scrollIntoView();
    return;
  }
  const nImages = clamp(+$("n-images").value, 0, 12);
  const nHeadlines = clamp(+$("n-headlines").value, 0, 30);
  if (!nImages && !nHeadlines) {
    progress("Peça pelo menos uma imagem ou uma headline.");
    return;
  }
  $("generate").disabled = true;
  progress(state.refs.length ? "Analisando as referências e escrevendo…" : "Escrevendo headlines e ideias de imagem…", 0, nImages + 1);
  try {
    const keep = await Promise.all(state.refs.filter((r) => r.send).map((r) => downscale(r.blob)));
    const winners = await Promise.all(state.refs.map(async (r) => dataURL(await downscale(r.blob, 1024))));
    const plan = await postJSON("api/plan", {
      prompt: $("prompt").value.trim(),
      headline_examples: lines($("examples").value),
      language: "en",
      vertical: state.vertical,
      ages: $("ages").value.trim(),
      headlines: nHeadlines,
      images: nImages,
      has_references: keep.length > 0,
      winners,
      avoid: [...state.headlines.map((h) => h.text), ...state.creatives.filter((c) => c.brief).map((c) => c.brief)],
    });
    state.spent += plan.cost_usd || 0;
    if (plan.analysis?.length) renderAnalysis(plan.analysis);
    const known = new Set(state.headlines.map((h) => clean(h.text).toLowerCase()));
    for (const text of plan.headlines || []) {
      if (known.has(clean(text).toLowerCase())) continue;
      state.headlines.push({ id: nextId++, text, source: "generated", chosen: false });
    }
    renderHeadlines();
    const cards = (plan.briefs || []).map(({ brief, angle }) => {
      const c = { id: nextId++, source: "generated", brief, angle, pending: true, chosen: false, ai: true };
      state.creatives.push(c);
      return c;
    });
    renderCreatives();
    update();
    let done = 0;
    progress(`Gerando imagens: 0 de ${cards.length}…`, 1, cards.length + 1);
    await runQueue(cards, 3, async (c) => {
      await makeImage(c, keep);
      done++;
      progress(`Gerando imagens: ${done} de ${cards.length}…`, done + 1, cards.length + 1);
    });
    const failed = cards.filter((c) => c.error).length;
    progress(`Pronto${failed ? `, ${plural(failed, "imagem com erro", "imagens com erro")}` : ""}. Escolha abaixo.`);
    $("s-creatives").scrollIntoView();
  } catch (err) {
    progress(`Não deu: ${err.message}`);
  } finally {
    $("generate").disabled = !state.status?.generation;
    renderSpent();
  }
}

function renderAnalysis(rows) {
  $("analysis").innerHTML = `<h3>Padrão das referências</h3>
    <table class="analysis"><thead><tr><th>Aspecto</th><th>Fixo (o que faz funcionar)</th><th>Variável (pode mudar)</th></tr></thead>
    <tbody>${rows.map((a) => `<tr><th>${esc(a.aspect)}</th><td>${esc(a.fixed)}</td><td>${esc(a.variable)}</td></tr>`).join("")}</tbody></table>`;
}

async function makeImage(c, keep) {
  c.pending = true;
  c.error = "";
  renderCreatives();
  try {
    const form = new FormData();
    form.append("brief", c.brief);
    keep.forEach((blob, i) => form.append("reference", blob, `referencia-${i + 1}.jpg`));
    const out = await postForm("api/image", form);
    state.spent += out.cost_usd || 0;
    const mime = out.mime || "image/jpeg";
    await fill(c, base64Blob(out.image, mime), `gerada-${c.id}.${{ "image/png": "png", "image/webp": "webp" }[mime] || "jpg"}`);
  } catch (err) {
    c.error = err.message;
  }
  c.pending = false;
  renderCreatives();
  renderSpent();
  update();
}

async function runQueue(items, width, work) {
  let next = 0;
  const lane = async () => {
    while (next < items.length) await work(items[next++]);
  };
  await Promise.all(Array.from({ length: Math.min(width, items.length) }, lane));
}

function renderSpent() {
  $("dock-spent").textContent = state.spent ? `Gasto com IA nesta página: ${money(state.spent)}` : "";
}

// ---- creatives ---------------------------------------------------------------------------

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
  for (const f of [...files].filter((x) => x.type.startsWith("image/"))) {
    const c = { id: nextId++, source: "upload", chosen: true, pending: true };
    state.creatives.push(c);
    renderCreatives();
    await fill(c, f, f.name);
    c.pending = false;
  }
  renderCreatives();
  update();
}

function usable(c) {
  return !!c.blob && !c.pending && !(c.error && !c.width);
}

function renderCreatives() {
  const chosen = state.creatives.filter((c) => c.chosen && usable(c)).length;
  $("creatives-count").textContent = state.creatives.length ? `${chosen} de ${state.creatives.length} escolhidas` : "";
  $("creatives-empty").hidden = state.creatives.length > 0;
  $("creatives").innerHTML = state.creatives.map((c) => {
    const pic = c.url && !c.pending
      ? `<img class="pic" src="${c.url}" alt="">`
      : `<div class="pic pending">${c.pending ? "gerando" : "sem imagem"}</div>`;
    const warnings = usable(c) ? imageWarnings(c) : [];
    const tags = [
      c.angle ? `<span class="tag angle">${esc(c.angle)}</span>` : "",
      `<span class="tag">${c.source === "generated" ? "gerada com IA" : "enviada"}</span>`,
      c.source === "upload" && c.aiLikely ? `<span class="tag">parece feita com IA</span>` : "",
    ].join("");
    return `<div class="tile${usable(c) ? " pick" : ""}${c.chosen ? " chosen" : ""}" data-id="${c.id}">
      ${pic}
      ${usable(c) ? `<span class="tick">✓</span>` : ""}
      <div class="body">
        <div>${tags}</div>
        ${c.brief ? `<div class="brief" title="${esc(c.brief)}">${esc(c.brief)}</div>` : ""}
        <div class="meta">${c.width ? `${c.width}×${c.height}` : ""}${c.size ? ` · ${Math.round(c.size / 1024)} KB` : ""}</div>
        ${c.error ? `<div class="warn">${esc(c.error)}</div>` : ""}
        ${warnings.length ? `<ul class="warn">${warnings.map((w) => `<li>${esc(w)}</li>`).join("")}</ul>` : ""}
        <div class="row-actions">${c.source === "generated" && !c.pending ? `<button data-act="again">Gerar de novo</button>` : ""}<button data-act="remove" class="ghost">Remover</button></div>
      </div>
    </div>`;
  }).join("");
}

$("creatives").addEventListener("click", async (e) => {
  const tile = e.target.closest(".tile");
  if (!tile) return;
  const c = state.creatives.find((x) => x.id === +tile.dataset.id);
  if (!c) return;
  const act = e.target.closest("button")?.dataset.act;
  if (act === "remove") {
    if (c.url) URL.revokeObjectURL(c.url);
    state.creatives = state.creatives.filter((x) => x !== c);
  } else if (act === "again") {
    const keep = await Promise.all(state.refs.filter((r) => r.send).map((r) => downscale(r.blob)));
    await makeImage(c, keep);
    return;
  } else if (usable(c)) {
    c.chosen = !c.chosen;
  }
  renderCreatives();
  update();
});

// ---- headlines -----------------------------------------------------------------------------

function titleWarnings(text) {
  return [...headlineWarnings(text), ...blockedWarnings(text, state.status?.blocked)];
}

function chosenHeadlines() {
  return state.headlines.filter((h) => h.chosen && clean(h.text));
}

function renderHeadlines() {
  const chosen = chosenHeadlines();
  $("headlines-count").textContent = state.headlines.length ? `${chosen.length} de ${state.headlines.length} escolhidas` : "";
  $("headlines-empty").hidden = state.headlines.length > 0;
  $("headlines").innerHTML = state.headlines.map((h) => {
    const n = chosen.indexOf(h);
    return `<div class="hl${h.chosen ? " chosen" : ""}" data-id="${h.id}">
      <span class="tick">✓</span>
      <span class="num">${n >= 0 ? `H${n + 1}` : ""}</span>
      <div>
        <input type="text" data-act="edit" value="${esc(h.text)}">
        <div class="hint">${headlineHint(h)}</div>
      </div>
      <div class="side">${hasHidden(h.text) ? `<button data-act="clean">Tirar invisíveis</button>` : ""}<button data-act="remove" class="ghost">Remover</button></div>
    </div>`;
  }).join("");
}

function headlineHint(h) {
  const warnings = titleWarnings(h.text);
  const tag = h.source === "generated" ? "gerada com IA · " : "";
  return `${tag}${clean(h.text).length} caracteres` +
    (warnings.length ? `<ul class="warn">${warnings.map((w) => `<li>${esc(w)}</li>`).join("")}</ul>` : "");
}

$("headlines").addEventListener("input", (e) => {
  if (e.target.dataset?.act !== "edit") return;
  const row = e.target.closest(".hl");
  const h = state.headlines.find((x) => x.id === +row.dataset.id);
  h.text = e.target.value;
  row.querySelector(".hint").innerHTML = headlineHint(h);
  update();
});

$("headlines").addEventListener("change", (e) => {
  if (e.target.dataset?.act === "edit") renderHeadlines();
});

$("headlines").addEventListener("click", (e) => {
  const row = e.target.closest(".hl");
  if (!row || e.target.dataset?.act === "edit") return;
  const h = state.headlines.find((x) => x.id === +row.dataset.id);
  const act = e.target.closest("button")?.dataset.act;
  if (act === "clean") h.text = clean(h.text);
  else if (act === "remove") state.headlines = state.headlines.filter((x) => x !== h);
  else h.chosen = !h.chosen;
  renderHeadlines();
  update();
});

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

// ---- CTAs ------------------------------------------------------------------------------------

function renderCTAs() {
  $("ctas").innerHTML = CTAS.map((c) => `<label class="chip"><input type="checkbox" value="${esc(c)}" ${state.ctas.has(c) ? "checked" : ""}><span>${esc(c || "Sem botão")}</span></label>`).join("");
}

function chosenCTAs() {
  return CTAS.filter((c) => state.ctas.has(c));
}

$("ctas").addEventListener("change", (e) => {
  if (e.target.checked) state.ctas.add(e.target.value);
  else state.ctas.delete(e.target.value);
  update();
});

// ---- pairing ------------------------------------------------------------------------------------

function chosenCreatives() {
  return state.creatives.filter((c) => c.chosen && usable(c));
}

// combos is the list of [creative, headline, cta] the ads are made of.
function combos() {
  const C = chosenCreatives();
  const H = chosenHeadlines();
  const T = chosenCTAs();
  if (!C.length || !H.length || !T.length) return [];
  if (state.mode === "manual") {
    // Each ticked pair is one ad; the CTAs take turns over the pairs.
    const out = [];
    C.forEach((c) => H.forEach((h) => {
      if (state.manual.has(`${c.id}:${h.id}`)) out.push([c, h, T[out.length % T.length]]);
    }));
    return out;
  }
  const sizes = [C.length, H.length, T.length];
  const idx = state.mode === "every" ? everyN(sizes) : mixedN(sizes, state.seed ? seeded(state.seed) : null);
  return idx.map(([c, h, t]) => [C[c], H[h], T[t]]);
}

function setMode(mode) {
  if (mode === "manual" && state.mode !== "manual") {
    state.manual = new Set(combos().map(([c, h]) => `${c.id}:${h.id}`));
  }
  state.mode = mode;
  document.querySelector(`input[name=mode][value=${mode}]`).checked = true;
  update();
}

function renderPairing(list) {
  const C = chosenCreatives();
  const H = chosenHeadlines();
  const T = chosenCTAs();
  $("reshuffle").hidden = state.mode !== "mixed";
  const note = $("pairing-note");
  if (!C.length || !H.length || !T.length) {
    note.textContent = "Escolha pelo menos uma imagem, uma headline e um botão.";
    $("grid").innerHTML = "";
    return;
  }
  const idx = (arr) => new Map(arr.map((x, i) => [x, i]));
  const [iC, iH, iT] = [idx(C), idx(H), idx(T)];
  const [uC, uH, uT] = usesN(list.map(([c, h, t]) => [iC.get(c), iH.get(h), iT.get(t)]), [C.length, H.length, T.length]);
  const times = (n) => (n === 1 ? "1 vez" : `${n} vezes`);
  const range = (a) => (!a.length ? "" : Math.min(...a) === Math.max(...a) ? times(a[0]) : `${Math.min(...a)} a ${times(Math.max(...a))}`);
  const unused = [...uC, ...uH, ...uT].filter((n) => n === 0).length;
  note.textContent = `${plural(C.length, "imagem", "imagens")} × ${plural(H.length, "headline", "headlines")} × ${plural(T.length, "botão", "botões")} → ${plural(list.length, "anúncio", "anúncios")}. ` +
    `Cada imagem ${range(uC)}, cada headline ${range(uH)}, cada botão ${range(uT)}.` + (unused ? ` ${unused} sem uso.` : "");
  if (state.mode !== "manual") {
    $("grid").innerHTML = "";
    return;
  }
  const on = new Set(list.map(([c, h]) => `${c.id}:${h.id}`));
  const head = `<tr><th></th>${H.map((h, i) => `<th title="${esc(h.text)}">H${i + 1}</th>`).join("")}</tr>`;
  const rows = C.map((c) => `<tr><th class="row-head"><img src="${c.url}" alt=""></th>${H.map((h) => {
    const key = `${c.id}:${h.id}`;
    return `<td class="${on.has(key) ? "on" : ""}"><button data-key="${key}" title="${esc(h.text)}">${on.has(key) ? "✓" : ""}</button></td>`;
  }).join("")}</tr>`).join("");
  $("grid").innerHTML = head + rows;
}

$("grid").addEventListener("click", (e) => {
  const key = e.target.dataset?.key;
  if (!key) return;
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

// ---- Taboola --------------------------------------------------------------------------------------

function tbOn() {
  return !!state.tb.status?.connected;
}

function renderTaboola() {
  const on = tbOn();
  $("tb-on").hidden = !on;
  $("tb-off").hidden = on;
  $("create-box").hidden = !on;
  $("bundle").open = !on;
  $("tb-reason").textContent = on ? "" : `${state.tb.status?.reason || "Taboola não conectado."} Digite os IDs e baixe a planilha para o Bulk Upload na última etapa.`;
  if (!on) return;
  const accounts = state.tb.status.accounts || [];
  if (!accounts.some((a) => a.id === state.tb.account)) state.tb.account = accounts[0]?.id || "";
  $("tb-account").innerHTML = accounts.map((a) => `<option value="${esc(a.id)}" ${a.id === state.tb.account ? "selected" : ""}>${esc(a.name || a.id)}${a.name && a.name !== a.id ? ` (${esc(a.id)})` : ""}</option>`).join("");
  if (state.tb.status.only_own) {
    $("tb-reason").textContent = "";
  }
  $("tb-own").textContent = state.tb.status.only_own
    ? `Conta de testes: só aparecem e só recebem anúncios as campanhas criadas por esta página, com nome começando em "${state.tb.status.name_prefix}".`
    : "";
  $("tb-existing").hidden = state.tb.mode !== "existing";
  $("tb-new").hidden = state.tb.mode !== "new";
  renderCampaigns();
}

async function loadCampaigns() {
  if (!state.tb.account) return;
  state.tb.loading = true;
  renderCampaigns();
  const account = encodeURIComponent(state.tb.account);
  try {
    state.tb.campaigns = await getJSON(`api/taboola/campaigns?account=${account}`);
    state.tb.error = "";
  } catch (err) {
    state.tb.campaigns = [];
    state.tb.error = err.message;
  }
  try {
    state.tb.groups = await getJSON(`api/taboola/groups?account=${account}`);
  } catch {
    state.tb.groups = [];
  }
  renderNewCampaignLists();
  const ids = new Set(state.tb.campaigns.map((c) => String(c.id)));
  state.tb.chosen = new Set([...state.tb.chosen].filter((id) => ids.has(id)));
  state.tb.loading = false;
  renderCampaigns();
  update();
}

function renderCampaigns() {
  const box = $("tb-campaigns");
  if (state.tb.loading) {
    box.innerHTML = `<p class="empty">Carregando campanhas…</p>`;
    return;
  }
  if (state.tb.error) {
    box.innerHTML = `<p class="warn">Não deu para ler as campanhas: ${esc(state.tb.error)}</p>`;
    return;
  }
  const q = $("tb-search").value.trim().toLowerCase();
  const list = state.tb.campaigns.filter((c) => !q || `${c.name} ${c.id}`.toLowerCase().includes(q));
  if (!list.length) {
    box.innerHTML = `<p class="empty">${state.tb.campaigns.length ? "Nenhuma campanha com esse nome." : "Nenhuma campanha nesta conta. Crie uma nova acima."}</p>`;
    return;
  }
  box.innerHTML = list.map((c) => {
    const id = String(c.id);
    return `<div class="campaign${state.tb.chosen.has(id) ? " chosen" : ""}" data-id="${esc(id)}">
      <span class="tick">✓</span>
      <div><div class="name">${esc(c.name)}</div>
      <div class="meta">ID ${esc(id)}${c.branding_text ? ` · marca ${esc(c.branding_text)}` : ""}${c.cpc ? ` · lance ${money(+c.cpc)}` : ""}${c.daily_cap ? ` · ${money(+c.daily_cap)}/dia` : ""}</div></div>
      <span class="status ${esc(c.status || "")}">${esc(statusName(c.status))}</span>
    </div>`;
  }).join("");
}

// The lists inside the new-campaign form: campaigns to copy from, groups.
function renderNewCampaignLists() {
  const copy = $("nc-copy").value;
  $("nc-copy").innerHTML = `<option value="">Do zero</option>` +
    state.tb.campaigns.map((c) => `<option value="${esc(c.id)}" ${String(c.id) === copy ? "selected" : ""}>Copiar: ${esc(c.name)}</option>`).join("");
  const group = $("nc-group").value;
  $("nc-group").innerHTML = `<option value="">Automático (o Taboola cria um só para ela)</option>` +
    state.tb.groups.map((g) => `<option value="${esc(g.id)}" ${String(g.id) === group ? "selected" : ""}>${esc(g.name)}</option>`).join("") +
    `<option value="new" ${group === "new" ? "selected" : ""}>Novo grupo…</option>`;
  lockCopyGroup();
}

// A copy stays in its original's campaign group (Taboola sets the group only
// when a campaign is made, and the copy is made inside the original's), so
// the group is not a choice while copying.
function lockCopyGroup() {
  const copying = Boolean($("nc-copy").value);
  if (copying) $("nc-group").value = "";
  $("nc-group").disabled = copying;
  $("nc-group-copy").hidden = !copying;
  $("nc-group-new").hidden = $("nc-group").value !== "new";
}

function statusName(s) {
  return { RUNNING: "No ar", PAUSED: "Pausada", PENDING_APPROVAL: "Em revisão", DEPLETED: "Sem verba", EXPIRED: "Encerrada", REJECTED: "Rejeitada", FROZEN: "Congelada", PENDING_START_DATE: "Agendada", DEPLETED_MONTHLY: "Sem verba no mês" }[s] || s || "";
}

$("tb-campaigns").addEventListener("click", (e) => {
  const row = e.target.closest(".campaign");
  if (!row) return;
  const id = row.dataset.id;
  if (state.tb.chosen.has(id)) state.tb.chosen.delete(id);
  else state.tb.chosen.add(id);
  renderCampaigns();
  update();
});

$("tb-search").addEventListener("input", renderCampaigns);
$("tb-reload").addEventListener("click", loadCampaigns);
$("tb-account").addEventListener("change", (e) => {
  state.tb.account = e.target.value;
  state.tb.chosen.clear();
  loadCampaigns();
});
$("tb-modes").addEventListener("change", (e) => {
  state.tb.mode = e.target.value;
  renderTaboola();
  update();
});

function newCampaign() {
  const platforms = [...$("nc-platforms").querySelectorAll("input:checked")].map((i) => i.value);
  const group = $("nc-group").value;
  return {
    account: state.tb.account,
    copy_from: $("nc-copy").value,
    name: $("nc-name").value.trim(),
    brand: $("brand").value.trim(),
    marketing_objective: $("nc-objective").value,
    bid_strategy: $("nc-bid").value,
    cpc: +$("nc-cpc").value || 0,
    daily_cap: +$("nc-daily").value || 0,
    spending_limit: +$("nc-total").value || 0,
    countries: $("nc-countries").value.toUpperCase().match(/[A-Z]{2}/g) || [],
    platforms,
    tracking_code: $("tracking").value.trim(),
    start_date: $("nc-start").value,
    end_date: $("nc-end").value,
    group_id: group === "new" ? "" : group,
  };
}

function newGroup() {
  return {
    account: state.tb.account,
    name: $("ng-name").value.trim(),
    spending_limit: +$("ng-limit").value || 0,
    spending_limit_model: $("ng-model").value,
    marketing_objective: $("nc-objective").value,
  };
}

function newCampaignProblems() {
  const n = newCampaign();
  const max = state.tb.status || {};
  const out = [];
  const prefix = state.tb.status?.only_own ? state.tb.status.name_prefix || "" : "";
  if (!n.name) out.push("Dê um nome para a campanha.");
  else if (prefix && !n.name.startsWith(prefix)) out.push(`Nesta conta de testes o nome começa com "${prefix}".`);
  if ($("nc-group").value === "new") {
    const g = newGroup();
    if (!g.name) out.push("Dê um nome para o grupo novo.");
    else if (prefix && !g.name.startsWith(prefix)) out.push(`O nome do grupo também começa com "${prefix}".`);
    if (!(g.spending_limit > 0)) out.push("Falta a verba do grupo.");
  }
  if (n.start_date && n.end_date && n.end_date < n.start_date) out.push("A data de fim vem antes do começo.");
  if (!n.brand) out.push("Falta a marca (etapa 8): ela vai na campanha.");
  if (!(n.cpc > 0)) out.push("Falta o lance por clique.");
  else if (max.max_cpc && n.cpc > max.max_cpc) out.push(`O lance passa do limite deste servidor (${money(max.max_cpc)}).`);
  if (!(n.daily_cap > 0)) out.push("Falta o limite por dia.");
  else if (max.max_daily_cap && n.daily_cap > max.max_daily_cap) out.push(`O limite por dia passa do máximo deste servidor (${money(max.max_daily_cap)}).`);
  if (!n.countries.length) out.push("Escolha pelo menos um país.");
  if (!n.platforms.length) out.push("Escolha pelo menos um aparelho.");

  return out;
}

// ---- ads, review and create --------------------------------------------------------------------------

let adsRun = 0;
let currentAds = [];

async function buildAds(list) {
  const used = [...new Set(list.map(([c]) => c))];
  const names = uniqueNames(used.map((c) => c.name || "imagem.jpg"));
  const fileOf = new Map(used.map((c, i) => [c, names[i]]));
  const H = chosenHeadlines();
  const T = chosenCTAs();
  return Promise.all(list.map(async ([c, h, cta]) => {
    const file = fileOf.get(c);
    const title = clean(h.text);
    const suffix = T.length > 1 ? ` - ${cta || "sem botão"}` : "";
    return {
      creative: c,
      headline: h,
      cta,
      creativeFile: file,
      title,
      adName: `${file.replace(/\.[^.]+$/, "")} - H${H.indexOf(h) + 1}${suffix}`,
      customId: await adId(c.fp, title, T.length > 1 ? cta : ""),
    };
  }));
}

function aiAnswer() {
  return document.querySelector("input[name=ai]:checked")?.value || "";
}

function targetCampaigns() {
  if (!tbOn()) return campaignIds($("campaigns").value);
  return state.tb.mode === "new" ? ["nova"] : [...state.tb.chosen];
}

function settings() {
  return {
    campaigns: tbOn() ? [...state.tb.chosen] : campaignIds($("campaigns").value),
    url: $("url").value.trim(),
    description: $("description").value.trim(),
    ai: { yes: "Yes", no: "No" }[aiAnswer()] || "",
  };
}

async function renderAds(list) {
  const run = ++adsRun;
  const ads = await buildAds(list);
  if (run !== adsRun) return;
  currentAds = ads;
  const s = settings();
  const brand = $("brand").value.trim() || "Sua marca";
  const targets = targetCampaigns();
  $("ads-count").textContent = ads.length
    ? `${plural(ads.length, "anúncio", "anúncios")}${targets.length > 1 ? `, em cada uma das ${targets.length} campanhas` : ""}`
    : "";
  const shown = state.showAll ? ads : ads.slice(0, PREVIEW);
  $("ads").innerHTML = shown.map((a) => `<div class="tile">
      <img class="pic" src="${a.creative.url}" alt="">
      <div class="body">
        <div class="title">${esc(a.title)}</div>
        <div class="brand">${esc(brand)}</div>
        ${a.cta ? `<span class="cta">${esc(a.cta)}</span>` : ""}
      </div>
    </div>`).join("");
  $("ads-more").hidden = ads.length <= PREVIEW;
  $("ads-more").textContent = state.showAll ? "Mostrar menos" : `Ver todos os ${ads.length}`;

  const problems = [];
  const badHeadlines = new Set(ads.filter((a) => titleWarnings(a.headline.text).length).map((a) => a.headline));
  const badImages = new Set(ads.filter((a) => imageWarnings(a.creative).length).map((a) => a.creative));
  if (badHeadlines.size) problems.push(`${plural(badHeadlines.size, "headline tem", "headlines têm")} aviso das regras do Taboola (etapa 5).`);
  if (badImages.size) problems.push(`${plural(badImages.size, "imagem tem", "imagens têm")} aviso (etapa 4).`);
  for (const w of urlWarnings(s.url)) problems.push(w);
  // Brand: an existing campaign carries its own.
  if (tbOn() && state.tb.mode === "existing") {
    const other = state.tb.campaigns.filter((c) => state.tb.chosen.has(String(c.id)) && c.branding_text && $("brand").value.trim() && c.branding_text !== $("brand").value.trim());
    if (other.length) problems.push(`A marca que aparece é a da campanha no Taboola (${other.map((c) => c.branding_text).join(", ")}), não a da etapa 8.`);
  }
  $("warnings-summary").innerHTML = problems.length
    ? `<div class="summary-box warnings"><b>Avisos</b> <span>(não impedem: a decisão é sua)</span><ul>${problems.map((p) => `<li>${esc(p)}</li>`).join("")}</ul></div>`
    : "";
  $("url-warnings").textContent = $("url").value ? urlWarnings(s.url).join(" ") : "";
  $("description-warnings").textContent = blockedWarnings(s.description, state.status?.blocked, "description").join(" ");
  $("nc-warnings").innerHTML = tbOn() && state.tb.mode === "new" ? newCampaignProblems().map((p) => esc(p)).join("<br>") : "";

  // What still blocks CREATE (missing, not warnings).
  const missing = [];
  if (!ads.length) missing.push("monte pelo menos um anúncio");
  if (!aiAnswer()) missing.push("responda a pergunta sobre IA (etapa 9)");
  if (!/^https?:\/\/\S+\.\S+$/i.test(s.url)) missing.push("ponha o link da página (etapa 10)");
  if (tbOn()) {
    if (state.tb.mode === "existing" && !state.tb.chosen.size) missing.push("escolha uma campanha (etapa 10)");
    if (state.tb.mode === "new" && newCampaignProblems().length) missing.push("complete a nova campanha (etapa 10)");
  }
  if (ads.length > 500) missing.push("no máximo 500 anúncios por vez");
  const sent = state.sent === sentKey(ads);
  $("create").disabled = state.creating || sent || missing.length > 0;
  $("create").textContent = state.tb.mode === "new" && tbOn()
    ? `CRIAR CAMPANHA E ${plural(ads.length, "ANÚNCIO", "ANÚNCIOS")}`
    : `CRIAR ${plural(ads.length * Math.max(1, targets.length), "ANÚNCIO", "ANÚNCIOS")} NO TABOOLA`;
  $("create-note").textContent = state.creating ? "" : missing.length ? `Falta: ${missing.join("; ")}.`
    : sent ? "Estes anúncios já foram criados. Mude algo para criar de novo."
    : "Tudo é criado pausado. Nada roda até você ligar no painel do Taboola.";

  const readyBundle = ads.length > 0 && !!aiAnswer();
  for (const id of ["download-zip", "copy-rows"]) $(id).disabled = !readyBundle;
  $("download-sheet").disabled = !readyBundle || !state.template;
  $("download-note").textContent = !readyBundle ? "Monte os anúncios e responda a pergunta sobre IA."
    : !state.template ? "Escolha o modelo do Taboola acima para baixar a planilha."
    : ads.length > MAX_ADS ? `O modelo tem espaço para ${MAX_ADS} anúncios: divida em mais de uma planilha.` : "";

  renderDock(ads.length, targets.length);
}

$("ads-more").addEventListener("click", () => {
  state.showAll = !state.showAll;
  update();
});

// sentKey names one CREATE: the same ads into the same campaigns, so a
// second press does not make them twice.
function sentKey(ads) {
  return `${state.tb.account}|${[...state.tb.chosen].sort().join(",")}|${ads.map((a) => a.customId).join(",")}`;
}

function renderAI(list) {
  const answer = aiAnswer();
  const aiImages = new Set(list.filter(([c]) => c.ai || c.aiLikely).map(([c]) => c)).size;
  const aiHeadlines = new Set(list.filter(([, h]) => h.source === "generated").map(([, h]) => h)).size;
  let warning = "";
  if (answer === "no" && (aiImages || aiHeadlines)) {
    const parts = [];
    if (aiImages) parts.push(plural(aiImages, "imagem feita (ou que parece feita) com IA", "imagens feitas (ou que parecem feitas) com IA"));
    if (aiHeadlines) parts.push(plural(aiHeadlines, "headline escrita com IA", "headlines escritas com IA"));
    warning = `Há ${parts.join(" e ")}. O Taboola pede para declarar conteúdo de IA e pode rejeitar anúncios ou advertir a conta. A escolha é sua.`;
  }
  $("ai-warning").textContent = warning;
}

function renderDock(nAds, nTargets) {
  const C = chosenCreatives().length;
  const H = chosenHeadlines().length;
  const T = chosenCTAs().length;
  $("dock-summary").innerHTML = `${plural(C, "imagem", "imagens")} · ${plural(H, "headline", "headlines")} · ${plural(T, "botão", "botões")} <span class="eq">→ ${plural(nAds, "anúncio", "anúncios")}</span>` +
    (nTargets > 1 ? ` <span class="muted">× ${nTargets} campanhas</span>` : "");
}

function renderRail() {
  const done = {
    "s-vertical": !!state.vertical,
    "s-refs": state.refs.length > 0 || lines($("examples").value).length > 0,
    "s-generate": state.creatives.some((c) => c.source === "generated") || state.headlines.some((h) => h.source === "generated"),
    "s-creatives": chosenCreatives().length > 0,
    "s-headlines": chosenHeadlines().length > 0,
    "s-ctas": chosenCTAs().length > 0,
    "s-pairing": currentAds.length > 0,
    "s-brand": !!$("brand").value.trim(),
    "s-ai": !!aiAnswer(),
    "s-campaign": tbOn() ? (state.tb.mode === "new" ? !newCampaignProblems().length : state.tb.chosen.size > 0) && !!$("url").value.trim() : !!$("url").value.trim(),
    "s-review": false,
  };
  $("rail").innerHTML = [...document.querySelectorAll("section.step")].map((s, i) =>
    `<li class="${done[s.id] ? "done" : ""}"><a href="#${s.id}"><span class="dot">${done[s.id] ? "✓" : i + 1}</span>${esc(s.dataset.title)}</a></li>`).join("");
}

function update() {
  renderTracking();
  const list = combos();
  renderPairing(list);
  renderAI(list);
  renderAds(list).then(renderRail);
  saveFields();
}

// create sends the ads to Taboola: the new campaign first when there is
// one, then every image once and the ads into each campaign.
$("create").addEventListener("click", async () => {
  if (state.creating) return;
  const ads = currentAds;
  const box = $("create-result");
  state.creating = true;
  $("create").disabled = true;
  box.innerHTML = "";
  try {
    let campaigns = [...state.tb.chosen];
    if (state.tb.mode === "new") {
      const body = newCampaign();
      if ($("nc-group").value === "new") {
        $("create-note").textContent = "Criando o grupo de campanhas…";
        const group = await postJSON("api/taboola/groups", newGroup());
        body.group_id = String(group.id);
        box.innerHTML += `<div class="result ok">Grupo criado: <b>${esc(group.name)}</b> (ID ${esc(group.id)}), pausado.</div>`;
      }
      $("create-note").textContent = "Criando a campanha…";
      const made = await postJSON("api/taboola/campaigns", body);
      campaigns = [String(made.id)];
      box.innerHTML += `<div class="result ok">Campanha criada, pausada: <b>${esc(made.name)}</b> (ID ${esc(made.id)}).</div>`;
      await loadCampaigns();
      state.tb.mode = "existing";
      document.querySelector("input[name=tb-mode][value=existing]").checked = true;
      state.tb.chosen = new Set(campaigns);
      renderTaboola();
    }
    $("create-note").textContent = `Enviando ${plural(new Set(ads.map((a) => a.creative)).size, "imagem", "imagens")} e ${plural(ads.length, "anúncio", "anúncios")}…`;
    const used = [...new Set(ads.map((a) => a.creative))];
    const form = new FormData();
    form.append("account", state.tb.account);
    form.append("campaigns", JSON.stringify(campaigns));
    const s = settings();
    form.append("ads", JSON.stringify(ads.map((a) => ({
      image: used.indexOf(a.creative),
      title: a.title,
      description: s.description,
      cta: ctaType(a.cta),
      url: s.url,
      custom_id: a.customId,
      ai: aiAnswer() === "yes",
    }))));
    ads.length && used.forEach((c, i) => form.append("image", c.blob, uniqueNames(used.map((x) => x.name || "imagem.jpg"))[i]));
    const out = await postForm("api/taboola/ads", form);
    if ((out.results || []).every((r) => !r.error)) state.sent = sentKey(ads);
    const byId = new Map(state.tb.campaigns.map((c) => [String(c.id), c.name]));
    box.innerHTML += (out.results || []).map((r) => r.error
      ? `<div class="result fail"><b>${esc(byId.get(String(r.campaign_id)) || r.campaign_id)}</b>: ${esc(r.error)}${r.created?.length ? ` (${r.created.length} criados antes do erro)` : ""}</div>`
      : `<div class="result ok"><b>${esc(byId.get(String(r.campaign_id)) || r.campaign_id)}</b>: ${plural(r.created.length, "anúncio criado", "anúncios criados")}, pausados e em revisão. Ligue no painel do Taboola quando quiser.</div>`).join("");
    $("create-note").textContent = "";
  } catch (err) {
    box.innerHTML += `<div class="result fail">Não deu: ${esc(err.message)}</div>`;
    $("create-note").textContent = "";
  } finally {
    state.creating = false;
    update();
  }
});

// ---- the Bulk Upload bundle (Taboola not connected, or by choice) ---------------------------------------

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
  $("download-note").textContent = `${rows().length} linhas copiadas: cole na aba Ads do modelo do Taboola, na célula A3.`;
});

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

// ---- wiring ----------------------------------------------------------------------------------------------

function lines(text) {
  return text.split("\n").map((l) => l.trim()).filter(Boolean);
}

function clamp(n, lo, hi) {
  return Math.max(lo, Math.min(hi, Number.isFinite(n) ? Math.round(n) : lo));
}

$("upload-input").addEventListener("change", (e) => {
  addUploads(e.target.files);
  e.target.value = "";
});
dropZone($("s-creatives"), addUploads);

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
for (const id of FIELDS) {
  $(id).addEventListener("input", update);
  $(id).addEventListener("change", update);
}
$("nc-platforms").addEventListener("change", update);
$("nc-group").addEventListener("change", () => {
  $("nc-group-new").hidden = $("nc-group").value !== "new";
  update();
});
$("nc-copy").addEventListener("change", () => {
  lockCopyGroup();
  const from = state.tb.campaigns.find((c) => String(c.id) === $("nc-copy").value);
  if (from) {
    if (from.tracking_code && !$("tracking").value.trim()) $("tracking").value = from.tracking_code;
    if (from.cpc) $("nc-cpc").value = from.cpc;
    if (from.daily_cap) $("nc-daily").value = from.daily_cap;
    if (from.bid_strategy === "FIXED" || from.bid_strategy === "SMART") $("nc-bid").value = from.bid_strategy;
    if (from.marketing_objective) $("nc-objective").value = from.marketing_objective;
  }
  update();
});

// ---- tracker and link ----------------------------------------------------------------------------------

function renderTrackers() {
  $("trackers").innerHTML = Object.entries(TRACKERS).map(([k, t]) => `<label class="chip"><input type="radio" name="tracker" value="${k}" ${k === state.tracker ? "checked" : ""}><span>${esc(t.name)}</span></label>`).join("");
  renderTracking();
}

// renderTracking says where the tracking code comes from: the new
// campaign's field, or the chosen campaigns' own.
function renderTracking() {
  const t = TRACKERS[state.tracker];
  $("tracker-hint").textContent = t.hint;
  const newOne = !tbOn() || state.tb.mode === "new";
  $("tracking-field").hidden = !newOne || state.tracker === "none";
  const chosen = state.tb.campaigns.filter((c) => state.tb.chosen.has(String(c.id)));
  let note = "";
  if (!newOne && chosen.length) {
    const missing = chosen.filter((c) => !c.tracking_code);
    note = missing.length && state.tracker !== "none"
      ? `Sem tracking code no Taboola: ${missing.map((c) => c.name).join(", ")}. Coloque no painel da campanha, senão o tracker não recebe os dados do clique.`
      : `As campanhas escolhidas já têm tracking code no Taboola; ele vale para os anúncios novos.`;
  } else if (!tbOn() && state.tracker !== "none") {
    note = "Com o Bulk Upload, ponha o tracking code no painel da campanha (Tracking › Tracking code).";
  }
  $("tracking-note").textContent = note;
}

$("trackers").addEventListener("change", (e) => {
  state.tracker = e.target.value;
  const t = TRACKERS[state.tracker];
  if (state.tracker === "none") $("tracking").value = "";
  else if (t.template && !$("tracking").value.trim()) $("tracking").value = t.template;
  renderTracking();
  update();
});

// A pasted link is split: the address into every ad, the {macros} into the
// tracking code.
$("link").addEventListener("input", () => {
  const { url, tracking } = splitLink($("link").value);
  $("url").value = url;
  if (tracking) $("tracking").value = tracking;
  else if (TRACKERS[state.tracker].template && !$("tracking").value.trim()) $("tracking").value = TRACKERS[state.tracker].template;
  update();
});
$("ai-label").addEventListener("change", update);

loadFields();
renderVerticals();
renderRefs();
renderCTAs();
renderCreatives();
renderHeadlines();
renderTaboola();
renderTrackers();
update();
loadStatus();
loadTemplate();
