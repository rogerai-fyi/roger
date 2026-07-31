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

test("the model catalogue points at it without misfiling it as a model", () => {
  const cat = read("research-models.html");
  assert.match(cat, /href="\/research-wave-family\.html#infinite"/, "the catalogue links it");
  assert.match(visible(cat), /runtime rather than a model|not a model/i, "and says why it is not listed");
  // It must not appear as a catalogue ENTRY beside the size classes.
  const entries = [...cat.matchAll(/<h3[^>]*>([^<]+)<\/h3>/g)].map((m) => m[1]);
  assert.ok(!entries.some((e) => /Wave Infinite/i.test(e)), "it is not a row in the model list");
});

test("it is a runtime, not a fifth size class", () => {
  const copy = visible(section());
  assert.match(copy, /runtime/i, "named as a runtime");
  assert.match(copy, /runs under|layer a model|under a model/i, "it sits under a model, not beside one");
  // It must never be given a parameter class, which would file it as a size.
  assert.doesNotMatch(copy, /Wave Infinite[^.]{0,80}\b\d+\s?(B|M)-class\b/i, "no parameter class");
  // And it must not appear as a row in the size or jobs tables.
  const page = read("research-wave-family.html");
  for (const id of ["slots", "jobs"]) {
    const table = page.match(new RegExp(`<section[^>]*id="${id}"[\\s\\S]*?</section>`))[0];
    assert.doesNotMatch(table, /Wave Infinite/, `${id} table stays a table of sizes`);
  }
  // The scope plots sizes, so it must not draw a contact for the runtime.
  assert.doesNotMatch(read("research.html"), /data-slot="wave-infinite"/, "not on the size axis");
});

test("the name is explained where it is used, as a theorem", () => {
  const copy = visible(section());
  assert.match(copy, /infinite in specification/i);
  assert.match(copy, /finite in implementation/i);
  assert.match(copy, /theorem/i, "presented as a theorem, not a capability");
});

test("the definition is the brief's own", () => {
  const copy = visible(section());
  assert.match(copy, /certificate/i);
  assert.match(copy, /behaviour-preserving|does not change|unchanged/i);
  assert.match(copy, /region/i);
  assert.match(copy, /falls back|fallback|deoptimi/i, "and the way out is stated");
});

test("what is proven is attributed, and stated exactly", () => {
  const copy = visible(section());
  assert.match(copy, /0 of 391,386|zero of 391,386/i, "the sceptic's sentence");
  assert.match(copy, /bit-identical/i);
});

test("what is NOT proven appears in the main flow, not a footnote", () => {
  const sec = section();
  const copy = visible(sec);
  assert.match(copy, /no (demonstrated )?(performance|speed)|speedup is unmeasured|unmeasured/i);
  assert.match(copy, /soundness is proven/i);
  assert.match(copy, /self-improvement[^.]*(unmeasured|out of scope)/i);
  // Not carried only by a title attribute or a caption.
  const flow = visible(sec.replace(/<figcaption[\s\S]*?<\/figcaption>/g, "").replace(/title="[^"]*"/g, ""));
  assert.match(flow, /unmeasured/i, "the limit survives without the caption");
});

test("it is never described as a working system", () => {
  const copy = visible(section());
  for (const banned of [/self-learning/i, /self-improving/i, /trains itself/i,
                        /available now/i, /download/i, /\bships\b/i]) {
    assert.doesNotMatch(copy, banned, `must not say ${banned}`);
  }
  assert.doesNotMatch(section(), /href="https:\/\/huggingface\.co[^"]*infinite/i, "no artifact link");
  assert.doesNotMatch(copy, /\b\d+(\.\d+)?\s?(x faster|tok\/s|GB\/s)\b/i, "no speed claim");
});

test("the build stages are shown at their real state", () => {
  const copy = visible(section());
  for (const [stage, state] of [["Reify", /built|done|verified/i], ["Certify", /validated|proven/i]]) {
    assert.match(copy, new RegExp(stage, "i"), `${stage} is shown`);
    assert.ok(state.test(copy), `${stage} carries a state`);
  }
  for (const stage of ["Specialise", "Guard"]) assert.match(copy, new RegExp(stage, "i"));
  assert.match(copy, /not built|not started|unrun/i, "the unbuilt stages say so");
  assert.doesNotMatch(copy, /\bQ[1-4]\b|\b20(2[6-9]|3\d)\b.*(ship|release|deliver)/i, "no delivery dates");
});

test("the three words are scored honestly", () => {
  const copy = visible(section());
  assert.match(copy, /reflect/i);
  assert.match(copy, /evolv/i);
  assert.match(copy, /nothing learns|no weights change|does not learn/i);
});

test("the shimmer stays inside the RogerAI palette and carries no information", () => {
  const css = readFileSync(path.join(WEB, "src", "styles", "wave-family.css"), "utf8");
  const block = css.slice(css.indexOf(".wf-inf"));
  assert.ok(block.length > 0, "the treatment is styled");
  // No rainbow: the page spends exactly one accent, and it is ours.
  const hues = [...block.matchAll(/#[0-9a-f]{3,8}\b/gi)].map((m) => m[0].toLowerCase());
  assert.equal(hues.length, 0, `no raw hex colours in the treatment, found ${hues.join(", ")}`);
  assert.doesNotMatch(block, /hsl\(|rainbow|violet|indigo/i, "no multi-hue spectrum");
  // Reduced motion must stop it, and the static state must still read as distinct.
  const reduced = (css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/g) || [])
    .find((b) => b.includes("wf-inf"));
  assert.ok(reduced, "the shimmer has a reduced-motion escape");
  assert.match(reduced, /animation: none/);
});

test("it survives with no JavaScript", () => {
  const sec = section();
  // Nothing in the section may be injected or revealed by script.
  assert.doesNotMatch(sec, /<script/i, "no inline script");
  assert.match(visible(sec), /Wave Infinite/, "the copy is in the served markup");
});

// The overclaim this page shipped with, and the reason it is easy to make: "part of the
// family" slides into "works with all of it". The certificate is reachability
// certification of MoE EXPERTS - the technical phrasing in the explainer says so - and
// the base model needs a per-expert selection-bias tensor. A dense model has no experts
// to certify, and Roger Edge is not a language model at all.
test("the page states the constraint instead of implying it works with everything", () => {
  const copy = visible(section());
  assert.match(copy, /mixture-of-experts|MoE/i, "the mechanism's precondition is named");
  assert.match(copy, /selection bias|per-expert|experts to certify|has experts/i,
    "and what the base model has to provide");
  assert.match(copy, /Roger Edge/, "the slot it explicitly does not cover is named");
  // The claim that started this: nothing may say it runs under any or every slot.
  for (const overclaim of [/any of (them|these) could run under/i, /across the family/i,
                           /every slot could/i, /works with (all|any)/i]) {
    assert.doesNotMatch(copy, overclaim, `must not claim ${overclaim}`);
  }
});
