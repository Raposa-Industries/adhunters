// Tests for the ad pages' shared logic: node --test shared/adsweb/test/
import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { mixed, every, uses, seeded, mixedN, everyN, usesN, pairwiseN } from "../assets/pairing.js";
import { clean, hasHidden, headlineWarnings, blockedWords, blockedWarnings, blockedHits, swapBlocked, imageWarnings, urlWarnings, looksAIMade } from "../assets/checks.js";
import { zip, concat, crc32, unzip, inflate } from "../assets/zip.js";
import { AD_COLUMNS, CTAS, adRows, campaignIds, ctaType, safeName, uniqueNames, adId, tsv } from "../assets/sheet.js";
import { readTemplate, fillTemplate } from "../assets/template.js";
import { splitLink, TRACKERS } from "../assets/tracking.js";
import { xlsx } from "./xlsx.js";

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

test("pairwise: image k with headline k, the shorter list starting over in order", () => {
  assert.deepEqual(pairwiseN([3, 3, 1]), [[0, 0, 0], [1, 1, 0], [2, 2, 0]]);
  assert.deepEqual(pairwiseN([4, 2, 1]), [[0, 0, 0], [1, 1, 0], [2, 0, 0], [3, 1, 0]]);
  assert.deepEqual(pairwiseN([2, 3, 2]), [[0, 0, 0], [1, 1, 1], [0, 2, 0]]);
  assert.deepEqual(pairwiseN([2, 0, 1]), []);
  assert.deepEqual(pairwiseN([]), []);
});

test("mixed and every with an empty list make nothing", () => {
  assert.deepEqual(mixed(0, 5), []);
  assert.deepEqual(mixed(3, 0), []);
  assert.equal(every(4, 3).length, 12);
});

test("mixed over three lists: the longest once each, the others in even rounds", () => {
  const combos = mixedN([6, 4, 2], seeded(7));
  assert.equal(combos.length, 6);
  assert.deepEqual(combos.map((c) => c[0]), [0, 1, 2, 3, 4, 5]);
  const [a, b, c] = usesN(combos, [6, 4, 2]);
  assert.deepEqual(a, [1, 1, 1, 1, 1, 1]);
  assert.ok(Math.max(...b) - Math.min(...b) <= 1 && Math.min(...b) >= 1);
  assert.deepEqual(c, [3, 3]);
  // A list that is not the longest can lead too: 2 images, 5 headlines, 3 CTAs.
  const other = mixedN([2, 5, 3]);
  assert.equal(new Set(other.map((x) => x.join())).size, 5);
  assert.deepEqual(mixedN([3, 0, 1]), []);
});

test("every combination over three lists", () => {
  const all = everyN([2, 3, 2]);
  assert.equal(all.length, 12);
  assert.equal(new Set(all.map((x) => x.join())).size, 12);
  assert.deepEqual(all[0], [0, 0, 0]);
  assert.deepEqual(all[1], [0, 0, 1]);
});

