// Executable lock for the @web scenarios of features/details: the node console's BROWSE about
// row and category modal (internal/webui/assets, driven in jsdom with a stubbed /api), the
// website's QSL ABOUT block and band row (web/src/js/details.js + bands.js wiring), and the
// manual. details.js is ONE script shipped twice; the first test holds the copies identical.
// Run: node --test web/test/details.test.mjs
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { JSDOM } from "jsdom";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const ASSETS = path.join(WEB, "..", "internal", "webui", "assets");
const src = (p) => readFileSync(path.join(WEB, p), "utf8");
const asset = (p) => readFileSync(path.join(ASSETS, p), "utf8");

// web_about.feature Background, as the broker publishes it (curated notes stamped).
const curated = (model, summary, best, notFor, page) => ({
  summary, best_at: best, not_for: notFor, details_source: "rogerai", page, base_model: "demo-1b", license: "Apache-2.0",
});
const OFFERS = () => [
  { node_id: "n1", model: "demo-1b-fire-decision", online: true, modality: "chat", tuning: "adapter",
    ...curated("demo-1b-fire-decision", "Picks one fire-defense action from site state.", "Choosing among a fixed list of actions.",
      "Chat. It answers with one action only.", "/research-demo-1b.html#fire") },
  { node_id: "n2", model: "demo-1b-fire-vl", online: true, modality: "chat", tuning: "adapter", capabilities: ["vision"],
    ...curated("demo-1b-fire-vl", "Confirms or rejects smoke or flame in a frame.", "Single camera frames, yes or no.",
      "Video, or deciding what to do about it.", "/research-demo-1b.html#vl") },
  { node_id: "n3", model: "someone-7b", online: true, modality: "chat", tuning: "instruct", base_model: "base-7b", license: "MIT",
    summary: "A 7B chat model.", best_at: "Long answers.", not_for: "Short replies.", details_source: "declared" },
  { node_id: "n4", model: "mystery-m", online: true, modality: "chat" },
];

function details() {
  const dom = new JSDOM("<!doctype html><body></body>", { runScripts: "outside-only" });
  dom.window.eval(src("src/js/details.js"));
  return { RD: dom.window.RogerDetails, doc: dom.window.document, win: dom.window };
}

// ---- the node console in jsdom -------------------------------------------------------------
async function consolePage(state) {
  const html = asset("console.html").replace(/<script[^>]*src=[^>]*><\/script>/g, "");
  const dom = new JSDOM(html, { url: "http://127.0.0.1:8000/?t=tok#browse", runScripts: "outside-only", pretendToBeVisual: true });
  const w = dom.window;
  state.posts = [];
  w.fetch = (url, opts = {}) => {
    const u = new URL(url, "http://127.0.0.1:8000");
    let body = {};
    if (u.pathname === "/api/browse" && state.down) {
      return Promise.resolve({ ok: false, status: 502, statusText: "Bad Gateway", headers: { get: () => "text/plain" },
        json: () => Promise.reject(new Error("not json")), text: () => Promise.resolve("broker down") });
    }
    if (u.pathname === "/api/browse") body = state.offers;
    else if (u.pathname === "/api/categories") body = { version: 1, categories: [{ name: "language", subcategories: ["chat"] }] };
    else if (u.pathname === "/api/share/meta" && (opts.method || "GET") === "GET") body = state.meta || { declared: {}, inferred: {} };
    else if (u.pathname === "/api/share/meta") { state.posts.push(JSON.parse(opts.body)); body = { declared: {}, inferred: {} }; }
    return Promise.resolve({ ok: true, status: 200, statusText: "OK", headers: { get: () => "application/json" },
      json: () => Promise.resolve(body), text: () => Promise.resolve(JSON.stringify(body)) });
  };
  w.EventSource = class { constructor() {} close() {} };
  w.eval(asset("console-details.js"));
  w.eval(asset("console.js"));
  await settle(w);
  return { w, doc: w.document };
}
const settle = (w) => new Promise((r) => w.setTimeout(r, 20));
const rowOf = (doc, model) => [...doc.querySelectorAll("#browse-rows tr")].find((tr) => tr.querySelector(".model-name")?.textContent === model);
const aboutBtn = (doc, model) => rowOf(doc, model).querySelector(".about-toggle");
const aboutRows = (doc) => [...doc.querySelectorAll("#browse-rows .about-row")];
const aboutText = (doc) => aboutRows(doc).map((r) => r.textContent).join("\n");

test("details.js is one script: the website and console copies are identical", () => {
  assert.equal(asset("console-details.js"), src("src/js/details.js"));
});

