// Executing locks for the Playbox routing drawer's pure functions (web/src/js/playbox-route.js):
// the real request body a turn sends and the stored-state cleaner, asserted with deepEqual
// (playbox-routing.test.mjs checks the page's markup and wiring). Run: node --test web/test/
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const src = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/playbox-route.js"), "utf8");
const mod = { exports: {} };
new Function("module", "window", src)(mod, undefined);
const R = mod.exports;
const turn = (model) => ({ model, stream: true, max_tokens: 1024, messages: [] });

test("signed out: every models[] fallback is asked for free, not only the tape's model", () => {
  const b = R.body({ models: ["b", "c:free"] }, "a", turn("a"), { loggedIn: false, routed: true });
  assert.deepEqual(b.model, "a:free");
  assert.deepEqual(b.models, ["b:free", "c:free"]);
});

test("signed in: the models list goes as typed", () => {
  const b = R.body({ models: ["b"] }, "a", turn("a"), { loggedIn: true, routed: true });
  assert.deepEqual(b, { ...turn("a"), models: ["b"] });
});

test("a price cap of 0 is no cap: never sent and never summarised", () => {
  const r = R.clean({ out: 0, in: 0, turn: 0 });
  assert.equal(r.out, null);
  assert.equal(r.in, null);
  assert.equal(r.turn, null);
  const b = R.body({ out: 0, in: 0, turn: 0 }, "a", turn("a"), { loggedIn: true, routed: true });
  assert.equal(b.provider, undefined);
  assert.equal(R.summary({ out: 0, in: 0, turn: 0 }, true), "");
  const b2 = R.body({ out: 2 }, "a", turn("a"), { loggedIn: true, routed: true });
  assert.deepEqual(b2.provider, { max_price: { completion: 2 } });
});

test("stored sort, pref and trust are kept only when they are values the drawer offers", () => {
  const r = R.clean({ sort: "fastest", pref: "Cheap", trust: "trusted" });
  assert.equal(r.sort, "");
  assert.equal(r.pref, "");
  assert.equal(r.trust, "");
  const ok = R.clean({ sort: "latency", trust: "verified" });
  assert.equal(ok.sort, "latency");
  assert.equal(ok.trust, "verified");
  const both = R.clean({ sort: "price", pref: "cheap" });
  assert.equal(both.sort, "price");
  assert.equal(both.pref, "", "sort by replaces prefer");
});

test("a turn on another model (the own-image vision turn) carries none of the tape's routing", () => {
  const route = { quant: "Q8_0", models: ["b"], region: "eu", pref: "fast" };
  const signedIn = R.body(route, "vision-m", turn("vision-m"), { loggedIn: true, routed: false });
  assert.deepEqual(signedIn, turn("vision-m"));
  const signedOut = R.body(route, "vision-m", turn("vision-m"), { loggedIn: false, routed: false });
  assert.deepEqual(signedOut, { ...turn("vision-m"), model: "vision-m:free" }, "signed out still asks only for free");
});

test("the full body for a routed turn maps each field to its contract key", () => {
  const route = R.clean({ quant: "Q8_0", sort: "throughput", tps: 20, selfHosted: true, confidential: true,
    tools: true, vision: true, size: [7, 70], region: "eu", trust: "verified", ctx: 32768, ttft: 1500, turn: 0.02 });
  const b = R.body(route, "a", turn("a"), { loggedIn: true, routed: true });
  assert.deepEqual(b.provider, { max_price: { request: 0.02 }, quantizations: ["Q8_0"], sort: "throughput" });
  assert.deepEqual(b.roger, { min_tps: 20, self_hosted_only: true, confidential: true, require: ["tools", "vision"],
    params_b: [7, 70], region: ["eu"], trust_min: "verified", min_ctx: 32768, max_ttft_ms: 1500 });
});
