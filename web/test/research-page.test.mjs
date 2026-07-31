import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (name) => readFileSync(path.join(DIST, name), "utf8");
const text = (name) => read(name).replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// research.html is now a hub; the model catalogue lives on research-models.html.
// `surface` is for assertions that are true of the research area as a whole and
// should not care which of the two pages carries the markup.
const surface = () => read("research.html") + read("research-models.html");
const surfaceText = () => text("research.html") + " " + text("research-models.html");


test("Research is a concise first-class destination", () => {
  const page = read("research.html");
  assert.match(page, /<title>RogerAI Research/);
  assert.match(page, /RogerAI Labs/);
  assert.match(page, /aria-current="page"/);
  assert.match(page, /rel="canonical" href="https:\/\/rogerai\.fm\/research\.html"/);
  const main = page.match(/<main[\s\S]*?<\/main>/)?.[0] || "";
  // Budget raised 18000 -> 19500 on founder direction: the industry section now names
  // the four focus markets (oil and gas, power generation, manufacturing, aerospace)
  // and the plant-interface standards an OT buyer checks for. That is the substance a
  // grant or enterprise reviewer came for, so it earns its bytes - but the ceiling stays
  // low on purpose. If a change needs more room, cut something before raising this.
  assert.ok(main.length < 19500, `research content is concise (${main.length} bytes)`);
});

// The mission family the founder asked for, in RogerAI's voice rather than a
// borrowed one. Each line is a commitment somebody could hold us to, which is why
// they are asserted: a mood survives a copy edit, a commitment should not.
test("the lab says why it exists, in terms it can be held to", () => {
  const why = read("research.html").match(/<section class="section" id="why"[\s\S]*?<\/section>/)?.[0] || "";
  assert.ok(why, "the why section exists");
  const copy = why.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");
  assert.match(copy, /Engineers for engineers/i);
  assert.match(copy, /Built in America, open to everyone/i);
  assert.match(copy, /Orange County/);
  // The two claims that are checkable rather than aspirational.
  assert.match(copy, /published as it happens|weights, recipes, raw evaluations/i);
  assert.match(copy, /nothing we publish needs us to keep existing/i, "the no-lock-in promise");
  // It must not drift into claiming a scale or a customer we do not have.
  assert.doesNotMatch(copy, /\b\d+\+? (employees|customers|partners)\b/i);
});

test("the page leads with the work, not a biography of the company", () => {
  const page = read("research.html");
  assert.match(page, /Models built for local constraints/i);
  assert.match(page, /smaller|less memory|local hardware/i);
  assert.doesNotMatch(page, /team|founders?|venture|employees|our origins/i);
});

test("the model list gives every program a reason and honest status", () => {
  const page = text("research-models.html");
  for (const model of [
    "DeepSeek-V4-Flash MTP",
    "Kimi-K3",
    "Wave Core",
    "Wave Micro",
    "Wave Nano",
    "Roger Edge",
  ]) assert.match(page, new RegExp(model));
  for (const reason of [
    /practical local inference/i,
    /high-memory workstation/i,
    /general local reasoning/i,
    /local text and tool use/i,
    /routing, extraction, and triage/i,
    /wake, voice activity, sensing, and fixed commands/i,
  ]) assert.match(page, reason);
  assert.match(page, /Research build/i);
  assert.match(page, /In design/i);
  assert.match(page, /In progress/i);
  // The fabricated artifact id is gone. Naming-CONVENTION placeholders on design
  // rows (wave-core-1b-instruct, wave-nano-<size>-<task>) are fine - they document
  // the id scheme, they are not download claims.
  assert.doesNotMatch(page, /wave-micro-350m-instruct/i);
});

