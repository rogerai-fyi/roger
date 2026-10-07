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

// a stand-in DOM node: its tag, contenteditable, and the ancestors closest() can find
const node = (tag, opts = {}) => ({
  tagName: tag.toUpperCase(), isContentEditable: !!opts.editable,
  closest: (sel) => (opts.inside || []).includes(sel) ? {} : null,
});

test("the cassette bay leaves arrow keys and drags to a field or the routing drawer", () => {
  for (const t of ["input", "select", "textarea"]) {
    assert.equal(R.ownsInput(node(t)), true, `${t} keeps its keys`);
  }
  assert.equal(R.ownsInput(node("div", { editable: true })), true, "contenteditable keeps its keys");
  assert.equal(R.ownsInput(node("button", { inside: ["#dkRoute"] })), true, "anything inside the drawer keeps its keys");
  assert.equal(R.ownsInput(node("button", { inside: ["#dkRouteBtn"] })), true, "the drawer toggle keeps its keys");
  assert.equal(R.ownsInput(node("div")), false, "the bay itself swaps tapes");
  assert.equal(R.ownsInput(null), false);
});

test("the bay's keydown and drag consult the guard before acting", () => {
  const js = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/playbox.js"), "utf8");
  const bay = js.slice(js.indexOf("(function dragBay() {"), js.indexOf("})();", js.indexOf("(function dragBay() {")));
  assert.match(bay, /function down\(e\) \{\s*if \(STATE\.playing \|\| window\.PlayboxRoute\.ownsInput\(e\.target\)\) return;/,
    "a drag starting on a field or the drawer must not throw the tape");
  assert.match(bay, /addEventListener\("keydown", function \(e\) \{\s*if \(window\.PlayboxRoute\.ownsInput\(e\.target\)\) return;/,
    "arrow keys in a field or the drawer must not change tape");
});

test("region choices and stored regions keep only tokens the broker accepts (^[a-z]{2,8}$)", () => {
  assert.deepEqual(R.regionChoices(["eu", "us-west", "US", "apac", "x", "toolongtoken"]), ["eu", "apac"]);
  assert.equal(R.clean({ region: "us-west" }).region, "", "a stored region the broker would 400 is dropped");
  assert.equal(R.clean({ region: "eu" }).region, "eu");
});

test("the drawer fills its region select through the filter", () => {
  const js = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/playbox.js"), "utf8");
  assert.match(js, /fill\("dkRtRegion", b \? window\.PlayboxRoute\.regionChoices\(Object\.keys\(b\.regions \|\| \{\}\)\) : \[\]/);
});

test("the page's shortcut keys leave a focused button or control in the deck alone (Escape still stops)", () => {
  for (const t of ["button", "a", "summary"]) assert.equal(R.isControl(node(t)), true, t);
  assert.equal(R.isControl({ ...node("div"), getAttribute: (n) => (n === "role" ? "button" : null) }), true, "role=button");
  assert.equal(R.isControl(node("div")), false);
  assert.equal(R.isControl(null), false);
});

test("the document keydown consults the drawer guard and the control guard", () => {
  const js = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/playbox.js"), "utf8");
  const i = js.indexOf('document.addEventListener("keydown", function (e) {');
  const head = js.slice(i, i + 700);
  assert.match(head, /if \(isTyping\(e\.target\)\) return;[^\n]*\n\s*if \(window\.PlayboxRoute\.ownsInput\(e\.target\)\) return;/);
  assert.match(head, /if \(window\.PlayboxRoute\.pressesControl\(e\)\) return;/);
});

test("a refused routing value names the drawer field, the models list included", () => {
  assert.equal(R.nameFields("invalid routing value for models: want a list of model ids"),
    "invalid routing value for also try: want a list of model ids");
  assert.equal(R.nameFields("invalid routing value for provider.max_price.completion: want a non-negative number"),
    "invalid routing value for max $/1M out: want a non-negative number");
  assert.equal(R.nameFields("invalid routing value for roger.min_tps: want a non-negative number"),
    "invalid routing value for min t/s: want a non-negative number");
});

test("only the keys a focused control takes (Space, Enter) are left to it; arrows, digits and E still drive the deck", () => {
  const btn = node("button");
  for (const key of [" ", "Spacebar", "Enter"]) assert.equal(R.pressesControl({ key, target: btn }), true, key);
  for (const key of ["ArrowLeft", "ArrowRight", "1", "e", "Escape"]) assert.equal(R.pressesControl({ key, target: btn }), false, key);
  assert.equal(R.pressesControl({ key: " ", target: node("div") }), false);
});

test("a size range parses to [min, max] or refuses, never NaN", () => {
  assert.deepEqual(R.parseSize("7-70B"), [7, 70]);
  assert.deepEqual(R.parseSize("-70"), [0, 70]);
  assert.deepEqual(R.parseSize("13"), [13, 13]);
  assert.equal(R.parseSize(""), null);
  assert.equal(R.parseSize("any"), null);
  for (const bad of [".", "7-.", "-", ". - .", "70-7", "0", "x"]) assert.throws(() => R.parseSize(bad), bad);
});

test("the drawer reads its size through the module's parser", () => {
  const js = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/playbox.js"), "utf8");
  assert.match(js, /function routeSize\(v\) \{ return window\.PlayboxRoute\.parseSize\(v\); \}/);
});

test("a voided reply is not shown as served (settle-failed still is: the reply finished)", () => {
  assert.deepEqual(R.servedOf({ cost: 0.01, rogerai: { model: "m", node: "n" } }), { model: "m", node: "n", cost: 0.01 });
  assert.deepEqual(R.servedOf({ cost: 0, rogerai: { model: "m", node: "n", void_reason: "upstream-5xx" } }), { void: "upstream-5xx" });
  assert.deepEqual(R.servedOf({ cost: 0, rogerai: { model: "m", node: "n", void_reason: "settle-failed" } }), { model: "m", node: "n", cost: 0 });
  assert.equal(R.servedOf({ prompt_tokens: 1 }), null, "a station's own usage object is not the broker's");
});

test("a size with a spaced unit parses", () => {
  assert.deepEqual(R.parseSize("7 B"), [7, 7]);
  assert.deepEqual(R.parseSize("7 - 70 B"), [7, 70]);
});

test("the stream reads served through the module, and clearing stale choices is saved", () => {
  const js = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/playbox.js"), "utf8");
  assert.match(js, /served = window\.PlayboxRoute\.servedOf\(d\.usage\)/);
  assert.match(js, /if \(qn\.indexOf\("is not on"\) !== -1 \|\| rn\.indexOf\("is not on"\) !== -1\) saveRoute\(\);/);
});
