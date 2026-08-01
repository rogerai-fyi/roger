// Regression locks for the Playground console, pinning the four minor findings the
// 2026-08-01 push audit raised (static-content assertions over web/src, like
// console-tapes.test.mjs):
//   - the dead /market poll is gone (only /discover feeds the directory)
//   - broker-unreachable is a REACHABLE state: the /discover fetch is not
//     null-swallowed before .then, so the "couldn't reach the broker" branch fires
//   - resize/init go through startDraw() so rAF loops never stack
//   - the unreferenced playground-nano-samples.jsonl no longer ships
// Run: node --test test/playground.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "../src");
const read = (p) => readFileSync(path.join(SRC, p), "utf8");

const js = read("js/playground.js");
const html = read("playground.html");

test("playground: no /market fetch - /discover is the one directory source", () => {
  assert.ok(!js.includes('"/market"'), "playground.js still fetches /market");
  assert.ok(js.includes('fetchJSON("/discover"'), "the /discover fetch is gone");
});

test("playground: broker-unreachable branch is reachable (no null-swallow on /discover)", () => {
  assert.ok(
    !/\/discover"[^\n]*catch\(function \(\) \{ return null; \}\)/.test(js),
    "the /discover fetch swallows errors into null, making the off-state unreachable"
  );
  assert.ok(js.includes("couldn't reach the broker"), "the off-state message is gone");
});

test("playground: the edge canvas never calls draw() outside the rAF loop", () => {
  assert.ok(
    !js.includes("draw(performance.now())"),
    "a direct draw(performance.now()) call stacks extra rAF loops beside the beacon loop"
  );
  assert.ok(js.includes("function startDraw()"), "startDraw() (the cancel-then-schedule entry) is gone");
});

test("playground: the unreferenced nano-samples file does not ship", () => {
  assert.ok(!existsSync(path.join(SRC, "data/playground-nano-samples.jsonl")),
    "playground-nano-samples.jsonl is back but nothing loads it (the dataset is inline in playground.html)");
  assert.ok(!html.includes("playground-nano-samples"), "playground.html still names the removed file");
  assert.ok(html.includes('id="pgEdgeData"'), "the inline Edge dataset is gone");
});
