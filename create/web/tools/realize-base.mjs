// realize-base makes the launcher's built-in Realize template from a real
// one, with every trace of the account it came from taken out:
//
//   node create/web/tools/realize-base.mjs Downloaded-Template.xlsx create/web/launcher/realize-base.xlsx
//
// A Realize template is one fixed base (the tabs, headers, dropdowns and
// Taboola's own code lists) plus the account's data: its name and download
// time in METADATA, and the Accounts, Conversion Events, My Audiences,
// Predictive Audiences and Campaign Groups lists. Those lists are emptied
// here, every string no remaining cell uses is blanked, and the page fills
// the account's part back from the Taboola API when it builds a sheet
// (launcher/template.js). Nothing else in the file is changed.

import { readFileSync, writeFileSync } from "node:fs";
import { deflateRawSync } from "node:zlib";
import { unzip, inflate, zip, concat, crc32 } from "../launcher/zip.js";

const [,, src, dst] = process.argv;
if (!src || !dst) {
  console.error("usage: node realize-base.mjs TEMPLATE.xlsx OUT.xlsx");
  process.exit(2);
}

// ACCOUNT_TABS keep only their header row. Names are exact: "Campaign Groups"
// is the list, "Campaign Groups " (with a space) the bulk tab.
const ACCOUNT_TABS = ["Accounts", "Conversion Events", "My Audiences", "Predictive Audiences", "Campaign Groups"];
// BULK_TABS keep their notes and header rows (1 and 2), and no data.
const BULK_TABS = ["Campaign Groups ", "Campaigns", "Ads", "Library Creatives"];
const META_BLANK = ["accountName", "utcDate", "parentAppName"];

const dec = new TextDecoder();
const enc = new TextEncoder();
const files = unzip(new Uint8Array(readFileSync(src)));
const byName = new Map(files.map((f) => [f.name, f]));
const text = async (name) => dec.decode(await inflate(byName.get(name)));
const out = new Map();

const workbook = await text("xl/workbook.xml");
const rels = await text("xl/_rels/workbook.xml.rels");
const target = (id) => {
  const r = [...rels.matchAll(/<Relationship\b[^>]*>/g)].find((m) => m[0].includes(`Id="${id}"`));
  const t = r[0].match(/Target="([^"]*)"/)[1];
  return t.startsWith("/") ? t.slice(1) : `xl/${t}`;
};
const tabs = [...workbook.matchAll(/<sheet\b[^>]*>/g)].map((m) => ({
  name: m[0].match(/\bname="([^"]*)"/)[1].replace(/&amp;/g, "&"),
  path: target(m[0].match(/\br:id="([^"]*)"/)[1]),
}));

const ROW = /<row r="(\d+)"[^>]*?(?:\/>|>[\s\S]*?<\/row>)/g;
const CELL = /<c r="[A-Z]+\d+"[^>]*?(?:\/>|>[\s\S]*?<\/c>)/g;
const strings = (await text("xl/sharedStrings.xml")).match(/<si>[\s\S]*?<\/si>/g);
const plain = (si) => si.replace(/<[^>]+>/g, "");
const valueOf = (cell) => {
  const v = cell.match(/<v>(\d+)<\/v>/);
  return v && /\bt="s"/.test(cell) ? plain(strings[+v[1]]) : null;
};
// emptyCell keeps a cell's place and style and drops its value.
const emptyCell = (cell) => cell.replace(/\s*t="[^"]*"/, "").replace(/>[\s\S]*<\/c>$/, "/>").replace(/\/?>$/, "/>");

for (const tab of tabs) {
  let xml = await text(tab.path);
  if (ACCOUNT_TABS.includes(tab.name)) {
    xml = xml.replace(ROW, (row, n) => (+n > 1 ? "" : row));
  } else if (BULK_TABS.includes(tab.name)) {
    xml = xml.replace(ROW, (row, n) => (+n > 2 ? row.replace(CELL, emptyCell) : row));
  } else if (tab.name === "METADATA") {
    xml = xml.replace(ROW, (row) => {
      const cells = row.match(CELL) || [];
      if (!META_BLANK.includes(valueOf(cells[0] || ""))) return row;
      return row.replace(CELL, (c, i) => (c === cells[0] ? c : emptyCell(c)));
    });
  } else continue;
  out.set(tab.path, xml);
}

// Blank every shared string no remaining cell points at: whatever the
// account lists held goes with them, and the indices the rest use stay put.
const used = new Set();
for (const tab of tabs) {
  const xml = out.get(tab.path) ?? await text(tab.path);
  for (const c of xml.matchAll(/<c [^>]*\bt="s"[^>]*>\s*<v>(\d+)<\/v>/g)) used.add(+c[1]);
}
let blanked = 0;
const sst = (await text("xl/sharedStrings.xml")).replace(/<si>[\s\S]*?<\/si>/g, (si) => {
  const i = blanked++;
  return used.has(i) ? si : "<si><t/></si>";
});
out.set("xl/sharedStrings.xml", sst);

const core = await text("docProps/core.xml");
out.set("docProps/core.xml", core.replace(/(<dc:creator>|<cp:lastModifiedBy>)[^<]*/g, "$1"));

// Changed files are deflated again, as Excel writes them.
const packed = (name) => {
  const data = enc.encode(out.get(name));
  return { name, raw: deflateRawSync(data, { level: 9 }), method: 8, crc: crc32(data), size: data.length };
};
const result = files.map((f) => (out.has(f.name) ? packed(f.name) : f));
writeFileSync(dst, concat(zip(result)));
console.log(`${dst}: ${strings.length - used.size} of ${strings.length} strings blanked`);
