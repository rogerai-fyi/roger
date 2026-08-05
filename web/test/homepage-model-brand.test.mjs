// Executable behavior lock for the homepage model/company/Tube Ping slice in
// features/web/homepage_company_research_branding.feature.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const ROOT = path.join(WEB, "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const visible = (value) => value.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

test("the homepage orders the compact right-sized model spectrum", () => {
  const home = read("index.html");
  const spectrum = home.match(/<ol class="home-spectrum"[\s\S]*?<\/ol>/)?.[0] || "";
  const order = ["Roger Edge", "Wave Nano", "Wave Micro", "Wave Core"];
  let cursor = -1;
  for (const name of order) {
    const next = spectrum.indexOf(name);
    assert.ok(next > cursor, `${name} follows the smaller tier`);
    cursor = next;
  }
  for (const phrase of ["fixed sensing", "routing + extraction", "local text + tools", "local reasoning"]) {
    assert.match(visible(spectrum), new RegExp(phrase.replace("+", "\\+"), "i"));
  }
  assert.match(spectrum, /href="\/research\.html"/);
});

test("the homepage Labs card presents Wave Micro as a program, not a download", () => {
  const section = read("index.html").match(/<section\b[^>]*id="company"[\s\S]*?<\/section>/)?.[0] || "";
  const sectionText = visible(section);
  // Nothing here may read as a shipping artifact while the checkpoint is unpublished.
  for (const claim of [/AVAILABLE/, /wave-micro-350m-instruct/, /v1\.0/, /GGUF/, /Q4_K_M/]) {
    assert.doesNotMatch(sectionText, claim, `unreleased model must not advertise ${claim}`);
  }
  assert.doesNotMatch(section, /Download or Run Wave/, "no download CTA without a download");
  assert.doesNotMatch(section, /huggingface\.co\/rogerai-fyi\/wave-/, "no artifact link");
  // What it MUST say instead: the honest status, and the independence promise.
  assert.match(sectionText, /TRAINED/i);
  assert.match(sectionText, /no checkpoint has been released/i);
  // Internal programme state is deliberately NOT published. It is not something a visitor
  // needs, and it creates a claim that has to be kept current as the work moves.
  assert.doesNotMatch(sectionText, /bake-off/i, "no internal programme stage on a public page");
  assert.match(section, /href="\/research\.html"/);
  assert.match(sectionText, /local use will not require RogerAI or its broker/i);
  assert.match(sectionText, /separate network terms/i);
});

test("the Company preview carries factual origin and component-specific openness", () => {
  const section = visible(read("index.html").match(/<section\b[^>]*id="company"[\s\S]*?<\/section>/)?.[0] || "");
  assert.match(section, /Orange County, California/);
  // These assertions guard a FACTUAL CLAIM about the company, so they track the copy
  // rather than the other way round. The claim narrowed from "independently owned and not
  // venture funded" to "founder-led", which is a weaker statement: a founder-led company
  // can still be externally funded. Kept as an assertion so the page cannot quietly stop
  // saying what kind of company this is.
  assert.match(section, /founder[- ]led/i);
  assert.match(section, /open model and runtime work/i);
  assert.match(section, /PolyForm Perimeter/i);
});

test("the website renders the HIGH-FIDELITY vector mascot, never the terminal art", () => {
  const home = read("index.html");
  const mascot = home.match(/<figure class="tube-ping"[\s\S]*?<\/figure>/)?.[0] || "";
  assert.match(mascot, /aria-label="Ping, the RogerAI on-air mascot"/);
  assert.match(mascot, /<svg class="tube-ping__mark"/, "the web mascot is a vector, not a <pre>");
  // The viewBox gained headroom above y=0 so the signal arcs are not clipped by the edge
  // an outermost SVG enforces. Width and the drawn body are unchanged; assert the shape
  // rather than one literal, so raising the signal again does not read as a broken mascot.
  const vb = mascot.match(/viewBox="([\d.-]+) ([\d.-]+) ([\d.-]+) ([\d.-]+)"/);
  assert.ok(vb, "the mascot declares a viewBox");
  assert.equal(Number(vb[1]), 0, "canonical mascot.svg geometry: x origin");
  assert.equal(Number(vb[3]), 64, "canonical mascot.svg geometry: width");
  assert.ok(Number(vb[2]) <= 0, "any extra band is headroom above the mascot, never a crop");
  assert.equal(Number(vb[4]) - Math.abs(Number(vb[2])), 72, "the drawn body keeps its height");
  assert.equal((mascot.match(/class="ping-mark__eye"/g) || []).length, 1, "one live-red beacon eye");
  assert.doesNotMatch(mascot, /<pre/, "no monospace sprite on a surface that can draw curves");

  // The ASCII Tube Ping is the TERMINAL costume. v5.4.8 put it on the marketing
  // site, which is what made the browser render block glyphs; guard the boundary
  // rather than the glyphs, so fixing the TUI art can never leak here again.
  const plain = home.replace(/<[^>]+>/g, "");
  // A glyph CLASS, not literal runs: the ASCII art has already changed width once,
  // and literals would silently start asserting the absence of strings that exist
  // nowhere. Any run of terminal block glyphs on a web page is the bug.
  assert.doesNotMatch(plain, /[\u2580-\u259F]{3,}/, "no terminal block-glyph run reaches the web page");
});

test("the Labs card keeps its model ID inside narrow layouts", () => {
  const css = read("styles/home.css");
  assert.match(css, /\.company__card code\s*\{[^}]*overflow-wrap:\s*anywhere/);
});
