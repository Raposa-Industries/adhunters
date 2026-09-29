// Filling a Realize template. Realize reads a bulk upload only in the shape
// of the template it hands out (Create › Bulk Upload › Download Template):
// hidden METADATA tabs, notes in row 1, headers in row 2. So the page never
// makes a workbook of its own: it starts from Realize's (the built-in base,
// realize-base.xlsx, made by tools/realize-base.mjs, or one the person
// picked), writes the ads into the Ads tab by header name, and, for the base,
// the account's part that Realize would have written. Every other file stays
// byte for byte as Realize wrote it.

import { unzip, inflate, zip, concat } from "./zip.js";

const decoder = new TextDecoder();
const encoder = new TextEncoder();

function unescapeXml(s) {
  return s.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&amp;/g, "&");
}

function escapeXml(s) {
  return String(s)
    .replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F￾￿]/g, "")
    .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function columnNumber(letters) {
  let n = 0;
  for (const ch of letters) n = n * 26 + ch.charCodeAt(0) - 64;
  return n;
}

// sheetPath finds the file of the tab named name (ignoring case and spaces).
// With exact, the name must match as written: Realize has both "Campaign
// Groups " (the bulk tab) and "Campaign Groups" (the list).
function sheetPath(workbook, rels, name, exact = false) {
  const want = exact ? name : name.trim().toLowerCase();
  for (const m of workbook.matchAll(/<sheet\b[^>]*>/g)) {
    const tag = m[0];
    const tabName = unescapeXml(tag.match(/\bname="([^"]*)"/)?.[1] || "");
    if ((exact ? tabName : tabName.trim().toLowerCase()) !== want) continue;
    const id = tag.match(/\br:id="([^"]*)"/)?.[1];
    for (const r of rels.matchAll(/<Relationship\b[^>]*>/g)) {
      if (r[0].match(/\bId="([^"]*)"/)?.[1] !== id) continue;
      const target = r[0].match(/\bTarget="([^"]*)"/)[1];
      return target.startsWith("/") ? target.slice(1) : `xl/${target}`;
    }
  }
  return null;
}

function sharedStrings(xml) {
  if (!xml) return [];
  return [...xml.matchAll(/<si>([\s\S]*?)<\/si>/g)].map((m) =>
    unescapeXml([...m[1].matchAll(/<t\b[^>]*>([\s\S]*?)<\/t>/g)].map((t) => t[1]).join("")));
}

function cellText(attrs, inner, strings) {
  if (!inner) return "";
  if (/\bt="s"/.test(attrs)) return strings[+(inner.match(/<v>(\d+)<\/v>/)?.[1] ?? -1)] ?? "";
  if (/\bt="inlineStr"/.test(attrs)) return unescapeXml([...inner.matchAll(/<t\b[^>]*>([\s\S]*?)<\/t>/g)].map((t) => t[1]).join(""));
  return unescapeXml(inner.match(/<v>([\s\S]*?)<\/v>/)?.[1] ?? "");
}

const CELL = /<c r="([A-Z]+)(\d+)"([^>]*?)(?:\/>|>([\s\S]*?)<\/c>)/g;
const ROW = /<row r="(\d+)"([^>]*?)(?:\/>|>([\s\S]*?)<\/row>)/g;

// readTemplate checks a template and says what it holds: {account, headers,
// room}. It throws, in Portuguese, when the file is not a Realize template.
export async function readTemplate(bytes) {
  const t = await open(bytes);
  return { account: t.account, headers: [...t.columns.keys()], room: t.room };
}

async function open(bytes) {
  const files = unzip(bytes);
  const byName = new Map(files.map((f) => [f.name, f]));
  const text = async (name) => (byName.has(name) ? decoder.decode(await inflate(byName.get(name))) : "");
  const workbook = await text("xl/workbook.xml");
  const rels = await text("xl/_rels/workbook.xml.rels");
  if (!workbook) throw new Error("Este arquivo não é uma planilha do Excel.");
  const adsPath = sheetPath(workbook, rels, "Ads");
  if (!adsPath || !byName.has(adsPath)) throw new Error("A planilha não tem a aba Ads: baixe o modelo em Create › Bulk Upload › Download Template.");
  const strings = sharedStrings(await text("xl/sharedStrings.xml"));
  const sheet = await text(adsPath);

  // The header row is the first one naming both Title and Image File Name.
  let headerRow = 0;
  const columns = new Map();
  let lastRow = 0;
  for (const row of sheet.matchAll(ROW)) {
    lastRow = Math.max(lastRow, +row[1]);
    if (headerRow) continue;
    const found = new Map();
    for (const c of (row[3] || "").matchAll(CELL)) {
      const v = cellText(c[3], c[4], strings).trim();
      if (v) found.set(v, c[1]);
    }
    if (found.has("Title") && found.has("Image File Name")) {
      headerRow = +row[1];
      for (const [k, v] of found) columns.set(k, v);
    }
  }
  if (!headerRow) throw new Error("Não achei o cabeçalho da aba Ads (Title, Image File Name): use o modelo do Realize sem mexer nele.");

  let account = "";
  const metaPath = sheetPath(workbook, rels, "METADATA");
  if (metaPath) {
    const meta = await text(metaPath);
    for (const row of meta.matchAll(ROW)) {
      const cells = [...(row[3] || "").matchAll(CELL)].map((c) => cellText(c[3], c[4], strings));
      if (cells[0] === "accountName") account = cells[1] || "";
    }
  }
  return { files, text, workbook, rels, strings, metaPath, adsPath, sheet, headerRow, columns, account, room: Math.max(lastRow, 500) - headerRow };
}

