// Broadcast 012 ("Run an LLM on your iPhone") is the SEO/AEO field guide for the iOS
// v1.2 release, and this file locks the parts that rot silently: the AEO scaffolding
// (Quick Answer + FAQPage JSON-LD that must parse and agree with the visible FAQ), the
// App Store link, the real screenshots, and the wiring (bundle, index row, sitemap).
//
// It also locks the brand-disambiguation fix that shipped with it: the site-wide
// Organization JSON-LD carries a sameAs array tying "RogerAI" to our App Store, GitHub,
// X, and Hugging Face properties - the signal engines use to tell rogerai.fm apart
// from lookalike domains. Losing that array would be invisible on the page.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const PAGE = "broadcasts-llm-on-iphone.html";
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

/* ---- wired like every other broadcast ---------------------------------- */

test("iphone: the broadcast builds, is indexable, and is in the sitemap", () => {
  assert.doesNotMatch(read(PAGE), /name=["']robots["'][^>]*noindex/i);
  assert.match(read("sitemap.xml"), new RegExp(`<loc>https://rogerai\\.fm/${PAGE}</loc>`));
});

test("iphone: the transmission log carries it, newest first", () => {
  const log = read("broadcasts.html");
  const first = log.indexOf(`href="/${PAGE}"`);
  const prev = log.indexOf('href="/broadcasts-what-a-million-tokens-costs.html"');
  assert.ok(first > 0 && prev > 0 && first < prev, "012 sits above 011");
});

/* ---- the AEO scaffolding ------------------------------------------------ */

test("iphone: exactly one h1, a Quick Answer up top, and question-based h2s", () => {
  const html = read(PAGE);
  assert.equal([...html.matchAll(/<h1[\s>]/g)].length, 1, "one h1");
  assert.match(html, /<b>Quick answer\.<\/b>/, "the Quick Answer blockquote");
  const h2s = [...html.matchAll(/<h2>([^<]+)<\/h2>/g)].map((m) => m[1]);
  const questions = h2s.filter((t) => t.trim().endsWith("?"));
  assert.ok(questions.length >= 5, `most h2s are questions (got ${questions.length} of ${h2s.length})`);
});

test("iphone: the FAQPage JSON-LD parses and mirrors the visible FAQ", () => {
  const html = read(PAGE);
  const m = html.match(/<script type="application\/ld\+json">(\{"@context":"https:\/\/schema\.org","@type":"FAQPage".*?)<\/script>/s);
  assert.ok(m, "a FAQPage block exists");
  const faq = JSON.parse(m[1]);
  assert.equal(faq["@type"], "FAQPage");
  assert.ok(faq.mainEntity.length >= 6, `at least 6 questions (got ${faq.mainEntity.length})`);
  for (const q of faq.mainEntity) {
    assert.equal(q["@type"], "Question");
    assert.ok(html.includes(`<dt><b>${q.name}</b></dt>`), `visible FAQ carries: ${q.name}`);
  }
  const visible = [...html.matchAll(/<dt><b>/g)].length;
  assert.equal(visible, faq.mainEntity.length, "schema and visible FAQ have the same count");
});

/* ---- the claims a reader acts on ---------------------------------------- */

test("iphone: the App Store link, the install line, and the product pages are linked", () => {
  const html = read(PAGE);
  assert.match(html, /href="https:\/\/apps\.apple\.com\/us\/app\/rogerai-fm\/id6785743752"/);
  assert.match(html, /curl -fsSL https:\/\/rogerai\.fm\/install\.sh \| sh/);
  for (const p of ["/app.html", "/models.html", "/pricing.html"]) {
    assert.match(html, new RegExp(`href="${p}"`), `links ${p}`);
  }
});

test("iphone: the three screenshots and the OG card are real shipped assets", () => {
  const html = read(PAGE);
  for (const a of ["phone-hero.webp", "phone-private.webp", "phone-confidential.webp"]) {
    assert.match(html, new RegExp(`assets/broadcasts/${a}`), `figure ${a}`);
    readFileSync(path.join(WEB, "dist", "assets", "broadcasts", a)); // throws if missing
  }
  assert.match(html, /property="og:image" content="https:\/\/rogerai\.fm\/assets\/broadcasts\/phone-og\.png"/);
  readFileSync(path.join(WEB, "dist", "assets", "broadcasts", "phone-og.png"));
});

// The 2026-09-08 audit caught the first draft claiming only a Mac can earn - the iOS
// ShareView shows the EARN price stepper on iPhone unconditionally (login-gated, like
// private bands), and iPhone serving needs the app open with the screen awake. These
// pin the corrected claims so a rewrite cannot quietly reintroduce the wrong ones.
test("iphone: earning is any-device, never sold as Mac-only", () => {
  const html = read(PAGE);
  assert.doesNotMatch(html, /On a Mac, yes/, "the Mac-only earn answer is gone");
  assert.match(html, /Yes, from any device - the share screen&#39;s price stepper|Yes, from any device - the share screen's price stepper/, "the FAQ says any device earns");
  assert.match(html, /phone included/, "the body says the phone sets a price too");
  const log = read("broadcasts.html");
  assert.doesNotMatch(log, /earning on a Mac/, "the index dek does not repeat the old claim");
});

test("iphone: the login gate and the screen-awake constraint are stated", () => {
  const html = read(PAGE);
  assert.match(html, /one-time login/, "earn/private need a login, said plainly");
  assert.match(html, /screen locked/i, "iOS cannot serve with the screen locked - stated");
  assert.match(html, /broadcast a private band|going private/i, "private bands named in the account answer");
});

test("iphone: no em dashes in the copy", () => {
  assert.doesNotMatch(read(PAGE), /—/, "founder style: spaced hyphens, never em dashes");
});

/* ---- the site-wide brand signal ----------------------------------------- */

test("brand: the Organization JSON-LD carries sameAs for our four properties", () => {
  const m = read("index.html").match(/<script type="application\/ld\+json">(\{"@context":"https:\/\/schema\.org","@graph".*?)<\/script>/s);
  assert.ok(m, "the @graph block exists");
  const org = JSON.parse(m[1])["@graph"].find((n) => n["@type"] === "Organization");
  assert.ok(org, "an Organization node");
  const same = org.sameAs ?? [];
  for (const u of [
    "https://apps.apple.com/us/app/rogerai-fm/id6785743752",
    "https://github.com/rogerai-fyi",
    "https://x.com/RogerAI_fm",
    "https://huggingface.co/rogerai-fyi",
  ]) {
    assert.ok(same.includes(u), `sameAs includes ${u}`);
  }
});
