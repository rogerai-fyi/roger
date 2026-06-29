// Unit lock for the footer easter-egg trigger logic (web/src/js/easter-egg.js). The file is a
// browser IIFE, but when `document` is undefined (here) it exports its pure bits for testing and
// skips all DOM/animation. Run: node --test test/easter-egg.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const src = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/easter-egg.js"), "utf8");
const mod = { exports: {} };
new Function("module", "window", src)(mod, undefined); // document is undefined -> node export path
const R = mod.exports;

test("makeMultiClick: 5 hits inside the 2.5s window fire exactly once, then reset", () => {
  let fired = 0;
  const hit = R.makeMultiClick(5, 2500, () => fired++);
  [0, 100, 200, 300].forEach((t) => assert.equal(hit(t), false)); // 4 so far - no fire
  assert.equal(hit(400), true);   // 5th within window -> fires
  assert.equal(fired, 1);
  [500, 600, 700, 800].forEach((t) => hit(t)); // counter was reset; 4 more - no fire
  assert.equal(fired, 1);
  assert.equal(hit(900), true);   // 5th of the new batch -> fires again
  assert.equal(fired, 2);
});

test("makeMultiClick: slow clicks (gaps > window) never accumulate", () => {
  let fired = 0;
  const hit = R.makeMultiClick(5, 2500, () => fired++);
  [0, 3000, 6000, 9000, 12000, 15000].forEach((t) => hit(t)); // each gap evicts the prior
  assert.equal(fired, 0);
});

test("easeInOutCubic: pinned at 0, 0.5, 1 (smooth, symmetric)", () => {
  assert.equal(R.easeInOutCubic(0), 0);
  assert.equal(R.easeInOutCubic(1), 1);
  assert.equal(Math.abs(R.easeInOutCubic(0.5) - 0.5) < 1e-9, true);
});

test("spline: passes through its endpoints", () => {
  const pts = [{ x: 0, y: 5 }, { x: 10, y: 0 }, { x: 20, y: 5 }, { x: 30, y: 0 }];
  const a = R.spline(pts, 0), b = R.spline(pts, 1);
  assert.ok(Math.abs(a.x - 0) < 1e-6 && Math.abs(a.y - 5) < 1e-6);
  assert.ok(Math.abs(b.x - 30) < 1e-6 && Math.abs(b.y - 0) < 1e-6);
});
