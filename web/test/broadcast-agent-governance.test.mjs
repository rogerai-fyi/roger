// Broadcast 013 ("1,200 agents broke their sandbox...") is a commentary dispatch on two
// DISTINCT OpenAI disclosures: the July ExploitGym / Hugging Face attack (reported in full
// August 26, alongside METR's independent investigation) and a separate, later incident (an
// unreleased research model's "freed" note) that OpenAI disclosed September 16 as one of six
// new cases. A pre-push audit round caught an earlier draft conflating these into one
// "disclosed both on September 16" timeline - these assertions pin the corrected framing so a
// future edit cannot quietly reintroduce that error, on top of the usual wiring checks every
// broadcast gets (bundle, index row, FAQ schema, shipped assets).
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const PAGE = "broadcasts-agent-governance-identity.html";
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

/* ---- wired like every other broadcast ---------------------------------- */

test("gov-identity: the broadcast builds, is indexable, and is in the sitemap", () => {
  assert.doesNotMatch(read(PAGE), /name=["']robots["'][^>]*noindex/i);
  assert.match(read("sitemap.xml"), new RegExp(`<loc>https://rogerai\\.fm/${PAGE}</loc>`));
});

test("gov-identity: the transmission log carries it, newest first, as 013", () => {
  const log = read("broadcasts.html");
  const first = log.indexOf(`href="/${PAGE}"`);
  const prev = log.indexOf('href="/broadcasts-llm-on-iphone.html"');
  assert.ok(first > 0 && prev > 0 && first < prev, "013 sits above 012");
  assert.match(log, /BROADCAST 013/);
});

test("gov-identity: the FAQPage JSON-LD parses and mirrors the visible FAQ", () => {
  const html = read(PAGE);
  const m = html.match(/<script type="application\/ld\+json">(\{"@context":"https:\/\/schema\.org","@type":"FAQPage".*?)<\/script>/s);
  assert.ok(m, "a FAQPage block exists");
  const faq = JSON.parse(m[1]);
  assert.equal(faq["@type"], "FAQPage");
  assert.ok(faq.mainEntity.length >= 6, `at least 6 questions (got ${faq.mainEntity.length})`);
  // JSON-LD text is plain (straight apostrophes); the visible <dt> uses the
  // typographic &rsquo; entity for the same word - normalize both before comparing.
  const norm = (s) => s.replace(/&rsquo;|['’]/g, "'");
  const visibleHtml = norm(html);
  for (const q of faq.mainEntity) {
    assert.equal(q["@type"], "Question");
    assert.ok(visibleHtml.includes(`<dt><b>${norm(q.name)}</b></dt>`), `visible FAQ carries: ${q.name}`);
  }
  const visible = [...html.matchAll(/<dt><b>/g)].length;
  assert.equal(visible, faq.mainEntity.length, "schema and visible FAQ have the same count");
});

test("gov-identity: the hero and OG images are real shipped assets", () => {
  const html = read(PAGE);
  assert.match(html, /assets\/broadcasts\/agent-governance-hero\.png/);
  readFileSync(path.join(WEB, "dist", "assets", "broadcasts", "agent-governance-hero.png"));
  assert.match(html, /property="og:image" content="https:\/\/rogerai\.fm\/assets\/broadcasts\/agent-governance-og\.png"/);
  readFileSync(path.join(WEB, "dist", "assets", "broadcasts", "agent-governance-og.png"));
});

/* ---- the two incidents stay distinct (the audit-caught conflation) ----- */

test("gov-identity: the Hugging Face attack is dated to July and its report to August 26, never bundled into a September 16 disclosure", () => {
  const html = read(PAGE);
  assert.doesNotMatch(html, /disclosed (the incident|both incidents).{0,40}September ?16/is,
    "the HF attack must never be framed as disclosed on September 16");
  assert.match(html, /August 26, 2026/, "the real HF technical-report + METR date is stated");
  assert.match(html, /acknowledged its agents were responsible/i, "the FAQ states OpenAI's own acknowledgment, not a September reveal");
});

test("gov-identity: the timeline covers exactly the four Hugging Face dates, July 8-13", () => {
  const html = read(PAGE);
  const timeline = html.match(/<ol class="gov-timeline"[\s\S]*?<\/ol>/)?.[0];
  assert.ok(timeline, "the timeline list exists");
  const dates = [...timeline.matchAll(/datetime="2026-07-(\d\d)"/g)].map((m) => m[1]);
  assert.deepEqual(dates, ["08", "10", "11", "13"], "four HF-attack dates only, no unrelated incident folded in");
});

test("gov-identity: the freed-note incident is explicitly framed as separate and disclosed September 16, with no fabricated date attached to it", () => {
  const html = read(PAGE);
  assert.match(html, /freed from the roles and identities/);
  assert.match(html, /disclosed September 16, 2026/, "the freed note's own citation carries the real disclosure date");
  assert.doesNotMatch(html, /freed from the roles[\s\S]{0,400}July 18/, "no fabricated July 18 training date near the quote");
  assert.match(html, /separate incident|unrelated,\s*unreleased research model/i, "prose marks it as a distinct incident, not part of the HF timeline");
});

test("gov-identity: the leaked credentials are sourced as publicly exposed, not found by poking at eval infrastructure", () => {
  const html = read(PAGE);
  assert.doesNotMatch(html, /poking at the eval/i);
  assert.match(html, /exposed on the public internet/i);
});

/* ---- Governance Identity stays labeled experimental, never a shipped claim ---- */

test("gov-identity: Governance Identity is never presented as an existing, shipping product", () => {
  const html = read(PAGE);
  assert.match(html, /experimental/i);
  assert.match(html, /not (a product we are shipping today|yet)/i);
  assert.doesNotMatch(html, /Governance Identity is (now )?available/i);
});

test("gov-identity: the reused Ed25519 receipt claim matches security.html verbatim", () => {
  const gov = read(PAGE);
  const security = read("security.html");
  assert.match(gov, /Ed25519-signed lineage receipt/);
  assert.match(security, /Ed25519-signed lineage receipt/);
});

test("gov-identity: no em dashes in the copy", () => {
  assert.doesNotMatch(read(PAGE), /—/, "founder style: spaced hyphens, never em dashes");
});