test("the model scope plots parameter class as radar range on a true log axis", () => {
  const page = surface();
  const css = read("styles/research.css");
  const scope = page.match(/<figure class="scope"[\s\S]*?<\/figure>/)?.[0];
  assert.ok(scope, "the Wave scope exists");

  const order = ["Roger Edge", "Wave Nano", "Wave Micro", "Wave Core"];
  let cursor = -1;
  for (const name of order) {
    const next = scope.indexOf(name);
    assert.ok(next > cursor, `${name} follows the smaller class`);
    cursor = next;
  }

  // The proof that the axis is really logarithmic: consecutive DECADE rings must be
  // equally spaced in radius. On a linear axis they would not be, and the plot would
  // be implying a proportion the five-order-of-magnitude family does not have.
  const rings = [...scope.matchAll(/<circle cx="200" cy="200" r="(\d+)"\/>/g)].map((m) => +m[1]);
  assert.ok(rings.length >= 5, `expected decade rings, found ${rings.length}`);
  const gaps = rings.slice(1).map((r, i) => r - rings[i]);
  const spread = Math.max(...gaps) - Math.min(...gaps);
  assert.ok(spread <= 1, `decade rings are evenly spaced (gaps ${gaps.join(",")})`);

  // Every class is a range gate: a line from its inner to its outer radius. Both
  // endpoints must sit inside the scope, and the gate must span outward.
  const gates = [...scope.matchAll(/<line class="scope__gate" x1="([\d.]+)" y1="([\d.]+)" x2="([\d.]+)" y2="([\d.]+)"/g)];
  assert.equal(gates.length, order.length, "every class has a range gate");
  const radius = (x, y) => Math.hypot(x - 200, y - 200);
  for (const g of gates) {
    const inner = radius(+g[1], +g[2]);
    const outer = radius(+g[3], +g[4]);
    assert.ok(outer > inner, `gate spans outward (${inner.toFixed(1)} -> ${outer.toFixed(1)})`);
    assert.ok(outer <= 200, "the gate stays inside the scope");
  }

  for (const tick of ["100K", "1M", "10M", "100M", "1B"]) {
    assert.ok(scope.includes(`>${tick}<`), `range ring is labelled at ${tick}`);
  }

  // Bearing is decoration; saying so is the difference between a plot and a lie.
  // Normalise whitespace first - the caption wraps across source lines.
  const prose = scope.replace(/\s+/g, " ");
  assert.match(prose, /[Bb]earing carries no meaning/);
  assert.match(prose, /declared design targets, not measurements/i);
  assert.doesNotMatch(scope, /\b\d+(\.\d+)?\s?(GB|MB|tok\/s|ms)\b/, "no invented footprint or speed");

  assert.match(page, /RogerAI-designed open model program/i);
  assert.match(page, /release gate|no checkpoint/i, "the scope states program status, not availability");
  assert.doesNotMatch(page, /Wave Edge/i);
  for (const selector of [".scope", ".scope__gate", ".model-group-head"]) {
    assert.match(css, new RegExp(selector.replace(".", "\\.")), `${selector} is styled`);
  }
  // Drawn is the DEFAULT state; the sweep and reveal are both reduced-motion safe.
  assert.match(css, /prefers-reduced-motion[\s\S]*scope__sweep/i);
});

test("model identity follows family size variant conventions", () => {
  const page = read("research-models.html");
  for (const id of [
    "roger-edge-&lt;task&gt;-&lt;size&gt;",
    "wave-nano-&lt;size&gt;-&lt;task&gt;",
    "wave-core-1b-instruct",
  ]) assert.match(page, new RegExp(id));
  assert.match(page, /Family · parameter class · variant/i);
});

test("upstream optimization keeps visible lineage and license", () => {
  const page = read("research-models.html");
  assert.match(page, /Optimized by RogerAI/);
  assert.match(page, /Upstream: DeepSeek-V4-Flash/);
  assert.match(page, /Upstream: Kimi-K3/);
  assert.match(page, /MIT License/i);
  assert.match(page, /Kimi K3 License/i);
  assert.match(page, /284B total[^<]*13B active/i);
  assert.match(page, /2\.8T total[^<]*104B active/i);
  assert.doesNotMatch(page, /RogerAI (?:pre)?trained (?:DeepSeek|Kimi)/i);
});

test("evidence and local-use promises stay explicit", () => {
  const page = text("research.html");
  assert.match(page, /hardware, artifact, runtime, settings, and raw results/i);
  assert.match(page, /Negative results remain available/i);
  assert.match(page, /download and run qualifying artifacts locally/i);
  assert.match(page, /network is optional/i);
  const html = read("research.html");
  assert.match(html, /href="\/broadcasts\.html"/);
  assert.match(html, /href="https:\/\/huggingface\.co\/rogerai-fyi"/);
  assert.match(html, /href="https:\/\/github\.com\/rogerai-fyi\/roger"/);
});

test("only released artifacts present download controls", () => {
  const page = read("research-models.html");
  assert.match(page, /href="https:\/\/huggingface\.co\/rogerai-fyi\/DeepSeek-V4-Flash-MTP-GGUF"/);
  // Exactly one download control: the DeepSeek research build, which is public.
  // Wave rungs have nothing to download, so they present none.
  assert.equal((page.match(/class="model-download"/g) || []).length, 1);
  assert.doesNotMatch(page, /coming soon/i);
  assert.doesNotMatch(page, /aria-disabled="true"/);
  for (const stage of [
    /16K saliency calibration/,
    /Sequenced after Wave Micro/,
    /Architecture and data design/,
    /Hardware and dataset design/,
  ]) assert.match(page, stage);
  assert.doesNotMatch(page, /href="[^"]*(?:placeholder|coming-soon)[^"]*"/i);
});

test("Wave Micro publishes its program status instead of a release contract", () => {
  const page = read("research-models.html");
  const card = page.match(/<article class="model-row"[^>]*id="wave-micro"[\s\S]*?<\/article>/)?.[0] || "";
  assert.ok(card, "the Wave Micro row exists");
  const cardText = card.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

  // Nothing that implies a shipping artifact.
  for (const claim of [/AVAILABLE/, /wave-micro-350m-instruct/, /v1\.0/, /GGUF/, /Q4_K_M/]) {
    assert.doesNotMatch(cardText, claim);
  }
  assert.doesNotMatch(card, /class="model-download"/);
  assert.doesNotMatch(card, /huggingface\.co\/rogerai-fyi\/wave-/);

  // The real, checkable status - and an explicit refusal to print placeholder
  // evidence. The previous version of this card listed five evidence fields that
  // all deferred to a model card that did not exist, which looked like rigour.
  assert.match(cardText, /IN PROGRESS/i);
  assert.match(cardText, /bake-off/i);
  assert.match(cardText, /not yet approved/i);
  assert.match(cardText, /no checkpoint released/i);
  assert.match(cardText, /No evidence contract yet/i);
  assert.match(text("research-models.html"), /separate network terms/i);
});