test("Each BROWSE row has an about toggle", async () => {
  const st = { offers: OFFERS() };
  const { doc } = await consolePage(st);
  for (const o of st.offers) {
    const b = aboutBtn(doc, o.model);
    assert.ok(b, "no about button on " + o.model);
    assert.equal(b.textContent, "about");
    assert.equal(b.getAttribute("aria-expanded"), "false");
  }
});

test("The about toggle opens a details row under the offer", async () => {
  const st = { offers: OFFERS() };
  const { doc } = await consolePage(st);
  aboutBtn(doc, "demo-1b-fire-decision").click();
  const rows = aboutRows(doc);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].previousElementSibling, rowOf(doc, "demo-1b-fire-decision"));
  assert.equal(rows[0].querySelector("td").colSpan, 8);
  assert.equal(aboutBtn(doc, "demo-1b-fire-decision").getAttribute("aria-expanded"), "true");
  const t = aboutText(doc);
  for (const want of ["RogerAI's description of this model", "Picks one fire-defense action from site state.",
    "Best at: Choosing among a fixed list of actions.", "Not for: Chat. It answers with one action only.",
    "Kind: text in, text out · adapter on demo-1b", "License: Apache-2.0"]) assert.ok(t.includes(want), "missing " + want + " in " + t);
  const a = rows[0].querySelector("a");
  assert.equal(a.textContent, "Model page");
  assert.equal(a.getAttribute("href"), "https://rogerai.fm/research-demo-1b.html#fire");
});

test("The vision adapter reads as image in", async () => {
  const { doc } = await consolePage({ offers: OFFERS() });
  aboutBtn(doc, "demo-1b-fire-vl").click();
  assert.ok(aboutText(doc).includes("Kind: text and image in, text out · adapter on demo-1b"));
});

test("Sibling models on the same base are named", async () => {
  const { doc } = await consolePage({ offers: OFFERS() });
  aboutBtn(doc, "demo-1b-fire-decision").click();
  assert.ok(aboutText(doc).includes("Same base: demo-1b-fire-vl"));
});

test("Declared text is labelled as the station's own words, with no link", async () => {
  const { doc } = await consolePage({ offers: OFFERS() });
  aboutBtn(doc, "someone-7b").click();
  const t = aboutText(doc);
  assert.ok(t.includes("The station's own words") && t.includes("A 7B chat model."), t);
  assert.equal(aboutRows(doc)[0].querySelector("a"), null);
});

test("An offer with nothing to say says so", async () => {
  const { doc } = await consolePage({ offers: OFFERS() });
  aboutBtn(doc, "mystery-m").click();
  assert.ok(aboutText(doc).includes("No description yet. The station has not said what this model is for."));
});

test("Only one details row is open at a time", async () => {
  const { doc } = await consolePage({ offers: OFFERS() });
  aboutBtn(doc, "someone-7b").click();
  aboutBtn(doc, "demo-1b-fire-vl").click();
  assert.equal(aboutRows(doc).length, 1);
  assert.equal(aboutRows(doc)[0].previousElementSibling, rowOf(doc, "demo-1b-fire-vl"));
  assert.equal(aboutBtn(doc, "someone-7b").getAttribute("aria-expanded"), "false");
});

test("Clicking about again closes the row", async () => {
  const { doc } = await consolePage({ offers: OFFERS() });
  aboutBtn(doc, "someone-7b").click();
  aboutBtn(doc, "someone-7b").click();
  assert.equal(aboutRows(doc).length, 0);
  assert.equal(aboutBtn(doc, "someone-7b").getAttribute("aria-expanded"), "false");
});

test("The open row survives a refresh when the offer is still listed", async () => {
  const st = { offers: OFFERS() };
  const { w, doc } = await consolePage(st);
  aboutBtn(doc, "someone-7b").click();
  doc.getElementById("btn-browse-refresh").click();
  await settle(w);
  assert.equal(aboutRows(doc).length, 1);
  assert.equal(aboutRows(doc)[0].previousElementSibling, rowOf(doc, "someone-7b"));
  assert.equal(aboutBtn(doc, "someone-7b").getAttribute("aria-expanded"), "true");
});

test("The open row closes when the offer leaves the market", async () => {
  const st = { offers: OFFERS() };
  const { w, doc } = await consolePage(st);
  aboutBtn(doc, "someone-7b").click();
  st.offers = st.offers.filter((o) => o.node_id !== "n3");
  doc.getElementById("btn-browse-refresh").click();
  await settle(w);
  assert.equal(aboutRows(doc).length, 0);
});