test("CTA labels map to the API's names, and a CTA makes its own ad id", async () => {
  assert.equal(ctaType("Learn More"), "LEARN_MORE");
  assert.equal(ctaType(""), "");
  for (const c of CTAS) assert.match(ctaType(c), /^([A-Z]+(_[A-Z0-9]+)*)?$/);
  const plain = await adId("0123456789", "A headline");
  assert.equal(await adId("0123456789", "A headline", ""), plain);
  assert.notEqual(await adId("0123456789", "A headline", "Learn More"), plain);
  assert.ok((await adId("0123456789", "A headline", "Learn More")).length <= 30);
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

// The team's list, read the way the server reads it.
const BLOCKED = readFileSync(new URL("../../../create/internal/openai/rules/blocked.txt", import.meta.url), "utf8")
  .split("\n").filter((l) => l.trim() && !l.startsWith("#"))
  .map((l) => { const [where, text] = l.split("\t"); return { text: text.trim(), description: where === "title+description" }; });

test("the team's blocked words are found, with their endings, and named once", () => {
  assert.deepEqual(blockedWords("Seniors Are Drinking This Before Bed", BLOCKED), ["drink"]);
  assert.deepEqual(blockedWords("What Doctors Say About Foods", BLOCKED), ["doctors", "food"]);
  assert.deepEqual(blockedWords("Memory Loss After 60?", BLOCKED), ["memory loss"]);
  assert.deepEqual(blockedWords("Doctors Warn Seniors About Rapid Memory Decline", BLOCKED), ["Doctors Warn… Rapid Memory Decline"]);
  assert.deepEqual(blockedWords("Ringing in the ears at night?", BLOCKED), ["Ringing In The Ears"]);
  assert.deepEqual(blockedWords("Warum Füße nachts brennen", BLOCKED), ["Füße"]);
  // No word inside another word, and a clean headline is clean.
  assert.deepEqual(blockedWords("A pillow trick seniors love", BLOCKED), []);
  assert.deepEqual(blockedWords("This Morning Habit Surprised Everyone", BLOCKED), []);
  // Descriptions only carry the words blocked there too.
  assert.deepEqual(blockedWords("Neurologists and doctors agree", BLOCKED, "description"), ["neurologists"]);
  assert.equal(blockedWarnings("Tinnitus?", BLOCKED).length, 1);
  assert.deepEqual(blockedWarnings("Tinnitus?", undefined), []);
});

// The replacements offered on the page, read the way the server reads them.
const SYNONYMS = new Map(readFileSync(new URL("../../../create/internal/openai/rules/synonyms.txt", import.meta.url), "utf8")
  .split("\n").filter((l) => l.trim() && !l.startsWith("#"))
  .map((l) => { const [word, opts] = l.split("\t"); return [word.trim().toLowerCase(), opts.split("|").map((o) => o.trim())]; }));
const WITH_SYNONYMS = BLOCKED.map((b) => ({ ...b, alternatives: SYNONYMS.get(b.text.toLowerCase()) || [] }));

test("every suggested replacement is itself clean", () => {
  for (const [word, opts] of SYNONYMS) {
    for (const o of opts) {
      assert.deepEqual(blockedWords(`Seniors Try This ${o} Trick`, BLOCKED), [], `${word} -> ${o}`);
      assert.deepEqual(headlineWarnings(`Seniors Try This ${o} Trick`), [], `${word} -> ${o}`);
    }
  }
});

test("a blocked word is swapped in place, in its own case", () => {
  assert.deepEqual(blockedHits("Memory Loss? Try This", WITH_SYNONYMS), [{ text: "memory loss", alternatives: ["fading focus", "senior moments", "slipping recall"] }]);
  assert.equal(swapBlocked("Memory Loss? Try This Before Bed", "memory loss", "fading focus"), "Fading Focus? Try This Before Bed");
  assert.equal(swapBlocked("what doctors and Doctors say", "doctors", "experts"), "what experts and Experts say");
  assert.equal(swapBlocked("WINE O'CLOCK", "wine", "red grape"), "RED GRAPE O'CLOCK");
  assert.equal(swapBlocked("memorycard stays", "memory", "focus"), "memorycard stays");
  // The person's extra instructions are checked against every word.
  assert.deepEqual(blockedWords("mulheres tomando um drink de manhã", BLOCKED, "prompt"), ["drink"]);
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

test("unzip reads deflated archives and zip copies their entries as they are", async () => {
  const dir = mkdtempSync(join(tmpdir(), "unzip-"));
  const file = join(dir, "d.zip");
  python("import sys,zipfile\nwith zipfile.ZipFile(sys.argv[1],'w',zipfile.ZIP_DEFLATED) as z: z.writestr('x/a.txt','hello '*50); z.writestr('b.txt','hi')", file);
  const files = unzip(new Uint8Array(readFileSync(file)));
  assert.deepEqual(files.map((f) => [f.name, f.method]), [["x/a.txt", 8], ["b.txt", 8]]);
  assert.equal(new TextDecoder().decode(await inflate(files[0])), "hello ".repeat(50));
  const copy = join(dir, "c.zip");
  writeFileSync(copy, concat(zip(files)));
  assert.match(python("import sys,zipfile;z=zipfile.ZipFile(sys.argv[1]);assert z.testzip() is None;print(z.read('b.txt'))", copy), /b'hi'/);
});

test("one row per ad, into every campaign, in the template's column order", () => {
  const ads = [
    { creativeFile: "a.jpg", adName: "a - H1", customId: "ah-1", title: "T1" },
    { creativeFile: "b.jpg", adName: "b - H2", customId: "ah-2", title: "T2" },
  ];
  const rows = adRows(ads, { campaigns: campaignIds("123, 456\n"), url: "https://x.io/p", cta: "Learn More", ai: "Yes" });
  assert.equal(rows.length, 2);
  const col = (name) => AD_COLUMNS.indexOf(name);
  assert.equal(rows[0][col("Campaign ID")], "123; 456");
  assert.equal(rows[1][col("Image File Name")], "b.jpg");
  assert.equal(rows[1][col("Title")], "T2");
  assert.equal(rows[1][col("Custom ID")], "ah-2");
  assert.equal(rows[1][col("Status")], "Paused");
  assert.equal(rows[1][col("AI Content")], "Yes");
  assert.equal(rows[1][col("CTA")], "Learn More");
  assert.equal(col("AI Content"), 16); // column Q
  assert.equal(tsv([["a\tb", 1]]), "a b\t1");
});

// A workbook shaped like Realize's template: a METADATA tab, then Ads with
// notes in row 1, headers in row 2 and empty styled rows under them.
function fakeTemplate(dataRows) {
  const notes = AD_COLUMNS.map(() => "#Optional");
  const empty = Array.from({ length: dataRows }, () => AD_COLUMNS.map(() => ""));
  return xlsx([
    { name: "METADATA", rows: [["utcDate", "2026-09-29"], ["accountName", "acme-sc"]] },
    { name: "Campaigns", rows: [["#Required"], ["Campaign ID", "Campaign Name"]] },
    { name: "Ads", rows: [notes, AD_COLUMNS, ...empty] },
  ]);
}

test("a template is read: its account and where the ads go", async () => {
  const t = await readTemplate(fakeTemplate(3));
  assert.equal(t.account, "acme-sc");
  assert.ok(t.headers.includes("AI Content"));
  await assert.rejects(readTemplate(xlsx([{ name: "Sheet1", rows: [["x"]] }])), /aba Ads/);
});

test("ads are written into the template's Ads tab, the rest left alone", async () => {
  const rows = adRows(
    [{ creativeFile: "a.jpg", adName: "n", customId: "ah-x", title: "Tips & <tricks> for “you”" },
     { creativeFile: "b.jpg", adName: "m", customId: "ah-y", title: "Two" }],
    { campaigns: ["987654"], url: "https://x.io", ai: "No" });
  // Two rows of room: the second ad goes on a new row.
  const out = await fillTemplate(fakeTemplate(1), AD_COLUMNS, rows);
  const dir = mkdtempSync(join(tmpdir(), "xlsx-"));
  const file = join(dir, "b.xlsx");
  writeFileSync(file, out);
  const script = `
import sys, zipfile, xml.etree.ElementTree as ET
z = zipfile.ZipFile(sys.argv[1])
assert z.testzip() is None
ns = {"m": "http://schemas.openxmlformats.org/spreadsheetml/2006/main"}
wb = ET.fromstring(z.read("xl/workbook.xml"))
print([s.get("name") for s in wb.find("m:sheets", ns)])
sheet = ET.fromstring(z.read("xl/worksheets/sheet3.xml"))
for row in sheet.find("m:sheetData", ns):
    if row.get("r") in ("3", "4"):
        print(row.get("r"), {c.get("r"): "".join(t.text or "" for t in c.iter("{%s}t" % ns["m"])) for c in row})
`;
  const text = python(script, file);
  assert.match(text, /\['METADATA', 'Campaigns', 'Ads'\]/);
  assert.match(text, /3 \{'B3': '987654', 'C3': 'https:\/\/x.io', 'D3': 'n', 'E3': 'ah-x', 'F3': 'Tips & <tricks> for “you”', 'I3': 'a.jpg', 'P3': 'Paused', 'Q3': 'No'\}/);
  assert.match(text, /4 \{'B4': '987654'.*'F4': 'Two'/);
});

// The built-in base: Realize's template with no account in it, which the
// page fills with the account read from the API.
const BASE = new URL("../assets/realize-base.xlsx", import.meta.url);

test("the built-in base carries no account, and gets one when filled", async () => {
  const base = new Uint8Array(readFileSync(BASE));
  const t = await readTemplate(base);
  assert.equal(t.account, "");
  assert.ok(t.headers.includes("Title") && t.headers.includes("Status"));
  const rows = adRows([{ creativeFile: "a.jpg", adName: "n", customId: "ah-x", title: "One" }],
    { campaigns: ["987654"], url: "https://x.io", ai: "Yes" });
  const account = { network: "acme-network", accounts: [{ id: "acme-sc", name: "Acme" }], groups: ["Group A"] };
  const out = await fillTemplate(base, AD_COLUMNS, rows, account, new Date("2026-09-29T20:00:00Z"));
  const dir = mkdtempSync(join(tmpdir(), "xlsx-"));
  const file = join(dir, "base.xlsx");
  writeFileSync(file, out);
  const script = `
import sys, zipfile, xml.etree.ElementTree as ET
z = zipfile.ZipFile(sys.argv[1])
assert z.testzip() is None
M = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"
R = "{http://schemas.openxmlformats.org/officeDocument/2006/relationships}"
sst = ["".join(t.text or "" for t in si.iter(M + "t")) for si in ET.fromstring(z.read("xl/sharedStrings.xml"))]
rels = {r.get("Id"): r.get("Target") for r in ET.fromstring(z.read("xl/_rels/workbook.xml.rels"))}
tabs = {s.get("name"): "xl/" + rels[s.get(R + "id")].lstrip("/").replace("xl/", "") for s in ET.fromstring(z.read("xl/workbook.xml")).iter(M + "sheet")}
def rows(name):
    out = []
    for row in ET.fromstring(z.read(tabs[name])).iter(M + "row"):
        vals = []
        for c in row:
            v = c.find(M + "v")
            vals.append(sst[int(v.text)] if c.get("t") == "s" and v is not None else "".join(t.text or "" for t in c.iter(M + "t")) or (v.text if v is not None else ""))
        out.append(vals)
    return out
meta = {r[0]: (r[1] if len(r) > 1 else "") for r in rows("METADATA") if r}
print("meta", meta["accountName"], meta["utcDate"])
print("accounts", rows("Accounts"))
print("groups", rows("Campaign Groups"))
ads = rows("Ads")
print("ad", ads[1][5], ads[2][5], ads[2][15])
print("bulk groups rows with text", sum(1 for r in rows("Campaign Groups ")[2:] if any(r)))
blob = b"".join(z.read(n) for n in z.namelist()).lower()
print("zolta", b"zolta" in blob)
`;
  const text = python(script, file);
  assert.match(text, /meta acme-network 2026-09-29T20:00:00.000Z/);
  assert.match(text, /accounts \[\['Account Name', 'Display Name'\], \['acme-sc', 'Acme'\]\]/);
  assert.match(text, /groups \[\['Name'\], \['Group A'\]\]/);
  assert.match(text, /ad Title One Paused/);
  assert.match(text, /bulk groups rows with text 0/);
  assert.match(text, /zolta False/);
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

test("a tracker link splits into the ad's address and the tracking code", () => {
  const link = "https://j4j2s.rttrk.com/6abbbb874d42468fe3139131?sub1={campaign_id}&utm_source=Taboola&sub4={campaign_item_id}&sub8={site_id}";
  assert.deepEqual(splitLink(link), {
    url: "https://j4j2s.rttrk.com/6abbbb874d42468fe3139131?utm_source=Taboola",
    tracking: "sub1={campaign_id}&sub4={campaign_item_id}&sub8={site_id}",
  });
  assert.deepEqual(splitLink("https://x.io/p"), { url: "https://x.io/p", tracking: "" });
  assert.deepEqual(splitLink(" https://x.io/p?a=1#top "), { url: "https://x.io/p?a=1#top", tracking: "" });
  assert.match(TRACKERS.redtrack.template, /sub1=\{campaign_id\}.*sub4=\{campaign_item_id\}.*sub8=\{site_id\}/);
});
