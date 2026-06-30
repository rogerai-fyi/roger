// Unit lock for the dashboard frontier-savings TOGGLE math (web/src/js/dashboard.js). The file
// is a browser IIFE, but with `document` undefined (here) it exports its pure bit (frontierAt)
// and skips all DOM/fetch. Run: node --test test/dashboard.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const src = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/dashboard.js"), "utf8");
const mod = { exports: {} };
new Function("module", "window", src)(mod, undefined); // document is undefined -> node export path
const R = mod.exports;

// Each reference entry carries the server-computed frontier_est = what THIS account's tokens
// would have cost at that model's list price. The toggle cycles them and recomputes savings.
const ref = [
  { model: "gpt-4o", frontier_est: 10 },
  { model: "claude-opus-4-8", frontier_est: 30 },
  { model: "gemini-2.5-pro", frontier_est: 6 },
];

test("frontierAt: cycles the reference list and wraps both directions", () => {
  assert.equal(R.frontierAt(ref, 4, 0).model, "gpt-4o");
  assert.equal(R.frontierAt(ref, 4, 1).model, "claude-opus-4-8");
  assert.equal(R.frontierAt(ref, 4, 2).model, "gemini-2.5-pro");
  assert.equal(R.frontierAt(ref, 4, 3).model, "gpt-4o");          // wraps forward
  assert.equal(R.frontierAt(ref, 4, -1).model, "gemini-2.5-pro"); // wraps backward
  assert.equal(R.frontierAt(ref, 4, 7).model, "claude-opus-4-8"); // large index, still modulo
});

test("frontierAt: frontier = the selected model's estimate; savings = frontier - spend", () => {
  const v = R.frontierAt(ref, 4, 1); // claude-opus-4-8 est 30, you spent 4
  assert.equal(v.frontier, 30);
  assert.equal(v.savings, 26);
});

test("frontierAt: savings floors at 0 when the frontier model is cheaper than you paid", () => {
  const v = R.frontierAt(ref, 9, 2); // gemini est 6, you spent 9 -> -3, floored to 0 (never negative)
  assert.equal(v.frontier, 6);
  assert.equal(v.savings, 0);
});

test("frontierAt: a missing / non-numeric estimate reads as 0 (the n0 guard)", () => {
  const v = R.frontierAt([{ model: "x" }], 5, 0); // no frontier_est on the entry
  assert.equal(v.frontier, 0);
  assert.equal(v.savings, 0);
});
