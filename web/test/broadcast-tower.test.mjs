// Executable lock for features/web/broadcast_tower.feature.
//
// This article is marketing for a money mechanism, which is the combination most likely to
// drift into a promise. Most assertions here are therefore of the form "this number is
// stated with the base it is measured against" or "this thing is never claimed" - in
// particular that the 10% is never dressed up as an expected income while relay traffic is
// still early.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const visible = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<style[\s\S]*?<\/style>/g, " ")
  .replace(/<script[\s\S]*?<\/script>/g, " ").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const PAGE = "broadcasts-run-a-tower.html";
const page = () => read(PAGE);
const copy = () => visible(page().match(/<main[\s\S]*?<\/main>/)[0]);

test("the broadcast exists and is findable as a broadcast", () => {
  assert.ok(existsSync(path.join(DIST, PAGE)), "the page builds");
  assert.match(read("sitemap.xml"), /broadcasts-run-a-tower\.html/, "it is indexed");

  const index = read("broadcasts.html");
  assert.match(index, /href="\/broadcasts-run-a-tower\.html"/, "the log lists it");
  // Newest first: it must appear before the broadcast it follows.
  assert.ok(index.indexOf("broadcasts-run-a-tower.html") < index.indexOf("broadcasts-how-rogerai-routes-models.html"),
    "it sits at the top of the transmission log");
  assert.match(index, /BROADCAST 010/, "the counter advanced");

  const c = copy();
  assert.match(page(), /href="\/tower\.html"/, "it links the concept page");
  assert.match(page(), /href="\/manual\.html#sTower"/, "it links the operator runbook");
  assert.ok(c.length > 3000, "it is an article, not a stub");
});

test("the split is stated with the base it is measured against", () => {
  const c = copy();
  assert.match(c, /70%/, "the node's share");
  assert.match(c, /10%/, "the tower operator's share");
  assert.match(c, /20%/, "the platform's share");
  // The base is the whole point: 10% of the NODE's own price, not of a house rate.
  assert.match(c, /(own listed price|node's own listed price|its own listed price)/i,
    "the article says what the percentage is a percentage OF");
  assert.match(c, /recount/i, "and that Core recounts rather than trusting the claim");
});

test("earning copy stays conditional on demand", () => {
  const c = copy();
  assert.match(c, /traffic through any one Tower is early/i,
    "the article says traffic is early, in those terms");
  assert.match(c, /start at zero/i, "and that a new Tower's figure starts at zero");
  assert.match(c, /follow(s)? demand/i, "and that it tracks demand, not the install date");

  // No projection of relay income, in any of the shapes a projection takes.
  assert.doesNotMatch(c, /\$\s?\d[\d,.]*\s*(\/|per\s*)?(mo|month|day|week|year)/i,
    "no per-period dollar figure");
  assert.doesNotMatch(c, /(earn|make|expect)\s+(up to\s+)?\$\s?\d/i, "no dollar promise");
  assert.doesNotMatch(c, /passive income|guaranteed|risk[- ]free/i, "no income-promise vocabulary");
});

test("the confidentiality claim is scoped to the relay, and admits what leaks", () => {
  const c = copy();
  assert.match(c, /holds no key to the content|no key to the content/i,
    "the Tower holds no content key");
  assert.match(c, /seal(s|ed)? .{0,40}(node's|node) .{0,20}key/i,
    "the work is sealed to the SERVING NODE's key - that is why the relay cannot read it");
  // The honest limit: shape is visible even when content is not.
  assert.match(c, /traffic shape/i, "it names what a Tower does see");
  assert.match(c, /how many bytes/i, "and is concrete about it");
});

test("the install surface matches what the release ships", () => {
  const c = copy();
  assert.match(c, /ROGERAI_COMPONENT=tower/, "the real installer invocation");
  assert.match(c, /amd64/, "amd64");
  assert.match(c, /arm64/, "arm64");
  assert.match(c, /Linux only|Linux on amd64/i, "Linux-only is stated, not implied");
  // A Tower runs no model, so telling anyone to buy a GPU for one would be a lie.
  assert.match(c, /no GPU|runs no model/i, "it says a Tower needs no GPU");
});

test("sign-in is never described as GitHub-only", () => {
  const c = copy();
  const mentionsSignIn = /sign(ing)? in|login|log in/i.test(c);
  assert.ok(mentionsSignIn, "the article does discuss signing in");
  // Whenever it does, the other two providers must be there too - this page shipped after
  // Apple and emailed codes went live, and must not re-introduce the stale GitHub-only line.
  assert.match(c, /GitHub, Apple, or email/i, "all three providers are named together");
});

test("the animated hero degrades to a still, and both are the same frame", () => {
  const html = page();
  // build.mjs cache-busts every asset url with ?v=<hash>, so match the path, not the url.
  assert.match(html, /assets\/broadcasts\/tower-loop\.mp4/, "the loop is referenced");
  assert.ok(existsSync(path.join(DIST, "assets/broadcasts/tower-loop.mp4")), "and shipped");
  assert.ok(existsSync(path.join(DIST, "assets/broadcasts/tower-hero.png")), "the still is shipped");
  // Autoplaying decoration must be muted, must loop, and must not fight the viewer.
  assert.match(html, /<video[^>]*\bmuted\b/, "the loop is muted");
  assert.match(html, /<video[^>]*\bloop\b/, "the loop loops");
  assert.match(html, /<video[^>]*\bplaysinline\b/, "it does not go fullscreen on iOS");
  assert.match(html, /prefers-reduced-motion: reduce/, "reduced motion is honoured");
  // The poster IS the still, so the first paint is the illustration either way.
  assert.match(html, /poster="assets\/broadcasts\/tower-hero\.png(\?v=[0-9a-f]+)?"/,
    "poster matches the still");
});

test("the FAQ answers are also in the structured data, verbatim enough to match", () => {
  const html = page();
  // Every page carries the site-wide Organization graph first, so pick the FAQ block by
  // its type rather than by position.
  const blocks = [...html.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)]
    .map((m) => JSON.parse(m[1]));
  const data = blocks.find((b) => b["@type"] === "FAQPage");
  assert.ok(data, "the page carries FAQ structured data");
  assert.equal(data["@type"], "FAQPage");
  assert.ok(data.mainEntity.length >= 6, "enough questions to be worth marking up");
  const c = copy();
  for (const q of data.mainEntity) {
    assert.equal(q["@type"], "Question");
    // Every marked-up question must actually be on the page - structured data that
    // answers questions the page does not is the definition of cloaking.
    const stem = q.name.replace(/[?.]/g, "").slice(0, 28);
    assert.ok(c.includes(stem), `the page asks: ${q.name}`);
  }
});
