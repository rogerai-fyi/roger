// Executable lock for features/web/tower.feature.
//
// The tower page makes claims about money and trust, so nearly every assertion here is
// either "this mechanism is described accurately" or "this thing is never claimed". The
// mechanisms come from features/trust/lineage_receipts.feature and
// features/routing/scoring.feature; the page may not get ahead of either.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const visible = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const page = () => read("tower.html");
const copy = () => visible(page().match(/<main[\s\S]*?<\/main>/)[0]);

test("the tower page exists, is reachable, and reconciles its own name", () => {
  assert.ok(existsSync(path.join(DIST, "tower.html")), "tower.html builds");
  assert.match(read("index.html"), /href="\/tower\.html"/, "the nav offers it");
  // An engineer reading the API and a buyer reading the site must know these are one thing.
  assert.match(copy(), /broker/i, "the page says what the code calls it");
  assert.match(read("sitemap.xml"), /tower\.html/, "it is indexed");
});

test("the routing score is described by its actual inputs", () => {
  const c = copy();
  for (const input of [/price/i, /reliab/i, /speed/i]) assert.match(c, input);
  assert.match(c, /divided by|load factor|how loaded|already carrying/i,
    "the score is divided by existing load");
  assert.match(c, /across (both )?(broker )?instances|every instance/i,
    "load is merged across instances, not counted per instance");
  assert.match(c, /power[- ]of[- ]two|two at random/i, "the final pick spreads load");
});

test("the health gate is described as a gate", () => {
  const c = copy();
  assert.match(c, /healthy/i);
  assert.match(c, /probation/i);
  assert.match(c, /only when|unless no|if none/i, "the fallback is conditional, not a preference");
});

test("failover is shown and its billing consequence is stated", () => {
  const c = copy();
  assert.match(c, /drops|fails|goes off air/i);
  assert.match(c, /not charged|no charge|costs (you )?nothing|refund/i,
    "a failed attempt does not cost the caller");
});

test("the receipt is described as co-signed and chained", () => {
  const c = copy();
  assert.match(c, /signs|signed/i);
  assert.match(c, /counter-sign/i, "the broker counter-signs - that is the differentiator");
  assert.match(c, /chain/i, "receipts are hash-chained per station");
  assert.match(c, /returns? (with|to)|rides back|comes back with/i, "the receipt reaches the caller");
});

test("every guarantee that stops a station profiting from a lie is stated", () => {
  const c = copy();
  assert.match(c, /claimed price is (discarded|ignored)|ignores the (station|node)'s price/i);
  assert.match(c, /lower of|min(imum)? of|whichever is (lower|smaller)/i,
    "billing takes the lower of claim and recount");
  assert.match(c, /recount/i, "the broker counts the tokens itself");
  assert.match(c, /cap(ped)? at|never exceeds/i, "cost is capped at the authorised hold");
  assert.match(c, /wrong key|forged|does not verify/i, "an unverifiable receipt settles nothing");
});

test("the failure paths are on the page, because they are the guarantee", () => {
  const c = copy();
  assert.match(c, /no usable output|nothing usable|empty (response|completion)/i);
  assert.match(c, /zero|\$0/i, "a zero-value receipt is still recorded");
  assert.match(c, /no gaps|still recorded|trail/i);
});

// ---- what the page must never say ------------------------------------------
test("the page claims no privacy property it does not have", () => {
  const c = copy();
  // The tower relays the request, so it handles the content. Claiming otherwise is false.
  for (const lie of [/cannot see your prompts?/i, /never sees? your (prompts?|data|content)/i,
                     /end-to-end encrypted/i, /zero[- ]knowledge/i,
                     /we do not (see|read) your (prompts?|content)/i]) {
    assert.doesNotMatch(c, lie, `must not claim ${lie}`);
  }
});

test("the page invents no operational numbers", () => {
  const c = copy();
  assert.doesNotMatch(c, /\b99(\.\d+)?\s?%\s*(uptime|availability)/i, "no uptime figure");
  assert.doesNotMatch(c, /\b\d+\s?(ms|milliseconds)\b/i, "no latency figure");
  assert.doesNotMatch(c, /\b[\d,.]+\s*(req\/s|requests per second|rps|tok\/s|tokens per second)\b/i,
    "no throughput figure");
  assert.doesNotMatch(c, /\b(SOC ?2|ISO ?27001|HIPAA|PCI[- ]DSS|penetration test(ed)?|audited by)\b/i,
    "no certification or audit we have not had");
});

// "Verify it yourself" is the obvious close for a trust page and we cannot make it:
// station public keys are held broker-side and are not published, so a reader has no way
// to check the station signature independently. The page may say the receipt comes back;
// it may not say the reader can verify it.
test("the close claims the receipt is returned, not that it can be independently verified", () => {
  const c = copy();
  assert.match(c, /X-RogerAI-Receipt/, "the header the receipt rides back on");
  assert.match(c, /usage/i, "and where the same records show up again");
  for (const overclaim of [/verify (it|the signature) yourself/i, /published (public )?keys?/i,
                           /check the signature (yourself|independently)/i,
                           /publicly verifiable/i, /anyone can verify/i]) {
    assert.doesNotMatch(c, overclaim, `must not claim ${overclaim} - we do not publish node keys`);
  }
  // The relay FACT must stay somewhere on the page. The founder removed the caveat
  // plate in the closing section; the fact moved into the signal-path caption, which is
  // where it belongs - the figure already draws the request passing through the tower, so
  // it reads as a description of the path rather than a disclaimer.
  assert.match(c, /relays both directions|handles the request and the response/i,
    "the page still says the tower handles the content in transit");
  assert.match(c, /privacy policy/i, "and still points at where retention is described");
});

test("the figures survive no motion, no script, and a narrow screen", () => {
  const html = page();
  const css = read("styles/tower.css");
  const figures = [...html.matchAll(/<figure class="(tower__[a-z]+)"/g)].map((m) => m[1]);
  assert.ok(figures.length >= 2, `the page carries both figures, found ${figures.length}`);
  for (const f of figures) {
    assert.match(html, new RegExp(`<figure class="${f}"[^>]*aria-`), `${f} has an accessible name`);
  }
  assert.doesNotMatch(html.match(/<main[\s\S]*?<\/main>/)[0], /<script/i, "nothing is injected");
  const reduced = (css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/g) || []).join("");
  assert.match(reduced, /animation: none/, "motion is escapable");
  assert.match(css, /max-width: 100%|width: 100%/, "the figures scale");
});

// The tower page says "the API, the manual and the source call it the broker". That is a
// claim ABOUT the manual, so the manual has to make the same reconciliation - otherwise a
// reader who follows it finds no mention of the Tower at all and the two names stay split.
test("the manual and the tower page reconcile the same two names", () => {
  const manual = read("manual.html");
  const gloss = manual.match(/<dl class="man-gloss">[\s\S]*?<\/dl>/)[0];
  assert.match(gloss, /tower/i, "the glossary names the Tower");
  assert.match(gloss, /broker/i, "beside the broker");
  assert.match(gloss, /href="\/tower\.html"/, "and links the page");
  // And the claim on the tower page stays true in both directions.
  assert.match(visible(page()), /manual/i, "the tower page points back at the manual's term");
});