test("The toggle is keyboard reachable", async () => {
  // A native <button type=button> is in the tab order and Enter activates it (the browser's own
  // behavior); jsdom does not synthesize that click, so the activation is the click itself.
  const { doc } = await consolePage({ offers: OFFERS() });
  const b = aboutBtn(doc, "someone-7b");
  assert.equal(b.tagName, "BUTTON");
  assert.equal(b.type, "button");
  assert.notEqual(b.tabIndex, -1);
  b.focus();
  assert.equal(doc.activeElement, b);
  b.click();
  assert.equal(aboutRows(doc).length, 1);
});

test("The empty-market and broker-down rows have no toggle", async () => {
  const empty = await consolePage({ offers: [] });
  assert.equal(empty.doc.querySelectorAll(".about-toggle").length, 0);
  assert.ok(empty.doc.querySelector("#browse-rows").textContent.includes("No models on the market right now."));
  const down = await consolePage({ offers: [], down: true });
  assert.equal(down.doc.querySelectorAll(".about-toggle").length, 0);
  assert.ok(down.doc.querySelector("#browse-rows").textContent.includes("Could not load the market."));
});

test("The details row wraps long text and never scrolls the page sideways", () => {
  // jsdom has no layout: the mechanism is the about cell's own wrapping rule.
  const css = asset("console.css").replace(/\s+/g, " ");
  const rule = css.match(/\.about-row td\.about \{([^}]*)\}/);
  assert.ok(rule, "no .about-row td.about rule");
  assert.match(rule[1], /overflow-wrap: anywhere/);
  assert.match(rule[1], /white-space: normal/);
});

const MARKUP = ["<script>alert(1)</script>", "<img src=x onerror=alert(1)>", '<a href="javascript:alert(1)">docs</a>',
  '"><svg onload=alert(1)>', "{@html '<b>x</b>'}", "{{card:nano.size}}", "&lt;b&gt;already escaped&lt;/b&gt;", "${alert(1)}"];

test("Markup in details is shown as literal text in every web surface (console)", async () => {
  for (const raw of MARKUP) {
    const offers = [{ node_id: "x", model: "demo-1b-chat", online: true, summary: raw, details_source: "declared" }];
    const { doc } = await consolePage({ offers });
    aboutBtn(doc, "demo-1b-chat").click();
    const row = aboutRows(doc)[0];
    assert.ok(row.textContent.includes(raw), raw);
    assert.equal(row.querySelectorAll("script,img,svg,a,b").length, 0, raw);
  }
});

test("A URL in operator text is never a link (console)", async () => {
  const offers = [{ node_id: "x", model: "m", online: true, best_at: "see https://example.invalid/free-credits", details_source: "declared" }];
  const { doc } = await consolePage({ offers });
  aboutBtn(doc, "m").click();
  assert.ok(aboutText(doc).includes("https://example.invalid/free-credits"));
  assert.equal([...doc.querySelectorAll("a")].filter((a) => (a.href || "").includes("example.invalid")).length, 0);
});

// ---- the console category modal (declaration.feature) --------------------------------------

test("The console category modal has the three fields with maxlength", async () => {
  const { doc } = await consolePage({ offers: [] });
  assert.equal(doc.getElementById("cat-summary").getAttribute("maxlength"), "140");
  assert.equal(doc.getElementById("cat-best").tagName, "TEXTAREA");
  assert.equal(doc.getElementById("cat-best").getAttribute("maxlength"), "200");
  assert.equal(doc.getElementById("cat-notfor").tagName, "TEXTAREA");
  assert.equal(doc.getElementById("cat-notfor").getAttribute("maxlength"), "200");
});

test("The console modal prefills the saved details; The console modal's guess detection counts details as a declaration", () => {
  // openCatModal reads /api/share/meta: the details prefill from the DECLARATION (never the
  // guess), and a details-only declaration is not "a guess".
  const js = asset("console.js").replace(/\s+/g, " ");
  assert.match(js, /\$\("cat-summary"\)\.value = \(d && d\.summary\) \|\| ""/);
  assert.match(js, /\$\("cat-best"\)\.value = \(d && d\.best_at\) \|\| ""/);
  assert.match(js, /\$\("cat-notfor"\)\.value = \(d && d\.not_for\) \|\| ""/);
  assert.match(js, /var guess = !\(d && \([^;]*d\.summary \|\| d\.best_at \|\| d\.not_for\)\)/);
  assert.match(js, /summary: \$\("cat-summary"\)\.value\.trim\(\), best_at: \$\("cat-best"\)\.value\.trim\(\), not_for: \$\("cat-notfor"\)\.value\.trim\(\)/);
});

