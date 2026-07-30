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

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

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
  assert.match(section, /Wave models/i);
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
