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
  // Ask the BAR, not a non-greedy slice of it: the sections group now nests a
  // disclosure panel, and matching to the first </div> stops inside that panel -
  // silently excluding every item after it, Company included.
  const barLinks = [...bar.matchAll(/<a\b[^>]*class="nav__link[^"]*"[^>]*href="([^"]*)"/g)].map((m) => m[1]);
  assert.ok(barLinks.includes("/company.html"), "top bar links to the company page");

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
  // The page states its ownership character in its own words. "founder-led" replaced
  // "independently owned and not venture funded" - see the note in the homepage test.
  assert.match(page, /founder[- ]led/i);
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
  // the positioning line (founder, 2026-07-31): the network is the frontier,
  // decentralized - a claim about where the models run, not about being a frontier lab.
  assert.match(page, /the frontier, decentralized/i);
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
  // No Wave checkpoint is public, so the page must not imply one. The previous
  // version of this test asserted the OPPOSITE - it required the release copy and
  // forbade the sentence "No Wave checkpoint has been released" - which is how a
  // fabricated release survived a green suite.
  assert.doesNotMatch(page, /wave-micro-350m-instruct/i, "no artifact id for an unpublished model");
  assert.doesNotMatch(page, /released (?:model|checkpoint)/i);
  assert.doesNotMatch(page, /Wave (?:Micro|Nano|Core).{0,30}available/i);
  assert.match(page, /No Wave checkpoint has been released/i, "the page states the truth plainly");
  // optimization of an upstream is never described as RogerAI pretraining
  assert.doesNotMatch(page, /we (pre)?trained (DeepSeek|Kimi)/i);
});

test("the company page presents an honest model-size family", () => {
  const page = visible(readDist("company.html"));
  for (const slot of [
    /Roger Edge/i,
    /Wave Nano/i,
    /Wave Micro/i,
    /frontier-scale optimization/i,
  ]) assert.match(page, slot);
  assert.match(page, /trained ~350M/i);
  assert.match(page, /1(?:&ndash;|\u2013|-)8B class/i);
  assert.match(page, /tens or hundreds of billions/i);
  assert.match(page, /right-sized|smallest model/i);
  // The family describes PROGRAMS; none of its slots may advertise an artifact id.
  // Founder ruling 2026-07-31: the release-gate explanation came off this page (noise
  // reduction); program status lives on the models page. What must survive here is
  // that no slot advertises an artifact.
  assert.doesNotMatch(page, /huggingface\.co\/rogerai-fyi\/wave-/i, "no artifact advertised");
});

test("American-made and openness claims are component-specific", () => {
  const page = visible(readDist("company.html"));
  assert.match(page, /built in Orange County/i);
  assert.match(page, /American-made/i);
  assert.match(page, /upstream models? (?:and|,).*global|global research community/i);
  assert.match(page, /open-source model and runtime work/i);
  assert.match(page, /PolyForm Perimeter/i);
  // The carve-out is named with its actual scope (LICENSING.md): the protocol and
  // receipt SDK are Apache 2.0; the platform itself is not called open source.
  assert.match(page, /protocol and\s+usage-receipt SDK are Apache 2\.0/i);
  // Licence is described as forward-looking until an artifact exists to carry one.
  assert.match(page, /Artifact licence/i);
  assert.doesNotMatch(page, /Artifact license: Apache-2\.0/i, "no definite licence for an unreleased artifact");
  assert.match(page, /network services/i);
  assert.match(page, /separate network terms/i);
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
