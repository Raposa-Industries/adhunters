// Tests for the page script's pure helpers: node --test funnels/web/test/
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const code = readFileSync(new URL("../ah.js", import.meta.url), "utf8");
const box = { URLSearchParams };
runInNewContext(code, box);
const ah = box.AdHuntersFunnels;

test("loads without a page and starts nothing", () => {
  assert.equal(ah.SCHEMA, 1);
  assert.equal(box.__ahStarted, undefined);
});

test("parseTime reads m:ss, h:mm:ss and plain seconds", () => {
  assert.equal(ah.parseTime("14:00"), 840);
  assert.equal(ah.parseTime("1:02:03"), 3723);
  assert.equal(ah.parseTime("90"), 90);
  assert.equal(ah.parseTime(" 0:05 "), 5);
  assert.ok(Number.isNaN(ah.parseTime("")));
  assert.ok(Number.isNaN(ah.parseTime("ten")));
  assert.ok(Number.isNaN(ah.parseTime(undefined)));
  assert.ok(Number.isNaN(ah.parseTime("1:2:3:4")));
});

test("pickArm gives one journey the same arm every time and spreads journeys", () => {
  const arms = [{ id: "a" }, { id: "b" }];
  assert.equal(ah.pickArm("j1:bp", arms), ah.pickArm("j1:bp", arms));
  const seen = { a: 0, b: 0 };
  for (let i = 0; i < 1000; i++) seen[ah.pickArm("journey-" + i + ":bp", arms).id]++;
  assert.ok(seen.a > 400 && seen.b > 400, JSON.stringify(seen));
  assert.equal(ah.pickArm("x", null), null);
  assert.equal(ah.pickArm("x", []), null);
});

test("watched seconds merge into ranges; seeking ahead leaves a gap", () => {
  let r = [];
  for (const s of [0, 1, 2, 3]) r = ah.addSecond(r, s);
  assert.deepEqual(JSON.parse(JSON.stringify(r)), [[0, 4]]);
  r = ah.addSecond(r, 10);
  r = ah.addSecond(r, 2); // seeking back counts once
  assert.deepEqual(JSON.parse(JSON.stringify(r)), [[0, 4], [10, 11]]);
  assert.equal(ah.rangeSeconds(r), 5);
  assert.deepEqual(JSON.parse(JSON.stringify(ah.mergeRanges([[5, 7], [0, 2], [2, 3], [6, 9], [4, 4]]))), [[0, 3], [5, 9]]);
});

test("smart progress runs ahead early and meets the end", () => {
  assert.equal(ah.smartProgress(0, 100, 2), 0);
  assert.equal(ah.smartProgress(100, 100, 2), 1);
  assert.ok(Math.abs(ah.smartProgress(25, 100, 2) - 0.4375) < 1e-9);
  assert.equal(ah.smartProgress(50, 100, 1), 0.5);
  assert.equal(ah.smartProgress(10, 0, 2), 0);
  assert.equal(ah.smartProgress(200, 100, 2), 1);
});

test("readParams takes RedTrack's clickid and the sub values", () => {
  const p = ah.readParams("?clickid=abc123&sub1=50549004&sub4=777&sub8=1322&other=x&utm_source=Taboola");
  assert.equal(p.clickid, "abc123");
  assert.deepEqual({ ...p.subs }, { sub1: "50549004", sub4: "777", sub8: "1322", utm_source: "Taboola" });
  assert.equal(ah.readParams("").clickid, "");
});

test("links get the click id and journey filled in", () => {
  assert.equal(ah.fillLink("https://j4j2s.rttrk.com/click?clickid={clickid}", "a b", "j"), "https://j4j2s.rttrk.com/click?clickid=a%20b");
  assert.equal(ah.fillLink("/next?j={journey}", "", "j1"), "/next?j=j1");
});

test("a journey continues across pages and restarts on a new click id", () => {
  let n = 0;
  const newId = () => "new" + ++n;
  const first = ah.resolveJourney(null, ah.readParams("?clickid=c1&sub4=9"), newId);
  assert.equal(first.j, "new1");
  assert.ok(first.fresh);
  const cookie = "x=1; ah_j=" + ah.encodeCookie(first);
  const saved = ah.decodeCookie(cookie);
  assert.equal(saved.c, "c1");
  const next = ah.resolveJourney(saved, ah.readParams("?page=2"), newId);
  assert.equal(next.j, "new1");
  assert.equal(next.fresh, false);
  assert.equal(next.s.sub4, "9");
  const same = ah.resolveJourney(saved, ah.readParams("?clickid=c1"), newId);
  assert.equal(same.j, "new1");
  const other = ah.resolveJourney(saved, ah.readParams("?clickid=c2"), newId);
  assert.equal(other.j, "new2");
  assert.equal(ah.decodeCookie("ah_j=%7Bbroken"), null);
  assert.equal(ah.decodeCookie(""), null);
});
