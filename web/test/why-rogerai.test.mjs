// Executable lock for features/web/why_rogerai.feature.
//
// MERGE RESPEC (founder, 2026-09-02): the why is no longer a standalone page - the
// argument now opens pricing.html as §0 (anchor #why), and why.html survives only as
// a noindex pointer stub. The argument is for a FEE, which makes it the copy most
// likely to drift into a promise, so the locks stay honesty-shaped: the anonymization
// claim must carry its own limit (the words still reach the model's operator), the
// comparison must not hide the self-host exit, and no surface may claim prompt
// privacy on curated bands - identity unlinking is the claim the architecture
// enforces, and the only one.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const visible = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<style[\s\S]*?<\/style>/g, " ")
  .replace(/<script[\s\S]*?<\/script>/g, " ").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const pricing = () => read("pricing.html");
// The why argument: §0 of the pricing page, from its anchor to the next section.
const whySection = () => {
  const m = pricing().match(/<section[^>]*id="why"[\s\S]*?<\/section>/);
  assert.ok(m, 'pricing carries <section id="why">');
  return m[0];
};

test("the argument opens the pricing page, before any number", () => {
  const p = pricing();
  const why = p.indexOf('id="why"');
  const paths = p.indexOf('id="paths"');
  assert.ok(why > -1 && paths > -1 && why < paths,
    "§0 WHAT THE FEE BUYS sits above the earn paths");
  assert.match(whySection(), /WHAT THE FEE BUYS/, "the section is named for its job");
});

test("why.html survives as a pointer stub, out of the index", () => {
  assert.ok(existsSync(path.join(DIST, "why.html")), "old links still land");
  const stub = read("why.html");
  assert.match(stub, /href="\/pricing\.html#why"/, "and are pointed at pricing");
  assert.match(stub, /noindex/, "the stub is not indexed");
  assert.doesNotMatch(read("sitemap.xml"), /why\.html/, "and left out of the sitemap");
});

test("anonymization is claimed exactly as far as the code enforces it", () => {
  const c = visible(whySection());
  assert.match(c, /sees a station/i, "the unlinking claim is stated");
  assert.match(c, /not your name, not your card, not your IP/i, "and itemized");
  assert.match(c, /the words themselves still reach/i,
    "the limit is stated in the same breath: content reaches the model's operator");
  // "private"/"anonymous" may only describe identity and self-hosted planes, never
  // prompt content on curated bands - the overselling phrase must not appear anywhere
  // on the page that argues for the fee.
  assert.doesNotMatch(visible(pricing()), /prompts? (are|stay|remain) (private|anonymous)/i,
    "no prompt-privacy claim the architecture does not enforce");
});

test("the comparison keeps humans and curated apart and names the fee's job", () => {
  const c = visible(whySection());
  assert.match(c, /never blurred together/i, "curated supply is presented apart from human supply");
  assert.match(c, /counter-signed receipt/i, "the receipt claim is the enforceable one");
  assert.match(visible(pricing()), /rides every request/i, "price protection stated as mechanism");
  assert.match(c, /go(ing)? direct/i, "the honest close: when to skip us is on the page");
});

test("self-hosting is a first-class exit", () => {
  const c = visible(whySection());
  assert.match(c, /run your own Tower/i, "the tower exit is in the comparison");
  assert.match(c, /free/i, "and it says the local plane is free");
  assert.match(pricing(), /href="\/broadcasts-run-a-tower\.html"/, "and the quickstart is linked");
});

// --- features/web/network_story.feature locks -----------------------------------

test("the why argument is one click from the homepage, and the old page is unlinked", () => {
  assert.match(read("index.html"), /href="\/pricing\.html#why"/, "the homepage links the anchor");
  // nav + footer are partials baked into every marketing page; models.html carries both
  const m = read("models.html");
  assert.doesNotMatch(m, /href="\/why\.html"/, "nothing links the retired page");
  assert.match(m, /href="\/pricing\.html"/, "the nav still carries pricing itself");
});

test("the homepage tells the story (network_story.feature)", () => {
  const home = read("index.html");
  assert.match(home, /sees a station -?\s*(never|not) you/i, "the unlinking line");
  assert.match(home, /Two kinds of transmitter/i, "both earn paths framed as one verb");
  assert.match(home, /curated station/i, "the resale path is named");
  assert.match(home, /keeps <b>5%<\/b> of everything it\s+carries/i, "the tower relay earn");
  assert.match(home, /standalone/i, "the free standalone exit");
  assert.match(home, /cheaper direct|go direct/i, "the honest concession");
  assert.match(home, /PRIVACY-FIRST AIRWAVES/, "the merged privacy section carries the tease");
});

test("the FAQ answers resale and provider-anonymity (network_story.feature)", () => {
  const faq = read("faq.html");
  assert.match(faq, /Can I resell my provider contract as a station\?/, "the resale question");
  assert.match(faq, /Does a model provider know who I am\?/, "the anonymity question");
  // REGRESSION 2026-09-02: the resale item was appended AFTER its section's list +
  // wrap divs had closed, so it rendered full-bleed left of the FAQ column. Pin it
  // as the last item INSIDE both containers of its section.
  assert.match(faq,
    /Can I resell my provider contract as a station\?[\s\S]*?<\/div>\s*<\/div>\s*<\/div>\s*<\/div>\s*<\/section>/,
    "the resale item closes inside its list and wrap, not after them");
});
