// Executable behavior lock for the dedicated company page scenarios in
// features/web/company_positioning.feature.
//
// The homepage Company SECTION (company-positioning.test.mjs) answers "is there a
// real product here". This file locks the separate question a grant, enterprise, or
// partnership reviewer asks: "is there a real COMPANY here" - a durable destination
// that states what kind of company RogerAI is, the two lines of work, the markets it
// serves, how it operates, and how to reach it, without inventing traction.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const readDist = (p) => readFileSync(path.join(DIST, p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// The page body without HTML comments - explanatory comments must never satisfy an
// assertion about what a VISITOR can read.
const visible = (html) => html.replace(/<!--[\s\S]*?-->/g, "");

test("Company is a first-class destination in the marketing nav", () => {
  const home = visible(readDist("index.html"));
  const bar = home.match(/<header class="nav[\s\S]*?<\/header>/)[0];
  const sections = bar.match(/<div class="nav__sections">[\s\S]*?<\/div>/)[0];
  const hrefs = [...sections.matchAll(/<a\b[^>]*href="([^"]*)"/g)].map((m) => m[1]);
  assert.ok(hrefs.includes("/company.html"), "top bar links to the company page");

  // and the homepage Company section hands off to the full page
  const section = home.match(/<section\b[^>]*id="company"[\s\S]*?<\/section>/)[0];
  assert.match(section, /href="\/company\.html"/, "Company section links to the company page");
});

test("the company page states what kind of company this is", () => {
  const page = visible(readDist("company.html"));
  assert.match(page, /American/, "identifies as American");
  assert.match(page, /research/i);
  assert.match(page, /infrastructure/i);
  assert.match(page, /Orange County, California/);
  assert.match(page, /independently owned/i);
  assert.match(page, /not venture[- ]funded|no venture funding|without venture/i);
});

test("the company page invents no traction", () => {
  const page = visible(readDist("company.html"));
  // no fabricated scale, funding, or logos
  assert.doesNotMatch(page, /\b\d+\+? (employees|engineers on staff)\b/i);
  assert.doesNotMatch(page, /Series [A-D]\b|seed round|raised \$/i);
  assert.doesNotMatch(page, /trusted by|our customers include|as used by/i);
});

test("the company page separates the product line from the research line", () => {
  const page = visible(readDist("company.html"));
  assert.match(page, /RogerAI Labs/);
  for (const href of ["/models.html", "/research.html", "/manual.html"]) {
    assert.match(page, new RegExp(`href="${href.replace(".", "\\.")}"`), `links ${href}`);
  }
  // the independence promise: models are usable without the network
  assert.match(page, /never requires|does not require|without the network/i);
});

test("the company page names the focus markets and the OT constraint", () => {
  const page = visible(readDist("company.html"));
  for (const market of [/oil and gas/i, /power generation/i, /manufacturing/i, /aerospace/i]) {
    assert.match(page, market, `names ${market}`);
  }
  assert.match(page, /embedded/i);
  assert.match(page, /edge/i);
  // the reason local inference is required, not merely preferred
  assert.match(page, /operational technology|OT network|air[- ]gapped/i);
  assert.match(page, /href="\/research\.html#industry"/, "links to the industry detail");
});

test("the company page publishes operating principles", () => {
  const page = visible(readDist("company.html"));
  assert.match(page, /harness/i, "evaluation harness named");
  assert.match(page, /hardware/i);
  assert.match(page, /raw results|raw evaluations/i);
  assert.match(page, /open formats?/i);
  assert.match(page, /telemetry/i);
  assert.match(page, /negative (and superseded )?results/i);
});

test("the company page routes real enquiries and names the legal entity source", () => {
  const page = visible(readDist("company.html"));
  assert.match(page, /mailto:labs@rogerai\.fm/, "pilots and engagements route");
  assert.match(page, /mailto:security@rogerai\.fm/, "security disclosure route");
  // Every contact on this page must be a mailbox that already routes. We do not
  // ship a press@ / hello@ address before the alias exists - a dead contact on the
  // credibility page is worse than one fewer contact.
  const boxes = [...page.matchAll(/mailto:([a-z]+)@rogerai\.fm/g)].map((m) => m[1]);
  const ROUTES = ["abuse", "billing", "confidential", "labs", "legal", "privacy", "security"];
  for (const b of boxes) assert.ok(ROUTES.includes(b), `mailto:${b}@ is a mailbox that exists`);
  assert.match(page, /href="\/tos\.html"/, "terms name the governing entity");
});

test("the company page makes no unearned model claims", () => {
  const page = visible(readDist("company.html"));
  // Wave has no released checkpoint; the page must not imply one
  assert.doesNotMatch(page, /download Wave|Wave .{0,20}available now/i);
  assert.match(page, /no released Wave checkpoint/i);
  // optimization of an upstream is never described as RogerAI pretraining
  assert.doesNotMatch(page, /we (pre)?trained (DeepSeek|Kimi)/i);
});

test("the company page presents an honest model-size ladder", () => {
  const page = visible(readDist("company.html"));
  for (const rung of [
    /Roger Edge/i,
    /Wave Micro/i,
    /Wave Nano/i,
    /frontier-scale optimization/i,
  ]) assert.match(page, rung);
  assert.match(page, /sub-100M/i);
  assert.match(page, /350M-class/i);
  assert.match(page, /tens or hundreds of billions/i);
  assert.match(page, /right-sized|smallest model/i);
  assert.match(page, /no released Wave checkpoint/i);
});

test("American-made and openness claims are component-specific", () => {
  const page = visible(readDist("company.html"));
  assert.match(page, /built in Orange County/i);
  assert.match(page, /American-made/i);
  assert.match(page, /upstream models? (?:and|,).*global|global research community/i);
  assert.match(page, /open-source model and runtime work/i);
  assert.match(page, /PolyForm Perimeter/i);
  assert.doesNotMatch(page, /open-source (?:network|broker)/i);
});

test("the company page is static: content survives with no client JavaScript", () => {
  const page = readDist("company.html");
  const body = page.slice(page.indexOf("<main"), page.indexOf("</main>"));
  const withoutScripts = body.replace(/<script[\s\S]*?<\/script>/g, "");
  assert.match(withoutScripts, /American/, "core positioning is server-rendered");
  assert.match(withoutScripts, /oil and gas/i, "markets are server-rendered");
});

test("the company page is indexable and in the sitemap", () => {
  const page = readDist("company.html");
  assert.doesNotMatch(page, /<meta[^>]+name=["']robots["'][^>]*noindex/i);
  assert.match(readDist("sitemap.xml"), /company\.html/);
});
