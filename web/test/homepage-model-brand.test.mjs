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
  const order = ["Roger Edge", "Wave Micro", "Wave Nano", "Wave Core"];
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

test("Wave Nano has a direct shipping checkpoint contract", () => {
  const section = read("index.html").match(/<section\b[^>]*id="company"[\s\S]*?<\/section>/)?.[0] || "";
  const sectionText = visible(section);
  for (const fact of [
    /AVAILABLE/,
    /wave-nano-350m-instruct/,
    /v1\.0/,
    /350M/,
    /instruct/,
    /GGUF/,
    /Q4_K_M/,
    /Artifact license: Apache-2\.0/i,
  ]) assert.match(sectionText, fact);
  assert.match(section, /href="https:\/\/huggingface\.co\/rogerai-fyi\/wave-nano-350m-instruct"/);
  assert.match(section, />Download or Run Wave</);
  assert.match(section, /href="\/research\.html"/);
  assert.match(sectionText, /local use does not require RogerAI or its broker/i);
  assert.match(sectionText, /separate network terms/i);
  assert.doesNotMatch(sectionText, /intended|pending final legal confirmation/i);
  assert.match(sectionText, /network services/i);
});

test("the Company preview carries factual origin and component-specific openness", () => {
  const section = visible(read("index.html").match(/<section\b[^>]*id="company"[\s\S]*?<\/section>/)?.[0] || "");
  assert.match(section, /Orange County, California/);
  assert.match(section, /independently owned/i);
  assert.match(section, /not venture funded/i);
  assert.match(section, /open model and runtime work/i);
  assert.match(section, /PolyForm Perimeter/i);
});

test("homepage Tube Ping is the canonical founder-approved pixel silhouette", () => {
  const home = read("index.html");
  const mascot = home.match(/<figure class="tube-ping"[\s\S]*?<\/figure>/)?.[0] || "";
  assert.match(mascot, /aria-label="Tube Ping, RogerAI Labs mascot"/);
  assert.equal((mascot.match(/class="tube-ping__eye"/g) || []).length, 1, "one saturated eye");
  const plain = mascot
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/<[^>]+>/g, "")
    .replace(/&nbsp;/g, " ");
  for (const row of ["   ▄███████▄", "(  █   •   █▓  )", "   █  ROG  █▓", "    ▀█▄▄▄█▀▒", "     ▀   ▀"]) {
    assert.ok(plain.includes(row), `canonical row is preserved: ${row}`);
  }

  const go = readFileSync(path.join(ROOT, "internal/tui/tube_ping.go"), "utf8");
  for (const row of ["   ▄███████▄", "(  █   •   █▓  )", "   █  ROG  █▓", "    ▀█▄▄▄█▀▒", "     ▀   ▀"]) {
    assert.ok(go.includes(JSON.stringify(row)), `web row is pinned to TUI canonical source: ${row}`);
  }
});

test("the Labs card keeps its model ID inside narrow layouts", () => {
  const css = read("styles/home.css");
  assert.match(css, /\.company__card code\s*\{[^}]*overflow-wrap:\s*anywhere/);
});
