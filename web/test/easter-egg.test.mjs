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

test("easeOutCubic / easeInCubic: pinned at 0 and 1, eased between", () => {
  assert.equal(R.easeOutCubic(0), 0);
  assert.equal(R.easeOutCubic(1), 1);
  assert.equal(R.easeInCubic(0), 0);
  assert.equal(R.easeInCubic(1), 1);
  // out-cubic is fast-then-slow (ahead of linear at the midpoint); in-cubic is the mirror.
  assert.ok(R.easeOutCubic(0.5) > 0.5);
  assert.ok(R.easeInCubic(0.5) < 0.5);
  assert.ok(Math.abs(R.easeOutCubic(0.5) + R.easeInCubic(0.5) - 1) < 1e-9);
});
