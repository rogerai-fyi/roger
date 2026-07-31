// Executable behavior lock for the search/social/schema slice in
// features/web/homepage_company_research_branding.feature.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

function pngSize(file) {
  const b = readFileSync(file);
  assert.equal(b.toString("ascii", 1, 4), "PNG", `${file} is a PNG`);
  return [b.readUInt32BE(16), b.readUInt32BE(20)];
}

test("homepage search metadata names the company, models, and infrastructure", () => {
  const page = read("index.html");
  assert.match(page, /<title>[^<]*local AI models[^<]*inference infrastructure[^<]*<\/title>/i);
  const desc = page.match(/<meta name="description" content="([^"]+)"/)?.[1] || "";
  assert.match(desc, /American AI research and infrastructure company/i);
  assert.match(desc, /models for constrained hardware/i);
  assert.match(desc, /OpenAI-compatible network/i);
  // ALL THREE description tags, not just <meta name="description">. The first pass
  // of this retraction fixed only that one and left og:/twitter: still naming Wave
  // Micro as a product - the social cards are what actually get shared and cached.
  const descriptions = [
    ...[...page.matchAll(/<meta name="description" content="([^"]+)"/g)].map((m) => m[1]),
    ...[...page.matchAll(/<meta property="og:description" content="([^"]+)"/g)].map((m) => m[1]),
    ...[...page.matchAll(/<meta name="twitter:description" content="([^"]+)"/g)].map((m) => m[1]),
  ];
  assert.ok(descriptions.length >= 3, "all three description tags are present");
  for (const d of descriptions) {
    assert.doesNotMatch(d, /Wave (?:Micro|Nano|Core)/i, `description names no unreleased model: ${d}`);
  }
});

test("homepage, Company, and Research use distinct intentional raster social cards", () => {
  const expected = new Map([
    ["index.html", "og-home.png"],
    ["company.html", "og-company.png"],
    ["research.html", "og-research.png"],
  ]);
  const seen = new Set();
  for (const [pageName, image] of expected) {
    const page = read(pageName);
    assert.match(page, new RegExp(`property="og:image" content="https://rogerai\\.fm/${image}"`));
    assert.match(page, new RegExp(`name="twitter:image" content="https://rogerai\\.fm/${image}"`));
    assert.match(page, /name="twitter:image:alt" content="[^"]{12,}"/);
    const file = path.join(DIST, image);
    assert.ok(existsSync(file), `${image} exists in the built site`);
    assert.deepEqual(pngSize(file), [1200, 630], `${image} declares the standard social-card size`);
    seen.add(image);
  }
  assert.equal(seen.size, 3);
});

test("Company search metadata is specific and compact", () => {
  const page = read("company.html");
  assert.match(page, /<title>RogerAI Company - American AI research and infrastructure<\/title>/);
  const desc = page.match(/<meta name="description" content="([^"]+)"/)?.[1] || "";
  assert.ok(desc.length >= 100 && desc.length <= 160, `description is 100-160 characters (${desc.length})`);
  assert.match(desc, /Orange County, California/i);
  assert.match(desc, /models/i);
  assert.match(desc, /inference infrastructure/i);
});

test("homepage structured data connects RogerAI, Labs, pages, and the released artifact", () => {
  const page = read("index.html");
  const blocks = [...page.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)]
    .map((m) => JSON.parse(m[1]));
  const serialized = JSON.stringify(blocks);
  assert.match(serialized, /https:\/\/rogerai\.fm\/#org/);
  assert.match(serialized, /https:\/\/rogerai\.fm\/company\.html/);
  assert.match(serialized, /https:\/\/rogerai\.fm\/research\.html/);
  assert.match(serialized, /ResearchOrganization/);
  assert.match(serialized, /RogerAI Labs/);
  // Structured data is a MACHINE claim that an artifact exists at a URL, and it is
  // cached and indexed long after a page is corrected. Until a stranger can download
  // a Wave checkpoint, no SoftwareSourceCode block may name one.
  assert.doesNotMatch(serialized, /SoftwareSourceCode/);
  // Both the artifact-id form and the spaced product name - the first draft only
  // matched the hyphenated id, so "Wave Micro" itself would have passed.
  assert.doesNotMatch(serialized, /wave-(?:micro|nano|core)-\d+/i);
  assert.doesNotMatch(serialized, /Wave\s+(?:Micro|Nano|Core)/i);
  assert.doesNotMatch(serialized, /Wave Nano|Wave Core|Roger Edge/);
  assert.doesNotMatch(serialized, /employee|customer|funding|award/i);
});