test("developers have a compact path from artifact to local inference", () => {
  const page = read("research.html");
  assert.match(page, /id="developers"/);
  // Download -> run -> broadcast. The third step closes the loop into the network
  // instead of restating the evaluation contract, which §4 already carries.
  assert.match(page, /Download/);
  assert.match(page, /Run locally/);
  assert.match(page, /Broadcast/);
  assert.match(page, /huggingface-cli download/);
  assert.match(page, /llama-server/);
  assert.match(page, /OpenAI-compatible endpoint/i);
  assert.match(page, /roger share/);
  // Going on air stays optional - the local path must never read as requiring it.
  assert.match(page, /Optional/i);
});

test("research and the live network directory remain distinct", () => {
  const page = read("research.html");
  assert.match(page, /href="\/models\.html"/);
  assert.match(page, /live network models/i);
  assert.match(page, /research programs/i);
});

test("company handoff resolves to concise industry deployment patterns", () => {
  const page = read("research.html");
  assert.match(page, /<section[^>]+id="industry"/);
  // The founder-named focus markets, in the founder's own terms.
  for (const market of [
    /oil and gas/i,
    /power generation/i,
    /manufacturing/i,
    /aerospace/i,
  ]) assert.match(page, market);
  assert.match(page, /deployment patterns, not customer case studies/i);
  assert.match(page, /advisory/i);
  assert.match(page, /closed-loop control/i);
});

// An industrial buyer and a grant reviewer both check the same thing first: does
// this vendor speak the plant's language, and does it know where its box is
// allowed to sit? Naming the standards is the cheapest, highest-signal proof that
// the work is grounded in operational technology rather than in a demo.
test("the industrial interface is described in the plant's own standards", () => {
  const copy = text("research.html");
  for (const standard of [
    /OPC UA/,             // tags
    /Modbus/,             // brownfield fieldbus
    /Sparkplug B/,        // MQTT payload spec behind the unified namespace
    /ISA-95/,             // asset hierarchy
    /NE 107/,             // NAMUR device-health status
    /IEC 62443/,          // zones and conduits
    /Purdue/,             // network level placement
  ]) assert.match(copy, standard, `names ${standard}`);
  // The one architectural promise an OT security team asks for by name.
  assert.match(copy, /outbound/i);
  assert.match(copy, /no inbound|without.{0,20}inbound|exposes no inbound/i);
});

test("the industrial pitch states what the model must never touch", () => {
  const copy = text("research.html");
  assert.match(copy, /protection|interlock|safety-instrumented/i);
  assert.match(copy, /deterministic/i);
  assert.match(copy, /beside|alongside/i, "positioned next to classical analytics, not replacing it");
});

test("device and roadmap claims remain gated", () => {
  const page = surface();
  const copy = surfaceText();
  assert.match(page, /Raspberry Pi/i);
  assert.match(page, /ESP32/i);
  assert.match(copy, /exact board.*runtime.*format.*quantization/i);
  assert.match(copy, /does not run Wave Micro/i);
  assert.match(copy, /full delegation.*partial delegation.*CPU fallback/i);
  // The Wave Tool / Vision / Audio roadmap block was retired on founder direction.
  // Those modalities are now absent entirely, so the honest invariant is that they
  // are never NAMED as things you could have - not that a gating sentence exists.
  for (const unshipped of ["Wave Tool", "Wave Vision", "Wave Audio"]) {
    assert.doesNotMatch(page, new RegExp(unshipped), `${unshipped} is not advertised`);
  }
});

test("services are optional and preserve local model independence", () => {
  const page = read("research.html");
  assert.match(page, /id="services"/);
  for (const service of [
    /optimization engineering/i,
    /edge deployment/i,
    /benchmarking/i,
    /industrial pilots/i,
  ]) assert.match(page, service);
  assert.match(page, /never requires buying services/i);
  assert.match(page, /services never require the network/i);
  assert.match(page, /mailto:labs@rogerai\.fm/);
});

test("new institutional pages contain no em dash character", () => {
  for (const name of ["research.html", "company.html", "index.html"]) {
    assert.doesNotMatch(read(name), /—/, `${name} contains an em dash`);
  }
});

// SVG has no z-index: source order IS paint order. The sweep was drawn after the contacts
// and painted over them, so the blips the figure exists to show were invisible under it.
// Geometry assertions cannot see this, which is how it shipped.
test("the radar sweep is painted before the contacts it must not cover", () => {
  const page = read("research.html");
  const sweep = page.indexOf("scope__sweep");
  const contacts = page.indexOf("scope__contacts");
  assert.ok(sweep >= 0 && contacts >= 0, "both layers are present");
  assert.ok(sweep < contacts, "the sweep must come first in source, or it paints over the blips");
});
