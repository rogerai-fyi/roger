// Unit lock for RogerFmt (web/src/js/fmt.js). Run: node --test web/test/
//
// fmt.js is a browser IIFE (assigns window.RogerFmt, and module.exports for here). We load it
// in a tiny sandbox so the boundary + Go-parity contract is tested without a browser. The money
// rule MUST match the Go internal/client FormatUSD (the whole point of "web reads like the CLI").
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const src = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/fmt.js"), "utf8");
const mod = { exports: {} };
// run the IIFE with `module` provided and `window` undefined, so it exports R for us.
new Function("module", "window", src)(mod, undefined);
const R = mod.exports;

test("usd: matches Go FormatUSD (0, sub-cent 3-sig-fig, >=$0.01 two dp, guards)", () => {
  const cases = [
    [0, "$0.00"],
    [12.5, "$12.50"],
    [0.01, "$0.01"],
    [0.0012, "$0.0012"],
    [0.0001234, "$0.000123"],   // 3 sig figs (was the 8-dp divergence)
    [0.00123456, "$0.00123"],
    [0.00000036, "$0.00000036"],
    [3e-9, "$0.000000003"],      // P1: a real tiny charge must NOT collapse to $0 / $0.00
    [-1, "-"], [NaN, "-"], [Infinity, "-"],
  ];
  for (const [in_, want] of cases) assert.equal(R.usd(in_), want, `usd(${in_})`);
});

test("usdExact: full precision, never $0.00 for a real charge", () => {
  assert.equal(R.usdExact(3e-9), "$0.000000003");
  assert.equal(R.usdExact(0.00000036), "$0.00000036");
  assert.equal(R.usdExact(1234.5678), "$1,234.5678");
  assert.equal(R.usdExact(0), "$0.00");
});

test("count: compact k/M/B/T, promotes at band tops (no '1000k')", () => {
  const cases = [
    [999, "999"], [1000, "1k"], [1111, "1.1k"], [11111, "11k"],
    [999999, "1M"],            // promoted, NOT "1000k"
    [1000000, "1M"], [1111111, "1.1M"], [12000000, "12M"],
    [999999999, "1B"],         // promoted, NOT "1000M"
    [1111111111, "1.1B"], [12000000000, "12B"], [1.5e12, "1.5T"],
    [-1500, "-1.5k"], [NaN, "-"], [Infinity, "-"],
  ];
  for (const [in_, want] of cases) assert.equal(R.count(in_), want, `count(${in_})`);
});

test("exact: full grouped integer", () => {
  assert.equal(R.exact(1111111111), "1,111,111,111");
  assert.equal(R.exact(Infinity), "-");
});
