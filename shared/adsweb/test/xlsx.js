// A minimal .xlsx writer, for tests: it builds small workbooks shaped like
// Realize's template for fillTemplate to fill.
import { zip, concat } from "../assets/zip.js";

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
  // The workbook opens on its last tab: Ads, where the rows are.
  const last = sheets.length - 1;
  const sheetXml = sheets.map(({ rows }, i) => {
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
    return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<worksheet xmlns="${MAIN}"><sheetViews><sheetView${i === last ? ' tabSelected="1"' : ""} workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><sheetData>${body}</sheetData></worksheet>`;
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
    text: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<workbook xmlns="${MAIN}" xmlns:r="${REL}"><bookViews><workbookView activeTab="${sheets.length - 1}"/></bookViews><sheets>${sheets.map((s, i) => `<sheet name="${esc(s.name)}" sheetId="${i + 1}" r:id="rId${i + 1}"/>`).join("")}</sheets></workbook>`,
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
