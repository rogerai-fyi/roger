// Executable spec for the three surfaces added after the Edge Impulse teardown:
// a pricing page, an FAQ, and a build-generated llms.txt.
//
// The bar these have to clear is the site's existing data-honesty doctrine
// (see homepage-data-honesty.test.mjs): the RULES of the money are stable and
// may be printed; the PRICES are set by operators, change hourly, and may only
// be pointed at. A page that quotes a station's price is a page that lies the
// moment that station retunes.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const compact = (s) => s.replace(/\s+/g, " ");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

/* ===================================================================
   PRICING
   =================================================================== */

test("pricing: the page builds, is indexable, and is in the sitemap", () => {
  const html = read("pricing.html");
  assert.doesNotMatch(html, /name=["']robots["'][^>]*noindex/i);
  assert.match(read("sitemap.xml"), /<loc>https:\/\/rogerai\.fm\/pricing\.html<\/loc>/);
});

test("pricing: it prints the money RULES, every one of which is committed elsewhere", () => {
  const html = compact(read("pricing.html"));
  // the split, as stated in tos.html and the manual
  assert.match(html, /70 ?(?:\/|&#x2F;) ?30|keep 70%/i, "the 70/30 split");
  // payout policy, as stated in manual.html §7
  assert.match(html, /120[- ]day/i, "the 120-day hold");
  assert.match(html, /\$25/, "the $25 minimum payout");
  assert.match(html, /Stripe Connect/, "the KYC gate");
  // wallet side, as stated in manual.html §6
  assert.match(html, /\$1 starter credit/i, "the starter credit");
  assert.match(html, /no subscription|never a subscription/i, "no subscription");
  // the price-safety ceilings, as stated in manual.html §6
  assert.match(html, /\$10 ?(?:\/|&#x2F;) ?1M/, "the default $10/1M out ceiling");
  assert.match(html, /\$20 ?(?:\/|&#x2F;) ?1M/, "the type-the-price threshold");
  assert.match(html, /\$100 ?(?:\/|&#x2F;) ?1M/, "the operator listing ceiling");
  // fair billing, as stated in manual.html §6
  assert.match(html, /voided|no output = \$0/i, "an unusable answer is free");
});

test("pricing: it never quotes a price a station could change under it", () => {
  const html = read("pricing.html");
  // No model id may appear anywhere on this page. A model id is only ever
  // written next to a number, and that number is live data that belongs on the
  // dial, not baked into a marketing page.
  for (const id of [/qwen/i, /llama/i, /gpt-oss/i, /mixtral/i, /gemma/i, /mistral/i, /deepseek/i]) {
    assert.doesNotMatch(html, id, `pricing.html names ${id} and therefore implies a rate`);
  }
  // and it must send the reader to the live source instead
  assert.match(html, /href="\/models\.html"/, "it points at the dial");
  assert.match(compact(html), /set by (?:the )?operators?/i, "it says who sets the number");
});

test("pricing: the free path is stated before the paid one", () => {
  const html = read("pricing.html");
  const free = html.search(/free/i);
  const topup = html.search(/top ?-?up/i);
  assert.ok(free > -1 && topup > -1, "both paths are on the page");
  assert.ok(free < topup, "the free path is introduced before the wallet");
  assert.match(compact(html), /no account/i, "browsing without an account is stated");
});

/* ===================================================================
   FAQ
   =================================================================== */

const faqQuestions = (html) =>
  [...html.matchAll(/<h3 class="faq__q"[^>]*>([\s\S]*?)<\/h3>/g)]
    .map((m) => compact(m[1].replace(/<[^>]*>/g, "")).trim());

test("faq: the page builds, is indexable, and is in the sitemap", () => {
  const html = read("faq.html");
  assert.doesNotMatch(html, /name=["']robots["'][^>]*noindex/i);
  assert.match(read("sitemap.xml"), /<loc>https:\/\/rogerai\.fm\/faq\.html<\/loc>/);
});

test("faq: it carries FAQPage structured data", () => {
  const html = read("faq.html");
  assert.match(html, /"@type"\s*:\s*"FAQPage"/, "FAQPage JSON-LD");
  assert.match(html, /"@type"\s*:\s*"Question"/);
  assert.match(html, /"acceptedAnswer"/);
});

test("faq: every visible question is in the structured data, and vice versa", () => {
  const html = read("faq.html");
  const visible = faqQuestions(html);
  assert.ok(visible.length >= 15, `at least 15 questions (found ${visible.length})`);

  const block = [...html.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)]
    .map((m) => m[1]).find((b) => /"FAQPage"/.test(b));
  assert.ok(block, "an ld+json block carrying the FAQPage");
  const jsonld = JSON.parse(block);
  const declared = jsonld.mainEntity.map((q) => compact(q.name).trim());

  assert.deepEqual(
    [...declared].sort(),
    [...visible].sort(),
    "the JSON-LD questions and the rendered questions have drifted apart",
  );
  for (const q of jsonld.mainEntity) {
    assert.equal(q["@type"], "Question");
    assert.equal(q.acceptedAnswer["@type"], "Answer");
    assert.ok(q.acceptedAnswer.text.length > 40, `answer to "${q.name}" is a stub`);
  }
});

test("faq: the answers that cost money or trust match the committed policy", () => {
  const html = compact(read("faq.html"));
  assert.match(html, /70%/, "the operator's share");
  assert.match(html, /\$25/, "the payout minimum");
  assert.match(html, /120[- ]day/i, "the hold");
  assert.match(html, /no inbound port|no port|outbound/i, "no port forwarding");
  assert.match(html, /OpenAI-compatible/i, "the drop-in claim");
  assert.match(html, /Stripe/, "who holds the card details");
});

test("faq: the operator-can-read-my-prompt answer is the honest one", () => {
  const html = compact(read("faq.html"));
  // The station runs the model, so on an ordinary station the operator is in a
  // position to read what it serves. Saying otherwise would be the single most
  // damaging sentence on the site. The only place secrecy is claimed is the
  // attested tier.
  assert.match(html, /confidential/i, "it names the confidential tier");
  assert.match(html, /attest/i, "it says what makes that tier different");
  assert.doesNotMatch(html, /operators? cannot see (?:your|any) prompt/i,
    "an unqualified secrecy claim for ordinary stations");
});

test("faq: it sends the reader to the manual rather than restating it", () => {
  const html = read("faq.html");
  const links = [...html.matchAll(/href="\/manual\.html#[\w-]+"/g)];
  assert.ok(links.length >= 5, `at least 5 deep links into the manual (found ${links.length})`);
});

/* ===================================================================
   LLMS.TXT
   =================================================================== */

test("llms: the file is generated and follows the llmstxt convention", () => {
  const txt = read("llms.txt");
  assert.match(txt, /^# RogerAI\n/, "an H1 with the site name");
  assert.match(txt, /^> /m, "a blockquote summary");
  assert.match(txt, /^## /m, "at least one section");
});

test("llms: it lists exactly the pages the sitemap lists", () => {
  const txt = read("llms.txt");
  const sitemap = [...read("sitemap.xml").matchAll(/<loc>([^<]+)<\/loc>/g)].map((m) => m[1]);
  for (const url of sitemap) {
    assert.ok(txt.includes(`(${url})`), `llms.txt is missing ${url}`);
  }
  const listed = [...txt.matchAll(/\((https:\/\/rogerai\.fm[^)]*)\)/g)].map((m) => m[1]);
  for (const url of listed) {
    assert.ok(sitemap.includes(url), `llms.txt lists ${url}, which is not indexable`);
  }
});

test("llms: every entry carries the page's own description, not a placeholder", () => {
  const txt = read("llms.txt");
  const entries = [...txt.matchAll(/^- \[([^\]]+)\]\((https:\/\/[^)]+)\): (.+)$/gm)];
  assert.ok(entries.length >= 20, `a line per page (found ${entries.length})`);
  for (const [, title, , desc] of entries) {
    assert.ok(title.trim().length > 0, "a title");
    assert.ok(desc.trim().length > 20, `a real description for ${title}`);
    assert.doesNotMatch(desc, /TODO|lorem|placeholder/i);
  }
});

test("llms: robots.txt points crawlers at it", () => {
  assert.match(src("robots.txt"), /llms\.txt/i);
});

/* ===================================================================
   WIRING
   =================================================================== */

test("wiring: both pages are reachable from the shared footer", () => {
  const footer = src("_partials/footer.html");
  assert.match(footer, /href="\/pricing\.html"/, "footer links pricing");
  assert.match(footer, /href="\/faq\.html"/, "footer links the FAQ");
});

test("wiring: pricing is in the nav, and the FAQ is reachable from pricing", () => {
  assert.match(src("_partials/nav.html"), /href="\/pricing\.html"/, "nav links pricing");
  assert.match(read("pricing.html"), /href="\/faq\.html"/, "pricing links the FAQ");
});
