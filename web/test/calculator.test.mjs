// The teardown's second gap: Edge Impulse's ROI calculator is framing with no arithmetic
// ("reduce total cost of ownership", "sidestep unforeseen expenses"), because a
// build-vs-buy model has no honest inputs - it is a guess about your staffing times a
// guess about salary. We are pricing a metered commodity, so real inputs exist.
//
// The rule this file enforces is what keeps ours from becoming theirs. Every number in
// either calculator is exactly one of:
//
//   1. the person's own input,
//   2. a policy constant of ours, read from the Go that enforces it, or
//   3. live market data.
//
// Nothing else. In particular the page must never print a competitor's price: that would
// be publishing someone else's number, committing to keep it true, and putting a
// falsifiable claim on the one page whose argument is that we do not do that. The
// consumer side asks what you pay today instead, which is also more accurate than
// anything we could quote.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const REPO = path.join(WEB, "..");
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const go = (...p) => readFileSync(path.join(REPO, ...p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const calc = () => src("js/calc.js");
const section = () => read("pricing.html").match(/<section[^>]*id="calc"[\s\S]*?<\/section>/)?.[0] || "";

/* ---- it exists, and it is on the pricing page ----------------------- */

test("calculator: the pricing page carries it, with its own script", () => {
  const page = read("pricing.html");
  assert.ok(section(), "the pricing page has a calculator section");
  assert.match(page, /js\/calc\.js/, "and loads the behavior");
});

test("calculator: both sides are there - what you would earn, and what you pay now", () => {
  const s = section();
  assert.match(s, /data-calc="operator"/, "the operator calculator");
  assert.match(s, /data-calc="consumer"/, "the consumer calculator");
});

/* ---- rule 2: our constants come from the Go that enforces them ------ */

test("calculator: the platform fee is the broker's own, not a number typed twice", () => {
  // cmd/rogerai-broker/main.go declares the take rate the broker actually applies.
  const fee = go("cmd", "rogerai-broker", "main.go").match(/flag\.Float64\("fee",\s*([0-9.]+)/)?.[1];
  assert.ok(fee, "the broker declares its fee rate");
  const share = 1 - Number(fee);
  assert.ok(calc().includes(String(share)), `calc.js uses the operator share ${share} derived from the broker's fee`);
  assert.match(section(), new RegExp(`${Math.round(share * 100)}%`), "and the page states it");
});

/* ---- rule 3, and the prohibition: no third-party prices ------------- */

test("calculator: it never prints a price that belongs to someone else", () => {
  // Naming a competitor's rate would commit us to keeping their price sheet current on
  // our pricing page. The consumer side takes the number from the person instead.
  const s = section().replace(/\s+/g, " ");
  for (const rival of [/openai/i, /anthropic/i, /gpt-4/i, /claude/i, /gemini/i, /together\.ai/i, /fireworks/i, /groq/i]) {
    assert.doesNotMatch(s, rival, `the calculator names ${rival}, and would then owe the reader a current price`);
  }
  assert.match(s, /what you pay|you pay today|your current/i,
    "the consumer side must ask for the reader's own price");
});

test("calculator: the live rate is read, never baked in", () => {
  const js = calc();
  assert.match(js, /\/market/, "it reads the live market");
  // And it must have somewhere honest to stand when the read fails or the band is free,
  // rather than falling back to an invented rate.
  assert.match(js, /quiet|unavailable|free/i, "it has a stated empty state");
  assert.doesNotMatch(js, /DEFAULT_MARKET_RATE|fallbackRate|assumedRate/i,
    "no invented market rate");

  // min_price is the cheapest INPUT price (cmd/rogerai-broker/market.go). Every figure on
  // this page is an OUT price, so reading min_price multiplied an output volume by an
  // input rate and overstated the saving. The field it reads must be the out one, and
  // there must be no fallback to the input one - that would be the bug wearing a
  // fallback.
  const market = go("cmd", "rogerai-broker", "market.go");
  assert.match(market, /MinOut\s+float64\s+`json:"min_out"`/, "the broker serializes an out-price");
  assert.match(market, /MinPrice[\s\S]{0,120}cheapest active input price/i,
    "and min_price is still documented as the INPUT price this must not use");
  assert.match(js, /min_out/, "the calculator reads the out-price");
  assert.doesNotMatch(js.replace(/\/\/[^\n]*/g, ""), /min_price/,
    "and never the input price, not even as a fallback");

  // The rate is one model's, and free bands are excluded from it. Both were true before
  // and neither was said, so the note called it "the cheapest on air" while free bands
  // were on air and the price belonged to some other model.
  assert.match(js, /PAID band on air/, "the note says the rate is a PAID band's");
  assert.match(js, /best\.model/, "and names which band it came from");
  // The fetched rate is shown within the field's own declared bounds, since num() clamps
  // to them on the way in - otherwise the page displays a rate the sum does not use.
  // Live data is never CLAMPED to a reader-input bound: doing so displayed and computed
  // a rate the market did not report. Out of range in either direction is handed back.
  assert.match(js, /best\.price > hi/, "a rate above the field's range is refused, not clamped");
  assert.match(js, /best\.price < 0\.005/, "and one too small to express is refused too");
  assert.doesNotMatch(js, /Math\.min\(best\.price/, "the live rate is not clamped");
  // Nor ROUNDED. toFixed(2) turned 0.0149 into 0.01 - the same "a rate the market did not
  // report" failure, one line after the guards against it. The field takes step="any" so
  // the real number fits, and the <0.005 guard keeps String() clear of exponent form.
  assert.doesNotMatch(js, /best\.price\.toFixed/, "the live rate is written verbatim");
  assert.match(js, /field\.value = String\(best\.price\)/, "exactly as the market reported it");

  // An unknown rate must not be treated as a free one. When the market cannot be read the
  // field is emptied, and an empty field stops the comparison rather than printing a
  // full-price "saving" against an implied zero.
  assert.match(js, /set a rate/, "an empty band rate asks for one instead of computing");
  assert.match(js, /value\.trim\(\) === ""/, "and the empty case is detected explicitly");
});

/* ---- the arithmetic is shown, and the uncertainty is named ---------- */

test("calculator: the inputs are clamped to the bounds the markup declares", () => {
  // The markup declares a 24-hour day and a 100% ceiling; without clamping, the
  // arithmetic ignored both and a typed 500% printed a number nobody could earn.
  const js = calc();
  assert.match(js, /el\.min/, "the floor each input declares is honored");
  assert.match(js, /el\.max/, "and so is the ceiling");
});

test("calculator: it shows its working", () => {
  const s = section();
  assert.match(s, /data-calc-formula/, "the arithmetic is rendered, not just the answer");
});

test("calculator: the operator side does not promise demand", () => {
  // The one dishonest thing a GPU-earnings calculator can do is present a capacity
  // number as an income forecast. Utilisation is an input for exactly that reason, and
  // the panel has to say out loud that nobody can promise it.
  const s = section().replace(/\s+/g, " ");
  assert.match(s, /estimate/i, "it is labelled an estimate");
  assert.match(s, /not guarantee|no guarantee|nobody can promise|cannot promise/i,
    "it says demand is not guaranteed");
  assert.match(s, /utilisation|utilization|how busy/i, "utilisation is an input, not an assumption");
});

/* ---- it works without JavaScript, or says it does not --------------- */

test("calculator: with no JavaScript it is honest rather than blank", () => {
  const s = section();
  assert.match(s, /<noscript>/, "a no-JS reader is told what they are missing");
  // Inputs carry their defaults in the markup, so the shipped page is never an empty box.
  const numbers = [...s.matchAll(/<input[^>]*type="number"[^>]*>/g)].map((m) => m[0]);
  assert.ok(numbers.length >= 4, `the inputs are real form controls (found ${numbers.length})`);
  for (const input of numbers) {
    assert.match(input, /id="[^"]+"/, "each input has an id for its label");
    // Every READER input ships a default so the page is never an empty box. The band
    // rate is the exception and must ship EMPTY: it is live data, and a plausible
    // default there is the invented rate this page forbids.
    if (/id="cnHere"/.test(input)) {
      assert.match(input, /value=""/, "the live band rate ships empty, not guessed");
      assert.match(input, /placeholder="[^"]+"/, "and says what it wants instead");
      continue;
    }
    assert.match(input, /value="[^"]+"/, "each reader input ships a default");
  }
  // And the shipped answer for it is the ask, not a number computed against zero.
  assert.match(s, />set a rate</, "the unanswered state ships in the markup");
  // The unit is hidden alongside it, or a live region announces "set a rate a month,
  // difference" as one sentence.
  assert.match(s, /id="cnUnit"[^>]*hidden/, "the unit is hidden while the answer is a prompt");
});

// The worked example that ships in the markup is what a no-JS reader sees and what a
// crawler quotes, and the first draft of it was wrong by a factor of 33 - it multiplied
// to a daily figure and labelled it monthly. Recomputing it from the shipped defaults is
// the only way that stays true when someone edits a default.
test("calculator: the printed example is what the printed inputs actually produce", () => {
  const s = section();
  const val = (id) => Number(s.match(new RegExp(`id="${id}"[^>]*value="([^"]+)"`))?.[1]);
  const fee = Number(go("cmd", "rogerai-broker", "main.go").match(/flag\.Float64\("fee",\s*([0-9.]+)/)?.[1]);
  const days = Number(calc().match(/var DAYS = (\d+)/)?.[1]);
  assert.ok(days > 0, "calc.js declares the month length it uses");

  const millions = val("opTps") * 3600 * val("opHours") * (val("opBusy") / 100) * days / 1e6;
  const gross = millions * val("opRate");
  const share = gross * (1 - fee);
  const formula = s.match(/id="opFormula"[^>]*>([\s\S]*?)<\/p>/)?.[1].replace(/\s+/g, " ") || "";

  assert.ok(formula.includes(millions.toFixed(1) + "M"),
    `the printed example says ${formula.trim()}, but the shipped inputs give ${millions.toFixed(1)}M tokens`);
  assert.ok(formula.includes(gross.toFixed(2)), `gross should be ${gross.toFixed(2)}`);
  assert.ok(formula.includes(share.toFixed(2)), `the share should be ${share.toFixed(2)}`);
  assert.ok(s.includes(">$" + share.toFixed(2) + "<"), "and the big number agrees with the formula");

  // The consumer example shows the half it CAN know - what the reader pays today - and
  // asks for the other half. Shipping a difference would mean shipping a band rate, and
  // the build does not have one.
  const today = val("cnVolume") * val("cnPrice");
  const cnFormula = s.match(/id="cnFormula"[^>]*>([\s\S]*?)<\/p>/)?.[1].replace(/\s+/g, " ") || "";
  assert.ok(cnFormula.includes(today.toFixed(2)), `the consumer example should show ${today.toFixed(2)}`);
  // Any "= $X here" clause would mean the markup shipped a band rate, and the build has
  // none. Forbidding only the zero spelling let a fabricated non-zero one through.
  assert.doesNotMatch(cnFormula, /=\s*\$[\d.,]+\s*here/,
    "the shipped example must not price the band at all when no rate is known");
});

// Inserting the calculator gave the page two §5 headings, which nothing caught - the
// duplicate only showed up in a screenshot. Section numbers are a promise that the page
// reads in order, so they are checked.
test("calculator: the pricing page's sections are numbered once each, in order", () => {
  const nums = [...read("pricing.html").matchAll(/&sect;(\d+) \/ [A-Z]/g)].map((m) => Number(m[1]));
  assert.ok(nums.length >= 6, `the page is numbered (${nums.length} sections)`);
  assert.deepEqual(nums, nums.map((_, i) => i + 1), "sections run 1..n with no repeat and no gap");
});

test("calculator: every input is labelled", () => {
  const s = section();
  const ids = [...s.matchAll(/<input[^>]*id="([^"]+)"/g)].map((m) => m[1]);
  assert.ok(ids.length > 0, "there are inputs");
  for (const id of ids) {
    assert.match(s, new RegExp(`<label[^>]*for="${id}"`), `${id} has a label`);
  }
});