// ---- the website ---------------------------------------------------------------------------
test("The QSL card's CATEGORY block becomes ABOUT", () => {
  const page = src("app/routes/models/+page.svelte");
  assert.match(page, /<span class="qsl__bk mono">ABOUT<\/span>\s*<div class="qsl__about" id="qslAbout"><\/div>\s*<p class="mono qsl__line" id="qslTax">/);
  assert.doesNotMatch(page, /qsl__bk mono">CATEGORY</);
  const { RD, doc } = details();
  const node = doc.createElement("div");
  RD.render(doc, node, OFFERS(), "demo-1b-fire-decision", { caps: false, origin: "" });
  const t = node.textContent;
  for (const want of ["RogerAI's description of this model", "Picks one fire-defense action from site state.",
    "best at: Choosing among a fixed list of actions.", "not for: Chat. It answers with one action only.",
    "kind: text in, text out · adapter on demo-1b", "license: Apache-2.0"]) assert.ok(t.includes(want), want);
  const a = node.querySelector("a");
  assert.equal(a.textContent, "model page");
  assert.equal(a.getAttribute("href"), "/research-demo-1b.html#fire");
  const bands = src("src/js/bands.js");
  assert.match(bands, /RD\.render\(document, aboutEl, all, b\.model, \{ caps: false, origin: "" \}\)/);
});

test("The QSL card uses the band vote", () => {
  const { RD, doc } = details();
  const st = (n, s) => ({ node_id: n, model: "someone-7b", online: true, summary: s });
  const node = doc.createElement("div");
  RD.render(doc, node, [st("a", "A 7B chat model."), st("b", "A 7B chat model."), st("c", "Other words.")], "someone-7b", {});
  assert.ok(node.textContent.includes("A 7B chat model."));
  assert.ok(node.textContent.includes("the station's own words · 2 of 3 stations"));
});

test("The QSL card for a band with nothing to say keeps a plain line", () => {
  const { RD, doc } = details();
  const node = doc.createElement("div");
  RD.render(doc, node, OFFERS(), "mystery-m", {});
  assert.ok(node.textContent.includes("no description yet"));
});

test("Switching QSL cards never leaves the previous model's text", () => {
  const { RD, doc } = details();
  const node = doc.createElement("div");
  RD.render(doc, node, OFFERS(), "someone-7b", {});
  RD.render(doc, node, OFFERS(), "mystery-m", {});
  assert.ok(!node.textContent.includes("A 7B chat model."));
  assert.equal(node.querySelector("a"), null);
});

test("The band list row carries a one-line summary under the name", () => {
  const bands = src("src/js/bands.js");
  assert.match(bands, /var d = RD && RD\.fold\(b\.stations \|\| \[\]\);\s*if \(d && d\.summary\) li\.appendChild\(el\("span", "band-row__about", null\)\)\.textContent = d\.summary;/);
  const css = src("src/styles/models.css").replace(/\s+/g, " ");
  assert.match(css, /\.band-row__about \{[^}]*text-overflow: ellipsis;[^}]*white-space: nowrap;/);
  const { RD } = details();
  assert.equal(RD.fold(OFFERS().filter((o) => o.model === "mystery-m")), null); // no secondary line
});

test("The /models page keeps working against a broker without details", () => {
  const { RD, doc } = details();
  const bare = [{ node_id: "a", model: "m", online: true }, { model: "m" }, {}];
  const node = doc.createElement("div");
  assert.doesNotThrow(() => RD.render(doc, node, bare, "m", {}));
  assert.ok(node.textContent.includes("no description yet"));
  assert.doesNotThrow(() => RD.render(doc, node, undefined, "m", {}));
});

test("The website ABOUT block passes the contrast and phone-width checks", () => {
  const css = src("src/styles/models.css").replace(/\s+/g, " ");
  assert.match(css, /\.qsl__about p \{[^}]*overflow-wrap: anywhere;/);
  assert.match(css, /\.qsl__about \.about__head \{[^}]*color: var\(--ink-500\);/); // a token the contrast test already holds
});

test('The QSL "model page" link is only ever a same-origin site path', () => {
  const { RD, doc } = details();
  for (const page of ["https://example.invalid/x", "//example.invalid/x", "javascript:alert(1)", "/a b.html"]) {
    const node = doc.createElement("div");
    RD.render(doc, node, [{ model: "m", online: true, summary: "s", details_source: "rogerai", page }], "m", {});
    assert.equal(node.querySelector("a"), null, page);
  }
});

