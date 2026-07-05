// Behavior lock for the homepage money meter (web/src/js/market.js): the RATE / REPLY
// line under the live band panel. The founder caught it HARDCODED ("$0.18 - $0.55",
// "~$0.0001 / 24 tok out") while every on-air band is FREE - the meter must read the
// LIVE band and never invent figures. market.js is a browser IIFE; with `document`
// undefined it exports its pure readout bits and skips all DOM/fetch (the dashboard.js
// seam pattern). Run: node --test web/test/
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const dir = path.dirname(fileURLToPath(import.meta.url));
const src = readFileSync(path.join(dir, "../src/js/market.js"), "utf8");

// Load market.js the node way (document undefined -> export path). `win` lets a case
// supply window.RogerFmt, the canonical money formatter, exactly as the page does.
function load(win) {
  const mod = { exports: {} };
  new Function("module", "window", src)(mod, win);
  return mod.exports;
}
const R = load(undefined);

const ch = (price, live = true) => ({ model: "m", price, live });

test("quiet: no channels / nothing on air -> neutral readout, no figures at all", () => {
  assert.equal(R.meterReadout([]).kind, "quiet");
  assert.equal(R.meterReadout(null).kind, "quiet");
  assert.equal(R.meterReadout(undefined).kind, "quiet");
  // an idle station's price is NOT a rate anyone can pay - off-air never drives the meter
  assert.equal(R.meterReadout([ch(0.3, false), ch(9.99, false)]).kind, "quiet");
});

test("all-free band (today's market): FREE + $0.00 reply - never the old fake range", () => {
  const r = R.meterReadout([ch(0), ch(0), ch(0)]);
  assert.equal(r.kind, "free");
  assert.equal(r.rate, "FREE");
  assert.equal(r.reply, "$0.00");
});

test("unpriced / junk prices read as free, never as invented numbers", () => {
  for (const bad of [undefined, null, NaN, "x", -1]) {
    const r = R.meterReadout([ch(bad)]);
    assert.equal(r.kind, "free", `price ${String(bad)} must fold to free`);
  }
});

test("priced band: real min..max of on-air out-prices, reply from the real mid rate", () => {
  // idle 9.99 is on the books but off the air - it must not stretch the range
  const r = R.meterReadout([ch(0.18), ch(0.3), ch(0.55), ch(9.99, false)]);
  assert.equal(r.kind, "priced");
  assert.equal(r.rate, "$0.18 - $0.55");
  // mid (0.18 + 0.55) / 2 = 0.365 $/1M on 24 output tokens = $0.00000876
  assert.equal(r.reply, "~$0.00000876");
});

test("single-price band: one honest figure, no fake range", () => {
  const r = R.meterReadout([ch(0.3), ch(0.3)]);
  assert.equal(r.kind, "priced");
  assert.equal(r.rate, "$0.30");
  assert.equal(r.reply, "~$0.0000072"); // 0.30 * 24 / 1e6, trailing zero trimmed
});

test("mixed free + paid: the range starts at free and the mid includes the free end", () => {
  const r = R.meterReadout([ch(0), ch(0.55)]);
  assert.equal(r.kind, "priced");
  assert.equal(r.rate, "free - $0.55"); // same 'free' wording as the band rows
  assert.equal(r.reply, "~$0.0000066"); // mid 0.275 * 24 / 1e6
});

test("money parity: with RogerFmt loaded (the page path) the readout is identical", () => {
  const fmtSrc = readFileSync(path.join(dir, "../src/js/fmt.js"), "utf8");
  const fmtMod = { exports: {} };
  new Function("module", "window", fmtSrc)(fmtMod, undefined);
  const withFmt = load({ RogerFmt: fmtMod.exports });
  for (const chans of [
    [ch(0)],
    [ch(0.18), ch(0.55)],
    [ch(0.3)],
    [ch(0), ch(0.55)],
    [ch(0.001), ch(0.002)], // tiny rates: reply lands in exponential-notation territory
  ]) {
    assert.deepEqual(withFmt.meterReadout(chans), R.meterReadout(chans));
  }
});

test("fmtPrice: the shared price renderer the meter range reuses stays free-aware", () => {
  assert.equal(R.fmtPrice(0), "free");
  assert.equal(R.fmtPrice(0.3), "$0.30");
});

// --- transient-error resilience: never blank the market on a non-200 (the "flickers to
// empty" incident). decideRender is the pure decision the fetch path uses. -----------------

test("decideRender: fresh live data always renders", () => {
  assert.equal(R.decideRender({ liveCount: 3, marketOK: true, discoverOK: true, prevCount: 0 }), "live");
  // even a transient non-200 alongside live offers (shouldn't happen, but) renders the live data
  assert.equal(R.decideRender({ liveCount: 1, marketOK: false, discoverOK: false, prevCount: 5 }), "live");
});

test("decideRender: a transient non-200 on BOTH reads HOLDS a last-known market (never blanks)", () => {
  // This is the release-day bug: a 429 body has no offers -> liveCount 0, neither read OK.
  // With a previous market on screen we HOLD it rather than paint the empty state.
  assert.equal(R.decideRender({ liveCount: 0, marketOK: false, discoverOK: false, prevCount: 6 }), "hold");
});

test("decideRender: a transient failure with NOTHING to hold falls to the honest unreachable state", () => {
  assert.equal(R.decideRender({ liveCount: 0, marketOK: false, discoverOK: false, prevCount: 0 }), "quiet-unreachable");
});

test("decideRender: a REACHABLE broker that genuinely returns empty shows the honest quiet state", () => {
  // A 200 with an empty list is NOT transient - it is an honest empty market, even if we had a
  // last-known one (the market really did go quiet).
  assert.equal(R.decideRender({ liveCount: 0, marketOK: true, discoverOK: false, prevCount: 6 }), "quiet-empty");
  assert.equal(R.decideRender({ liveCount: 0, marketOK: false, discoverOK: true, prevCount: 6 }), "quiet-empty");
  assert.equal(R.decideRender({}), "quiet-unreachable"); // defensive: no info == treat as unreachable, nothing held
});

test("parseRetryAfter: integer seconds -> ms; absent/garbage -> 0", () => {
  const mk = (v) => ({ headers: { get: (k) => (k === "Retry-After" ? v : null) } });
  assert.equal(R.parseRetryAfter(mk("5")), 5000);
  assert.equal(R.parseRetryAfter(mk("1")), 1000);
  assert.equal(R.parseRetryAfter(mk("0")), 0);      // 0/negative is not a useful delay
  assert.equal(R.parseRetryAfter(mk("nope")), 0);
  assert.equal(R.parseRetryAfter(mk(null)), 0);
  assert.equal(R.parseRetryAfter({}), 0);           // no headers
  assert.equal(R.parseRetryAfter(null), 0);         // no response at all
});
