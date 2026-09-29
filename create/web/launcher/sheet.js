// The bundle: Taboola's bulk sheet (an .xlsx with a Campaigns tab and an Ads
// tab) and a ZIP of the creatives it names. Column names and order follow
// Realize's "Bulk Operations: Creative & Campaigns" article (help center
// 10466563, read 2026-09-29): ads go in the second tab, and an ad's "Image
// File Name" must match a file in the ZIP exactly.

import { zip, concat } from "./zip.js";
import { clean } from "./checks.js";

export const CAMPAIGN_COLUMNS = [
  "Campaign ID", "Campaign Name", "Brand Name", "Marketing Objective", "Conversion Goal",
  "Start Date", "End Date", "DayParting Timezone", "Day Parting",
  "Country Targeting Type", "Country Targeting", "Region/State Type", "Region/State Targeting",
  "DMA Type", "DMA Targeting", "City Type", "City Targeting", "Postal Code Type", "Postal Code Targeting",
  "Platform Targeting", "OS Targeting Type", "OS Targeting", "Browser Targeting Type", "Browser Targeting",
  "Contextual Segments Targeting - Include", "Contextual Segments Targeting - Exclude",
  "Marketplace Audience Targeting - Include 1", "Marketplace Audience Targeting - Include 2",
  "Marketplace Audience Targeting - Include 3", "Marketplace Audience Targeting - Include 4",
  "Marketplace Audience Targeting - Exclude", "My Audiences Targeting - Include", "My Audiences Targeting - Exclude",
  "Block Sites - Include", "Block Sites - Exclude", "Brand Safety", "Bidding Strategy", "CPA Goal",
  "Pricing Model", "Bid", "Budget Type", "Budget Amount", "Spending Limit Type", "Spending Limit",
  "Ad Optimization", "AB Test End Date", "Tracking Code",
];

export const AD_COLUMNS = [
  "Ad ID", "Campaign ID", "Landing Page URL", "Ad Name", "Custom ID", "Title", "Description", "CTA",
  "Image File Name", "Image URL", "Motion Ad Video/Gif File Name", "Motion Ad Video URL", "Motion Ad Gif URL",
  "Motion Ads Default Image File Name", "Motion Ads Default Image URL", "Status",
];
for (let i = 1; i <= 5; i++) {
  AD_COLUMNS.push(`3rd Party Tracking ${i} - Pixel URL`, `3rd Party Tracking ${i} - Tag Type`, `3rd Party Tracking Pixel ${i} - Event Type`);
}

// CTA captions as Realize shows them. Empty means no button.
export const CTAS = ["", "Learn More", "Read More", "Shop Now", "Buy Now", "Order Now", "Get Offer", "Get Now", "Try Now", "Sign Up", "Watch Now", "Apply Now", "Click Here"];

// campaignIds reads the ids typed in one box: digits, split by anything else.
export function campaignIds(text) {
  return [...new Set((text.match(/\d+/g) || []))];
}

// adRows makes one Ads row per ad per campaign. ads: [{creativeFile,
// adName, customId, title}]; settings: {campaigns, url, description, cta,
// status}. With no campaign id, rows go out with the column empty.
export function adRows(ads, settings) {
  const ids = settings.campaigns.length ? settings.campaigns : [""];
  const rows = [];
  for (const campaign of ids) {
    for (const ad of ads) {
      const row = new Array(AD_COLUMNS.length).fill("");
      row[1] = campaign === "" ? "" : Number(campaign) <= Number.MAX_SAFE_INTEGER ? Number(campaign) : campaign;
      row[2] = settings.url;
      row[3] = ad.adName;
      row[4] = ad.customId;
      row[5] = ad.title;
      row[6] = settings.description || "";
      row[7] = settings.cta || "";
      row[8] = ad.creativeFile;
      row[15] = settings.status || "Active";
      rows.push(row);
    }
  }
  return rows;
}

// tsv turns rows into tab-separated text to paste under the header of the
// Ads tab in Taboola's own template.
export function tsv(rows) {
  return rows.map((r) => r.map((v) => String(v).replace(/[\t\r\n]+/g, " ")).join("\t")).join("\n");
}

// safeName turns a file name into one that survives every tool on the way:
// ASCII letters, digits, dot, dash and underscore, lower-case extension.
export function safeName(name) {
  const dot = name.lastIndexOf(".");
  let base = dot > 0 ? name.slice(0, dot) : name;
  let ext = dot > 0 ? name.slice(dot + 1).toLowerCase() : "jpg";
  if (ext === "jpeg") ext = "jpg";
  base = base.normalize("NFKD").replace(/[̀-ͯ]/g, "").replace(/[^A-Za-z0-9._-]+/g, "-").replace(/-+/g, "-").replace(/^[-.]+|[-.]+$/g, "");
  ext = ext.replace(/[^a-z0-9]/g, "") || "jpg";
  return `${(base || "imagem").slice(0, 80)}.${ext}`;
}

