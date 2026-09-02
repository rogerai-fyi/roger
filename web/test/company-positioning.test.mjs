// Executable behavior lock for features/web/company_positioning.feature and
// the restart handoff's homepage positioning requirement:
// RogerAI must read as an active AI company with a concrete product, audience,
// evaluation path, and a connected Labs/model story.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const compact = (value) => value.replace(/\s+/g, " ");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

test("homepage leads with a concrete product promise", () => {
  const home = readFileSync(path.join(DIST, "index.html"), "utf8");
  const hero = home.match(/<section class="hero">[\s\S]*?<\/section>/)?.[0];
  assert.ok(hero, "homepage has a hero");
  assert.match(hero, /OpenAI-compatible/i);
  assert.match(hero, /local endpoint/i);
  assert.match(compact(hero), /community and private hardware/i);
  assert.doesNotMatch(hero, /ham radio/i);
  assert.doesNotMatch(home, /pays rent while you sleep|Pick up the mic/i);
});

test("homepage masthead joins the company identity to the product promise", () => {
  const home = readFileSync(path.join(DIST, "index.html"), "utf8");
  const hero = home.match(/<section class="hero">[\s\S]*?<\/section>/)?.[0] || "";
  assert.match(hero, /American AI research \+ infrastructure/i);
  assert.match(compact(hero), /one OpenAI-compatible local endpoint/i);
  assert.match(hero, /open model research/i);
  assert.match(hero, /routing/i);
  assert.match(hero, /failover/i);
  assert.match(hero, /metering/i);
  assert.match(hero, /signed receipts/i);
  assert.ok(hero.indexOf('class="install"') > hero.indexOf("<h1"), "install remains in the hero");
});

test("a compact institutional strip separates Network, Labs, and evidence", () => {
  const home = readFileSync(path.join(DIST, "index.html"), "utf8");
  const strip = home.match(/<aside class="institution-strip"[\s\S]*?<\/aside>/)?.[0] || "";
  assert.match(strip, /RogerAI Network/);
  assert.match(strip, /RogerAI Labs/);
  assert.match(strip, /Open Air Waves/);
  assert.match(strip, /href="\/company\.html"/);
  assert.match(strip, /href="\/research\.html"/);
  assert.match(strip, /href="\/broadcasts\.html"/);
  assert.equal((strip.match(/<a\b/g) || []).length, 3, "one focused link per surface");
});

test("Company and Labs appear before technical detail and monetization", () => {
  const home = readFileSync(path.join(DIST, "index.html"), "utf8");
  const at = (id) => home.indexOf(`id="${id}"`);
  assert.ok(at("demo") > 0 && at("market") > at("demo"), "product proof leads");
  assert.ok(at("company") > at("market"), "company follows product and live proof");
  assert.ok(at("company") < at("what"), "company precedes network detail");
  assert.ok(at("company") < at("spec"), "company precedes specifications");
  assert.ok(at("company") < at("monetize"), "company precedes monetization");
  for (const marker of [
    "§1 / OPERATING PROCEDURE",
    "§2 / THE BAND",
    "§3 / COMPANY",
    "§4 / THE TUNE",
    "§5 / SPECIFICATIONS",
    "§6 / THE INDUSTRIAL SERIES",
    "§7 / MONETIZE",
    "§8 / THE OPERATOR",
    "§9 / THE TOWER",
    "§10 / YOU COULD GO DIRECT",
    "§11 / GO",
  ]) assert.match(home, new RegExp(marker.replace("/", "\\/")));
  const main = home.match(/<main\b[\s\S]*?<\/main>/)?.[0] || "";
  for (const id of ["demo", "market", "company", "what", "spec", "monetize"]) {
    assert.match(main, new RegExp(`id="${id}"`), `${id} remains in the main landmark`);
  }
  assert.match(main, /class="cta"/, "the closing CTA remains in the main landmark");
});

test("homepage connects company, product, audience, evaluation, and research", () => {
  const home = readFileSync(path.join(DIST, "index.html"), "utf8");
  const section = home.match(/<section\b[^>]*id="company"[\s\S]*?<\/section>/)?.[0];
  assert.ok(section, "homepage has a Company section");

  assert.match(section, /RogerAI is an AI company/i);
  assert.match(section, /developers|teams/i);
  assert.match(section, /OpenAI-compatible endpoint/i);
  assert.match(section, /evaluate|install|browse/i);

  for (const href of ["/models.html", "/manual.html", "/research.html"]) {
    assert.match(section, new RegExp(`href="${href.replace(".", "\\.")}"`));
  }
  assert.match(section, /href="\/company\.html"/);
  assert.match(section, /RogerAI Labs/);
  assert.match(section, /Open Air Waves/);
  assert.match(section, /Wave Pico|Wave Spectrum/i);  // SPECTRUM RENAME 2026-08-14
  assert.match(section, /edge|manufacturing|industrial|personal/i);
});

test("Company remains directly reachable from the full footer map", () => {
  const home = readFileSync(path.join(DIST, "index.html"), "utf8");
  const footer = home.match(/<footer[\s\S]*?<\/footer>/)?.[0];
  assert.ok(footer, "homepage has a footer");
  // The footer now targets the dedicated page rather than the homepage anchor: the
  // section summarises, the page is the destination. Reachability is unchanged.
  assert.match(footer, /href="\/company\.html"[^>]*>Company<\/a>/);
});