test("Markup in details is shown as literal text in every web surface (website)", () => {
  const { RD, doc } = details();
  for (const raw of MARKUP) {
    const node = doc.createElement("div");
    RD.render(doc, node, [{ model: "m", online: true, summary: raw }], "m", {});
    assert.ok(node.textContent.includes(raw), raw);
    assert.equal(node.querySelectorAll("script,img,svg,a,b").length, 0, raw);
  }
});

test("The website details code never renders raw HTML", () => {
  const d = src("src/js/details.js");
  for (const banned of ["innerHTML", "insertAdjacentHTML", "outerHTML", "document.write"]) assert.ok(!d.includes(banned), banned);
  assert.ok(d.includes("textContent"));
  const page = src("app/routes/models/+page.svelte");
  const block = page.slice(page.indexOf('qsl__bk mono">ABOUT'), page.indexOf('id="qslTax"'));
  assert.ok(block.length > 0 && !block.includes("{@html"), "the ABOUT block renders raw HTML");
});

test("A URL in operator text is never a link (website)", () => {
  const { RD, doc } = details();
  const node = doc.createElement("div");
  RD.render(doc, node, [{ model: "m", online: true, best_at: "see https://example.invalid/free-credits" }], "m", {});
  assert.ok(node.textContent.includes("https://example.invalid/free-credits"));
  assert.equal(node.querySelector("a"), null);
});

test("The details view says whose description it is", () => {
  const { RD, doc } = details();
  for (const caps of [true, false]) {
    const node = doc.createElement("div");
    RD.render(doc, node, OFFERS(), "demo-1b-fire-decision", { caps });
    assert.ok(node.textContent.includes("RogerAI's description of this model"));
    assert.ok(node.textContent.toLowerCase().includes("weights not verified"));
  }
});

test("clean mirrors protocol.DetailsText", () => {
  const { RD } = details();
  assert.equal(RD.clean("fire\u001b[31m RED\u001b[0m model", 140), "fire[31m RED[0m model");
  assert.equal(RD.clean("line one\nline two", 140), "line one line two");
  assert.equal(RD.clean("bidi \u202egnp.exe", 140), "bidi gnp.exe");
  assert.equal(RD.clean("zero\u200bwidth\u200dhere\ufeff", 140), "zerowidthhere");
  assert.equal(RD.clean("many     spaces   inside", 140), "many spaces inside");
  assert.equal(RD.clean("a".repeat(150), 140).length, 140);
  assert.equal([...RD.clean("🔥".repeat(150), 140)].length, 140);
});

// ---- the manual (api_cli.feature) -----------------------------------------------------------
let manual = "";
before(() => {
  execFileSync("node", ["build.mjs"], { cwd: WEB });
  manual = readFileSync(path.join(WEB, "dist", "manual.html"), "utf8").replace(/\s+/g, " ");
});

test("The manual documents the details", () => {
  for (const f of ["--summary", "--best-at", "--not-for"]) assert.ok(manual.includes(f), f);
  assert.ok(manual.includes("at most 140 characters") && manual.includes("at most 200 characters"));
  assert.match(manual, /<kbd>i<\/kbd><\/td><td><b>about \+ station log<\/b>/);
  assert.ok(manual.includes("<code>description</code>") && manual.includes("<code>rogerai.details</code>"));
  assert.ok(manual.includes("the station's own words and is not verified") || manual.includes("station's own words and are not verified"));
});

// Every @web scenario is bound to a test here by its title, so a new or renamed scenario fails.
test("every @web scenario of features/details has a test named after it", () => {
  const feats = ["text_sanitization", "web_about", "declaration", "api_cli", "curated_catalog"];
  const names = [];
  let bound = 0;
  const self = readFileSync(fileURLToPath(import.meta.url), "utf8");
  for (const m of self.matchAll(/^test\("([^"]+)"/gm)) names.push(m[1]);
  for (const m of self.matchAll(/^test\('([^']+)'/gm)) names.push(m[1]);
  for (const f of feats) {
    const lines = readFileSync(path.join(WEB, "..", "features", "details", f + ".feature"), "utf8").split("\n");
    lines.forEach((l, i) => {
      if (!/^\s*@.*\bweb\b/.test(l) || /@webui/.test(l)) return;
      const title = lines[i + 1].replace(/^\s*Scenario( Outline)?:\s*/, "").trim();
      assert.ok(names.some((n) => n.includes(title)), f + ": no test for @web scenario \"" + title + "\"");
      bound++;
    });
  }
  assert.equal(bound, 29, "the @web scenario count moved: bind the new ones here");
});
