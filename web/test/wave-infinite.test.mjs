// Executable lock for features/web/wave_infinite.feature.
//
// Wave Infinite is a model RUNTIME, not a fifth size class, and it is a design with one
// causally validated primitive and zero demonstrated speedup. Its own brief sets the
// ceiling ("must not be described as a working system") and names the naming risk
// ("infinite in specification, finite in implementation ... a theorem, not a capability
// claim"). This is the same failure mode as the retracted Wave Micro release, so most of
// these assertions are about what the page must NOT say.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const visible = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const section = () => {
  const s = read("research-wave-family.html").match(/<section[^>]*id="infinite"[\s\S]*?<\/section>/)?.[0];
  assert.ok(s, "the family page carries a Wave Infinite section");
  return s;
};

test("Wave Infinite is present and reachable", () => {
  assert.match(visible(section()), /Wave Infinite/);
  assert.match(read("research.html"), /#infinite|Wave Infinite/, "the hub points at it");
});

// The canonical doc is explicit: "It is NOT 'just a runtime'; the certified runtime is
// one layer of it." An earlier version of this page led with "a runtime, not a size".
test("it is a prototype programme, and the runtime is one layer of it", () => {
  const copy = visible(section());
  assert.match(copy, /prototype/i, "named as a prototype");
  assert.match(copy, /programme|program/i, "and as a programme");
  assert.match(copy, /layer/i, "which has layers");
  assert.doesNotMatch(copy, /a runtime, not a|is (just )?a runtime\b/i,
    "it must not be reduced to a runtime");
});

test("it is still not a size class", () => {
  const copy = visible(section());
  assert.doesNotMatch(copy, /Wave Infinite[^.]{0,80}\b\d+\s?(B|M)-class\b/i, "no parameter class");
  const page = read("research-wave-family.html");
  for (const id of ["slots", "jobs"]) {
    const table = page.match(new RegExp(`<section[^>]*id="${id}"[\\s\\S]*?</section>`))[0];
    assert.doesNotMatch(table, /Wave Infinite/, `${id} table stays a table of sizes`);
  }
  assert.doesNotMatch(read("research.html"), /data-slot="wave-infinite"/, "not on the size axis");
});

test("the measurement that forced it to exist leads", () => {
  const copy = visible(section());
  assert.match(copy, /invisible|hides? inside|benchmark noise/i, "damage hides in the noise");
  assert.match(copy, /cannot monitor your way/i, "and the conclusion is stated");
  assert.match(copy, /workload (shifts|changes)/i, "and what happens when the workload moves");
});

test("each layer is shown at its real state", () => {
  const sec = section();
  const layers = [...sec.matchAll(/<li data-layer="([^"]+)" data-state="([^"]+)"[^>]*>/g)]
    .map((m) => [m[1], m[2]]);
  assert.equal(layers.length, 3, "three layers");
  assert.deepEqual(layers.map((l) => l[0]), ["reflection", "certified", "growth"]);
  assert.deepEqual(layers.map((l) => l[1]), ["built", "measured", "unrun"]);
  const copy = visible(sec);
  assert.match(copy, /in development|preregistered|unrun/i, "the third layer says it is not done");
});

// The hardest constraint in the program doc, quoted: "Never claim 'self-training' in
// public material until CURE's gates pass ... externally it is an overclaim until
// measured." This is the assertion that keeps that promise.
test("the page never claims self-training", () => {
  const copy = visible(section());
  for (const banned of [/self-training/i, /self-improving/i, /trains itself/i,
                        /learns by itself/i, /improves itself today/i,
                        /available now/i, /download/i]) {
    assert.doesNotMatch(copy, banned, `must not say ${banned}`);
  }
});

test("what is proven carries its evidence", () => {
  const copy = visible(section());
  assert.match(copy, /0 of 391,386/, "the certificate result");
  assert.match(copy, /bit-identical/i);
  assert.match(copy, /0\.009\s?%/, "the in-domain guard rate");
  assert.match(copy, /20\s?%|a fifth/i, "and the cross-domain one");
});

test("what is NOT proven is published deliberately and in the main flow", () => {
  const sec = section();
  const copy = visible(sec);
  assert.match(copy, /unmeasured|in measurement|not (yet )?proven/i);
  assert.match(copy, /speed|tokens per second|tok\/s/i, "the speed benefit is named as unmeasured");
  assert.match(copy, /prototype/i);
  const flow = visible(sec.replace(/<figcaption[\s\S]*?<\/figcaption>/g, "").replace(/title="[^"]*"/g, ""));
  assert.match(flow, /unmeasured|not (yet )?proven|in measurement/i,
    "the limit survives without the caption");
});

test("the name is explained where it is used", () => {
  const copy = visible(section());
  assert.match(copy, /finite/i);
  assert.match(copy, /tower|self-observation/i);
  assert.match(copy, /Smith|Wand|1982|1986/, "attributed to the result it borrows");
});

test("the model catalogue points at it without misfiling it as a model", () => {
  const cat = read("research-models.html");
  assert.match(cat, /href="\/research-wave-family\.html#infinite"/, "the catalogue links it");
  const entries = [...cat.matchAll(/<h3[^>]*>([^<]+)<\/h3>/g)].map((m) => m[1]);
  assert.ok(!entries.some((e) => /Wave Infinite/i.test(e)), "it is not a row in the model list");
});

test("no page anywhere reduces it to a runtime or claims self-training", () => {
  for (const page of ["index.html", "research.html", "research-models.html",
                      "research-wave-family.html", "company.html"]) {
    const copy = visible(read(page));
    assert.doesNotMatch(copy, /self-training|self-improving|trains itself/i,
      `${page} must not claim self-training`);
    assert.doesNotMatch(copy, /whole family could run under|runtime the whole family/i,
      `${page} must not claim family-wide coverage`);
  }
});

test("the shimmer stays inside the RogerAI palette and carries no information", () => {
  const css = readFileSync(path.join(WEB, "src", "styles", "wave-family.css"), "utf8");
  const block = css.slice(css.indexOf(".wf-inf"));
  assert.ok(block.length > 0, "the treatment is styled");
  const hues = [...block.matchAll(/#[0-9a-f]{3,8}\b/gi)].map((m) => m[0].toLowerCase());
  assert.equal(hues.length, 0, `no raw hex colours in the treatment, found ${hues.join(", ")}`);
  assert.doesNotMatch(block, /hsl\(|rainbow|violet|indigo/i, "no multi-hue spectrum");
  const reduced = (css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/g) || [])
    .find((b) => b.includes("wf-inf"));
  assert.ok(reduced, "the shimmer has a reduced-motion escape");
  assert.match(reduced, /animation: none/);
});

test("it survives with no JavaScript", () => {
  const sec = section();
  assert.doesNotMatch(sec, /<script/i, "no inline script");
  assert.match(visible(sec), /Wave Infinite/, "the copy is in the served markup");
});
