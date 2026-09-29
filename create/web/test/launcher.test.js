// Tests for the launcher page's logic: node --test create/web/test/
import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { mixed, every, uses, seeded } from "../launcher/pairing.js";
import { clean, hasHidden, headlineWarnings, imageWarnings, urlWarnings, looksAIMade } from "../launcher/checks.js";
import { zip, concat, crc32 } from "../launcher/zip.js";
import { AD_COLUMNS, adRows, bulkSheet, campaignIds, safeName, uniqueNames, adId, tsv } from "../launcher/sheet.js";

test("mixed: 10 creatives and 5 headlines make 10 ads, each headline twice", () => {
  const pairs = mixed(10, 5);
  assert.equal(pairs.length, 10);
  const u = uses(pairs, 10, 5);
  assert.deepEqual(u.creatives, new Array(10).fill(1));
  assert.deepEqual(u.headlines, new Array(5).fill(2));
});

test("mixed: 10 and 8 give 10 ads, two headlines used twice", () => {
  const u = uses(mixed(10, 8), 10, 8);
  assert.deepEqual(u.creatives, new Array(10).fill(1));
  assert.equal(u.headlines.filter((n) => n === 2).length, 2);
  assert.equal(u.headlines.filter((n) => n === 1).length, 6);
});

test("mixed: more headlines than creatives repeats the creatives", () => {
  const u = uses(mixed(3, 7), 3, 7);
  assert.deepEqual(u.headlines, new Array(7).fill(1));
  assert.equal(Math.min(...u.creatives), 2);
  assert.equal(Math.max(...u.creatives), 3);
});

test("mixed: shuffled rounds keep every item used and no pair twice", () => {
  for (let seed = 1; seed < 50; seed++) {
    const pairs = mixed(11, 4, seeded(seed));
    const u = uses(pairs, 11, 4);
    assert.ok(u.headlines.every((n) => n >= 2 && n <= 3), `seed ${seed}: ${u.headlines}`);
    assert.equal(new Set(pairs.map((p) => p.join(":"))).size, pairs.length);
  }
});

test("mixed and every with an empty list make nothing", () => {
  assert.deepEqual(mixed(0, 5), []);
  assert.deepEqual(mixed(3, 0), []);
  assert.equal(every(4, 3).length, 12);
});

test("hidden characters are found and cleaned", () => {
  const t = "Ne​uropathy  relief­ tips ";
  assert.ok(hasHidden(t));
  assert.equal(clean(t), "Neuropathy relief tips");
  assert.match(headlineWarnings(t).join(" "), /invisíveis/);
});

test("headline warnings follow Taboola's title rules", () => {
  const w = (t) => headlineWarnings(t).join(" | ");
  assert.equal(w("Five simple ways to tidy your home office"), "");
  assert.match(w("This SHOCKING trick cures joint pain!!"), /maiúsculas/);
  assert.match(w("This SHOCKING trick cures joint pain!!"), /exclamação/);
  assert.match(w("This trick cures joint pain"), /absoluto/);
  assert.match(w("A diabetic's morning routine"), /diabetes/);
  assert.match(w("Doctors surprised by neuropathy tip"), /doença/);
  assert.match(w("Lose 20 lbs before summer"), /valor/);
  assert.equal(w("New ED option men are talking about"), "");
  assert.match(w("x".repeat(61)), /61 caracteres/);
});

test("image and link warnings", () => {
  assert.deepEqual(imageWarnings({ width: 1600, height: 896, size: 300000, type: "image/jpeg" }), []);
  assert.match(imageWarnings({ width: 500, height: 300, size: 1, type: "image/jpeg" }).join(), /600×400/);
  assert.match(imageWarnings({ width: 800, height: 1200, size: 1, type: "image/png" }).join(), /em pé/);
  assert.match(imageWarnings({ width: 1600, height: 900, size: 3 * 1048576, type: "image/jpeg" }).join(), /2,5 MB/);
  assert.deepEqual(urlWarnings("https://j4j2s.rttrk.com/abc?x=1"), []);
  assert.match(urlWarnings("https://x.com/?sub1={campaign_id}").join(), /Tracking Code/);
  assert.match(urlWarnings("").join(), /Falta/);
});

test("AI markers are spotted in file bytes", () => {
  const enc = new TextEncoder();
  assert.ok(looksAIMade(enc.encode("\x89PNG....c2pa.....OpenAI")));
  assert.ok(!looksAIMade(enc.encode("\xff\xd8 plain camera jpeg")));
});

