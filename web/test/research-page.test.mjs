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

test("Research is a concise first-class destination", () => {
  const page = read("research.html");
  assert.match(page, /<title>RogerAI Research/);
  assert.match(page, /RogerAI Labs/);
  assert.match(page, /aria-current="page"/);
  assert.match(page, /rel="canonical" href="https:\/\/rogerai\.fm\/research\.html"/);
  const main = page.match(/<main[\s\S]*?<\/main>/)?.[0] || "";
  // Budget raised again 19500 -> 21000: the hero mascot became the inline VECTOR
  // Ping (1291 bytes of path data) instead of five lines of ASCII. That is
  // STRUCTURAL cost, not prose - this ceiling exists to discipline copy, so the
  // increase is exactly the illustration and any future prose growth still trips it.
  // Budget raised 18000 -> 19500 on founder direction: the industry section now names
  // the four focus markets (oil and gas, power generation, manufacturing, aerospace)
  // and the plant-interface standards an OT buyer checks for. That is the substance a
  // grant or enterprise reviewer came for, so it earns its bytes - but the ceiling stays
  // low on purpose. If a change needs more room, cut something before raising this.
  assert.ok(main.length < 21000, `research content is concise (${main.length} bytes)`);
});

test("the page leads with the work, not a biography of the company", () => {
  const page = read("research.html");
  assert.match(page, /Models built for local constraints/i);
  assert.match(page, /smaller|less memory|local hardware/i);
  assert.doesNotMatch(page, /team|founders?|venture|employees|our origins/i);
});

test("the model list gives every program a reason and honest status", () => {
  const page = text("research.html");
  for (const model of [
    "DeepSeek-V4-Flash MTP",
    "Kimi-K3",
    "Wave Core",
    "Wave Nano",
    "Wave Micro",
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
  assert.match(page, /Available/i);
  assert.match(page, /In design/i);
  assert.match(page, /wave-nano-350m-instruct v1\.0/i);
});

test("the model ladder is visually sorted by increasing parameter class", () => {
  const page = read("research.html");
  const css = read("styles/research.css");
  const spectrum = page.match(/<figure class="size-spectrum"[\s\S]*?<\/figure>/)?.[0];
  assert.ok(spectrum, "size spectrum exists");
  const order = ["Roger Edge", "Wave Micro", "Wave Nano", "Wave Core"];
  let cursor = -1;
  for (const name of order) {
    const next = spectrum.indexOf(name);
    assert.ok(next > cursor, `${name} follows the smaller tier`);
    cursor = next;
  }
  for (const size of ["KB–10M", "&lt;100M", "~350M", "1B-class"]) {
    assert.match(spectrum, new RegExp(size));
  }
  assert.match(page, /RogerAI-designed open model program/i);
  assert.match(page, /Wave Nano v1\.0 available/i);
  assert.match(text("research.html"), /Roger Edge.*(?:task|microcontroller)/i);
  assert.doesNotMatch(page, /Wave Edge/i);
  for (const selector of [".size-spectrum", ".size-spectrum__bar", ".model-group-head"]) {
    assert.match(css, new RegExp(selector.replace(".", "\\.")), `${selector} is styled`);
  }
  assert.match(css, /@media \(max-width: 560px\)[\s\S]*size-spectrum/i);
});

test("model identity follows family size variant conventions", () => {
  const page = read("research.html");
  for (const id of [
    "roger-edge-&lt;task&gt;-&lt;size&gt;",
    "wave-micro-&lt;size&gt;-&lt;task&gt;",
    "wave-nano-350m-instruct",
    "wave-core-1b-instruct",
  ]) assert.match(page, new RegExp(id));
  assert.match(page, /Family · parameter class · variant/i);
});

test("upstream optimization keeps visible lineage and license", () => {
  const page = read("research.html");
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
  const page = read("research.html");
  assert.match(page, /href="https:\/\/huggingface\.co\/rogerai-fyi\/DeepSeek-V4-Flash-MTP-GGUF"/);
  assert.equal((page.match(/class="model-download"/g) || []).length, 2);
  assert.doesNotMatch(page, /coming soon/i);
  assert.doesNotMatch(page, /aria-disabled="true"/);
  for (const stage of [
    /16K saliency calibration/,
    /Sequenced after Wave Nano/,
    /Architecture and data design/,
    /Hardware and dataset design/,
  ]) assert.match(page, stage);
  assert.doesNotMatch(page, /href="[^"]*(?:placeholder|coming-soon)[^"]*"/i);
});

test("the released Wave Nano checkpoint publishes its release contract", () => {
  const page = read("research.html");
  const card = page.match(/<article class="model-row"[^>]*id="wave-nano-350m-instruct"[\s\S]*?<\/article>/)?.[0] || "";
  const cardText = card.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");
  for (const fact of [
    /AVAILABLE/,
    /wave-nano-350m-instruct/,
    /v1\.0/,
    /350M/,
    /instruct/,
    /GGUF/,
    /Q4_K_M/,
    /Artifact license: Apache-2\.0/i,
  ]) assert.match(cardText, fact);
  assert.doesNotMatch(cardText, /Apache-2\.0 intended|pending final legal confirmation/i);
  assert.match(card, /href="https:\/\/huggingface\.co\/rogerai-fyi\/wave-nano-350m-instruct"/);
  assert.match(card, /Weights|Model card|License|Source|Evaluations|Limitations/i);
  assert.match(text("research.html"), /local use does not require RogerAI or its broker/i);
  assert.match(text("research.html"), /separate network terms/i);
  for (const field of [/tested hardware/i, /runtime/i, /context/i, /peak memory/i, /measured speed/i]) {
    assert.match(cardText, field);
  }
});

test("developers have a compact path from artifact to local inference", () => {
  const page = read("research.html");
  assert.match(page, /id="developers"/);
  assert.match(page, /Download/);
  assert.match(page, /Run locally/);
  assert.match(page, /Reproduce/);
  assert.match(page, /huggingface-cli download/);
  assert.match(page, /llama-server/);
  assert.match(page, /OpenAI-compatible endpoint/i);
  assert.match(page, /artifact version[^<]*hardware[^<]*runtime[^<]*settings/i);
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
  const page = read("research.html");
  const copy = text("research.html");
  assert.match(page, /Raspberry Pi/i);
  assert.match(page, /ESP32/i);
  assert.match(copy, /exact board.*runtime.*format.*quantization/i);
  assert.match(copy, /does not run Wave Nano/i);
  assert.match(copy, /full delegation.*partial delegation.*CPU fallback/i);
  for (const roadmap of ["Wave Tool", "Wave Vision", "Wave Audio"]) {
    assert.match(page, new RegExp(roadmap));
  }
  assert.match(page, /gated on Wave Nano/i);
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