const inline = (ref, value) => `<c r="${ref}" t="inlineStr"><is><t xml:space="preserve">${escapeXml(value)}</t></is></c>`;
const letter = (i) => String.fromCharCode(65 + i);

// listRows puts rows (arrays of text) under a list tab's header row, in
// place of whatever was there.
function listRows(xml, rows) {
  const body = rows.map((r, i) => `<row r="${i + 2}">${r.map((v, j) => inline(`${letter(j)}${i + 2}`, v)).join("")}</row>`).join("");
  let out = xml.replace(ROW, (whole, n) => (+n > 1 ? "" : whole));
  out = out.includes("</sheetData>") ? out.replace("</sheetData>", `${body}</sheetData>`) : out.replace("<sheetData/>", `<sheetData>${body}</sheetData>`);
  const width = Math.max(1, ...rows.map((r) => r.length));
  return out.replace(/<dimension ref="[^"]*"\/>/, `<dimension ref="A1:${letter(width - 1)}${rows.length + 1}"/>`);
}

// accountFiles writes the account's part of a template, as Realize fills it
// when it hands one out: METADATA's accountName and utcDate, the Accounts
// tab and the Campaign Groups list. account: {network, accounts: [{id,
// name}], groups: [name]}. Tabs a template lacks are skipped.
async function accountFiles(t, account, when) {
  const out = new Map();
  if (t.metaPath) {
    const meta = (await t.text(t.metaPath)).replace(ROW, (whole, n, attrs, inner) => {
      const cells = [...(inner || "").matchAll(CELL)];
      const key = cells[0] ? cellText(cells[0][3], cells[0][4], t.strings) : "";
      const value = key === "accountName" ? account.network || "" : key === "utcDate" ? when.toISOString() : null;
      if (value === null) return whole;
      return `<row r="${n}"${attrs.replace(/\s*\/$/, "")}>${cells[0][0]}${inline(`B${n}`, value)}</row>`;
    });
    out.set(t.metaPath, meta);
  }
  const lists = [
    ["Accounts", (account.accounts || []).map((a) => [a.id, a.name || a.id])],
    ["Campaign Groups", (account.groups || []).map((g) => [g])],
  ];
  for (const [name, rows] of lists) {
    const path = sheetPath(t.workbook, t.rels, name, true);
    if (path) out.set(path, listRows(await t.text(path), rows));
  }
  return out;
}

// fillTemplate writes rows (arrays in the order of headers) into the Ads tab
// of the template, starting under its header row, and returns the new
// workbook's bytes. Cells keep the template's styles; values go in as text.
// With account, the account's part is written too (accountFiles): that is
// how the page's built-in base template becomes the account's own.
export async function fillTemplate(bytes, headers, rows, account = null, when = new Date()) {
  const t = await open(bytes);
  const letters = headers.map((h) => t.columns.get(h) || null);
  const missing = headers.filter((h, i) => !letters[i] && rows.some((r) => r[i] !== "" && r[i] != null));
  if (missing.length) throw new Error(`O modelo não tem a(s) coluna(s): ${missing.join(", ")}.`);

  const wanted = new Map();
  rows.forEach((row, i) => wanted.set(t.headerRow + 1 + i, row));
  const cellXml = (col, n, attrs, value) => {
    const style = attrs.match(/\bs="(\d+)"/);
    return `<c r="${col}${n}"${style ? ` s="${style[1]}"` : ""} t="inlineStr"><is><t xml:space="preserve">${escapeXml(value)}</t></is></c>`;
  };
  const buildRow = (n, attrs, inner) => {
    const values = wanted.get(n);
    const cells = new Map();
    for (const c of (inner || "").matchAll(CELL)) cells.set(c[1], { attrs: c[3], xml: c[0] });
    values.forEach((v, i) => {
      if (v === "" || v == null || !letters[i]) return;
      cells.set(letters[i], { xml: cellXml(letters[i], n, cells.get(letters[i])?.attrs || "", v) });
    });
    const ordered = [...cells.entries()].sort((a, b) => columnNumber(a[0]) - columnNumber(b[0])).map(([, c]) => c.xml).join("");
    const cleanAttrs = attrs.replace(/\s*\/$/, "");
    return `<row r="${n}"${cleanAttrs}>${ordered}</row>`;
  };

  const done = new Set();
  let sheet = t.sheet.replace(ROW, (whole, n, attrs, inner) => {
    if (!wanted.has(+n)) return whole;
    done.add(+n);
    return buildRow(+n, attrs, inner);
  });
  const extra = [...wanted.keys()].filter((n) => !done.has(n)).map((n) => buildRow(n, "", "")).join("");
  if (extra) sheet = sheet.replace("</sheetData>", `${extra}</sheetData>`).replace("<sheetData/>", `<sheetData>${extra}</sheetData>`);

  const changed = account ? await accountFiles(t, account, when) : new Map();
  changed.set(t.adsPath, sheet);
  const out = t.files
    .map((f) => (changed.has(f.name) ? { name: f.name, data: encoder.encode(changed.get(f.name)) } : f));
  return concat(zip(out));
}