// uniqueNames makes safe names unique, ignoring case: a.jpg, a-2.jpg, ...
export function uniqueNames(names) {
  const seen = new Set();
  return names.map((n) => {
    const safe = safeName(n);
    const dot = safe.lastIndexOf(".");
    let candidate = safe;
    for (let i = 2; seen.has(candidate.toLowerCase()); i++) candidate = `${safe.slice(0, dot)}-${i}${safe.slice(dot)}`;
    seen.add(candidate.toLowerCase());
    return candidate;
  });
}

async function sha256hex(bytes) {
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// fingerprint is the first 10 hex characters of a SHA-256.
export async function fingerprint(bytes) {
  return (await sha256hex(bytes)).slice(0, 10);
}

// adId is our id for one ad, sent as the item's Custom ID (30 characters at
// most): "ah-", the creative's fingerprint, "-", the cleaned headline's. The
// same picture with the same headline gets the same id in any bundle and
// campaign, so Taboola's reports can name the ad later.
export async function adId(creativeFingerprint, headline) {
  const h = await fingerprint(new TextEncoder().encode(clean(headline)));
  return `ah-${creativeFingerprint}-${h}`;
}

// --- xlsx ------------------------------------------------------------------

const MAIN = "http://schemas.openxmlformats.org/spreadsheetml/2006/main";
const REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
const PKG = "http://schemas.openxmlformats.org/package/2006/relationships";

function esc(s) {
  return String(s)
    .replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F￾￿]/g, "")
    .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function column(i) {
  let s = "";
  for (i++; i > 0; i = Math.floor((i - 1) / 26)) s = String.fromCharCode(65 + ((i - 1) % 26)) + s;
  return s;
}

// xlsx writes a workbook: sheets is [{name, rows}], the first row of each is
// its header (bold). Numbers become number cells, everything else text
// through the shared strings table, the way Excel writes them itself.
export function xlsx(sheets) {
  const strings = [];
  const index = new Map();
  const str = (s) => {
    if (!index.has(s)) {
      index.set(s, strings.length);
      strings.push(s);
    }
    return index.get(s);
  };
  const files = [];
  const sheetXml = sheets.map(({ rows }) => {
    const body = rows.map((row, r) => {
      const cells = row.map((v, c) => {
        if (v === "" || v === null || v === undefined) return "";
        const ref = `${column(c)}${r + 1}`;
        const style = r === 0 ? ' s="1"' : "";
        if (typeof v === "number" && Number.isFinite(v)) return `<c r="${ref}"${style}><v>${v}</v></c>`;
        return `<c r="${ref}"${style} t="s"><v>${str(String(v))}</v></c>`;
      }).join("");
      return `<row r="${r + 1}">${cells}</row>`;
    }).join("");
    return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<worksheet xmlns="${MAIN}"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><sheetData>${body}</sheetData></worksheet>`;
  });

  const n = sheets.length;
  files.push({
    name: "[Content_Types].xml",
    text: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>${sheets.map((_, i) => `<Override PartName="/xl/worksheets/sheet${i + 1}.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`).join("")}<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/><Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/></Types>`,
  });
  files.push({
    name: "_rels/.rels",
    text: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<Relationships xmlns="${PKG}"><Relationship Id="rId1" Type="${REL}/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
  });
  files.push({
    name: "xl/workbook.xml",
    text: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<workbook xmlns="${MAIN}" xmlns:r="${REL}"><sheets>${sheets.map((s, i) => `<sheet name="${esc(s.name)}" sheetId="${i + 1}" r:id="rId${i + 1}"/>`).join("")}</sheets></workbook>`,
  });
  files.push({
    name: "xl/_rels/workbook.xml.rels",
    text: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<Relationships xmlns="${PKG}">${sheets.map((_, i) => `<Relationship Id="rId${i + 1}" Type="${REL}/worksheet" Target="worksheets/sheet${i + 1}.xml"/>`).join("")}<Relationship Id="rId${n + 1}" Type="${REL}/styles" Target="styles.xml"/><Relationship Id="rId${n + 2}" Type="${REL}/sharedStrings" Target="sharedStrings.xml"/></Relationships>`,
  });
  files.push({
    name: "xl/styles.xml",
    text: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<styleSheet xmlns="${MAIN}"><fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`,
  });
  sheetXml.forEach((text, i) => files.push({ name: `xl/worksheets/sheet${i + 1}.xml`, text }));
  // Built after the sheets, which fill the table.
  files.push({
    name: "xl/sharedStrings.xml",
    text: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<sst xmlns="${MAIN}" count="${strings.length}" uniqueCount="${strings.length}">${strings.map((s) => `<si><t xml:space="preserve">${esc(s)}</t></si>`).join("")}</sst>`,
  });
  const enc = new TextEncoder();
  return concat(zip(files.map((f) => ({ name: f.name, data: enc.encode(f.text) }))));
}

// bulkSheet is the workbook for Taboola: an empty Campaigns tab (header
// only) and the Ads tab.
export function bulkSheet(rows) {
  return xlsx([
    { name: "Campaigns", rows: [CAMPAIGN_COLUMNS] },
    { name: "Ads", rows: [AD_COLUMNS, ...rows] },
  ]);
}