test("crc32 matches the standard check value", () => {
  assert.equal(crc32(new TextEncoder().encode("123456789")), 0xcbf43926);
});

function python(script, file) {
  return execFileSync("python3", ["-c", script, file], { encoding: "utf8" });
}

test("zip is readable by Python's zipfile, with UTF-8 names", () => {
  const dir = mkdtempSync(join(tmpdir(), "zip-"));
  const file = join(dir, "a.zip");
  const data = concat(zip([
    { name: "one.jpg", data: new Uint8Array([1, 2, 3]) },
    { name: "ação.png", data: new TextEncoder().encode("hello") },
  ]));
  writeFileSync(file, data);
  const out = python("import sys,zipfile;z=zipfile.ZipFile(sys.argv[1]);assert z.testzip() is None;print(z.namelist(),z.read('ação.png'))", file);
  assert.match(out, /\['one.jpg', 'ação.png'\] b'hello'/);
});

test("rows go one per ad per campaign, in Taboola's column order", () => {
  const ads = [
    { creativeFile: "a.jpg", adName: "a - H1", customId: "ah-1", title: "T1" },
    { creativeFile: "b.jpg", adName: "b - H2", customId: "ah-2", title: "T2" },
  ];
  const rows = adRows(ads, { campaigns: campaignIds("123, 456\n"), url: "https://x.io/p", cta: "Learn More", status: "Paused" });
  assert.equal(rows.length, 4);
  const col = (name) => AD_COLUMNS.indexOf(name);
  assert.equal(rows[0][col("Campaign ID")], 123);
  assert.equal(rows[3][col("Campaign ID")], 456);
  assert.equal(rows[1][col("Image File Name")], "b.jpg");
  assert.equal(rows[1][col("Title")], "T2");
  assert.equal(rows[1][col("Custom ID")], "ah-2");
  assert.equal(rows[1][col("Status")], "Paused");
  assert.equal(rows[1][col("CTA")], "Learn More");
  assert.equal(AD_COLUMNS.length, 31);
  assert.equal(tsv([["a\tb", 1]]), "a b\t1");
});

test("the bulk sheet opens as a workbook with Campaigns then Ads", () => {
  const rows = adRows([{ creativeFile: "a.jpg", adName: "n", customId: "ah-x", title: "Tips & <tricks> for “you”" }], { campaigns: ["987654"], url: "https://x.io" });
  const dir = mkdtempSync(join(tmpdir(), "xlsx-"));
  const file = join(dir, "b.xlsx");
  writeFileSync(file, bulkSheet(rows));
  const script = `
import sys, zipfile, xml.etree.ElementTree as ET
z = zipfile.ZipFile(sys.argv[1])
ns = {"m": "http://schemas.openxmlformats.org/spreadsheetml/2006/main"}
wb = ET.fromstring(z.read("xl/workbook.xml"))
print([s.get("name") for s in wb.find("m:sheets", ns)])
sst = [si.find("m:t", ns).text for si in ET.fromstring(z.read("xl/sharedStrings.xml"))]
sheet = ET.fromstring(z.read("xl/worksheets/sheet2.xml"))
rows = sheet.find("m:sheetData", ns)
for row in rows:
    out = []
    for c in row:
        v = c.find("m:v", ns).text
        out.append(sst[int(v)] if c.get("t") == "s" else "#" + v)
    print(out[:6])
`;
  const out = python(script, file);
  assert.match(out, /\['Campaigns', 'Ads'\]/);
  assert.match(out, /\['Ad ID', 'Campaign ID', 'Landing Page URL', 'Ad Name', 'Custom ID', 'Title'\]/);
  assert.match(out, /\['#987654', 'https:\/\/x.io', 'n', 'ah-x', 'Tips & <tricks> for “you”'/);
});

test("file names are made safe and unique", () => {
  assert.equal(safeName("Foto Ação (1).JPEG"), "Foto-Acao-1.jpg");
  assert.deepEqual(uniqueNames(["a.jpg", "A.jpg", "a.jpg", "b.png"]), ["a.jpg", "A-2.jpg", "a-3.jpg", "b.png"]);
});

test("ad ids are stable, short and ignore invisible characters", async () => {
  const a = await adId("0123456789", "Calmer desk tips");
  const b = await adId("0123456789", "Calmer​ desk  tips");
  assert.equal(a, b);
  assert.match(a, /^ah-0123456789-[0-9a-f]{10}$/);
  assert.ok(a.length <= 30);
});
