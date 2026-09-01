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
  assert.match(c, /90%/, "the node's share");
  assert.match(c, /5%/, "the tower operator's share");
  assert.match(c, /(the platform simply takes less|platform)/i, "the platform's share is named");
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
  assert.match(c, /(not|rather than) .{0,20}the day you installed/i,
    "and that it does not track the install date");

  // "Traffic is early" is too vague on its own - an operator needs the two gates that
  // actually decide whether anything reaches them, neither of which they control.
  //
  // This assertion used to require the word "opt-in", because reaching the fabric was
  // opt-in when the article was written. Then `--tower` was removed, the sentence stopped
  // being true, and the test failed for the copy being CORRECTED. A test that pins a
  // transient fact turns every fix into a failure and teaches the next person to edit the
  // test instead of the claim. What endures is that the join is BEST EFFORT - a node with
  // no signed-in account, or one that cannot reach a relay, silently never arrives - so
  // that is what gets pinned.
  assert.match(c, /best effort|best-effort/i,
    "gate 1: the article says the relay join is best effort, not guaranteed");
  assert.match(c, /(signed[- ]in|cannot reach)/i,
    "and names at least one concrete way a node never arrives");
  // The flag is gone (see the --tower removal commit) and typing it now fails outright.
  // The article must never instruct anyone to run it - not in the copy, not in the
  // metadata, not in the structured data.
  assert.doesNotMatch(page(), /share\s+--tower/,
    "the article never tells a provider to run `roger share --tower`");
  assert.match(c, /first[- ]fit/i,
    "gate 2: Core assigns the Tower, and today that assignment is first-fit");
  assert.match(c, /(new(er)? Tower|newer one) .{0,40}(wait|idle)/i,
    "and the consequence for a new Tower is spelled out, not left to be discovered");

  // No projection of relay income, in any of the shapes a projection takes.
  assert.doesNotMatch(c, /\$\s?\d[\d,.]*\s*(\/|per\s*)?(mo|month|day|week|year)/i,
    "no per-period dollar figure");
  assert.doesNotMatch(c, /(earn|make|expect)\s+(up to\s+)?\$\s?\d/i, "no dollar promise");
  assert.doesNotMatch(c, /passive income|guaranteed|risk[- ]free/i, "no income-promise vocabulary");
});

test("the confidentiality claim is scoped to the relay, and admits what leaks", () => {
  const c = copy();
  // The guarantee is stated as a key never handed over, NOT as a limit the operator is
  // asked to respect - those two readings have very different failure modes.
  assert.match(c, /never handed a key|no key to the content|never .{0,20}given/i,
    "the content key is never given to the Tower");
  assert.match(c, /seal(s|ed)? .{0,40}(node's|node) .{0,20}key/i,
    "the work is sealed to the SERVING NODE's key - that is the mechanism");
  // The honest limit: shape is visible even when content is not.
  assert.match(c, /traffic shape/i, "it names what a Tower does see");
  assert.match(c, /how many bytes/i, "and is concrete about it");
});

test("the confidentiality claim is never phrased as a dare", () => {
  // "get paid to carry what YOU cannot read" reads as a challenge to try, and it puts the
  // guarantee in the operator's restraint rather than in the key they were never given.
  // Both are wrong, so the second-person framing is banned outright.
  const c = copy();
  const html = page();
  for (const dare of [/what you cannot read/i, /what you can't read/i,
                      /you cannot read (it|them|the)/i, /try to read/i,
                      /dare|challenge you/i]) {
    assert.doesNotMatch(c, dare, `challenge framing: ${dare}`);
  }
  // Including in the metadata and structured data, which is what gets syndicated.
  assert.doesNotMatch(html, /what you cannot read/i, "not in <head> or JSON-LD either");
  assert.doesNotMatch(read("broadcasts.html"), /what you cannot read/i,
    "and not in the transmission log");
});

test("standalone is offered as a private relay, with the trade stated", () => {
  const c = copy();
  assert.match(c, /standalone/i, "the mode is named");
  assert.match(c, /--mode standalone/, "and the exact flag is printed");
  assert.match(c, /own trust root/i, "it has its own trust root");
  assert.match(c, /private relay network/i, "described in the terms a person would search");
  assert.match(c, /loopback/i, "and its default binding is stated");
  // The trade is the honesty rail: standalone cannot earn, and not merely "not yet".
  assert.match(c, /[Ss]tandalone earns nothing/,
    "the page says outright that a private Tower earns nothing");
  assert.match(c, /construction|structural/i, "and that this is structural, not a policy");
  // The immutability catches people out, so it must be on the page.
  assert.match(c, /one mode for life|never changed in place|initializing a new one/i,
    "a data directory is one mode for life");
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
