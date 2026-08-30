// The top-up bounds are ONE pair of numbers that live in Go
// (internal/client/topup_amount.go). Every other surface restates them - the billing
// page's input attributes, its click handler, the local console's - and a restatement
// drifts. This reads the constants out of the Go source and requires the web surfaces to
// agree, so a change to the policy fails here instead of shipping a page that promises
// one number while the broker enforces another.
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

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// Returned as a NUMBER: Go writes the floor as 1.0 and the HTML attribute as "1", which
// are the same bound and different strings.
const goConst = (name) => {
  const go = readFileSync(path.join(REPO, "internal", "client", "topup_amount.go"), "utf8");
  // Tolerates a grouped `const ( ... )` declaration, so moving the constants together
  // does not fail this with a misleading "is not declared".
  const m = go.match(new RegExp(`(?:const\\s+)?${name}\\s*=\\s*([0-9.]+)`));
  assert.ok(m, `${name} is declared in internal/client/topup_amount.go`);
  return Number(m[1]);
};

// A bound as a regex fragment. Three things, and the third was learned the hard way:
// every dot escaped (an unescaped one is a wildcard, so a floor of 1.5 would match
// "125"); a trailing digit/dot guard so `usd > 999999.99` is not satisfied by a guard
// written `usd > 999999.9999`; and the word boundary KEPT, because dropping it for the
// lookahead let `usd < 1e6` and `usd < 1_000` satisfy a pin that reads as `usd < 1`.
const bound = (v) => `${String(v).replace(/\./g, "\\.")}\\b(?![\\d.])`;

// The same number as a person reads it, which is how the surfaces print it.
const money = (v) => v.toLocaleString("en-US", { minimumFractionDigits: 2 });

test("topup bounds: the billing input allows exactly the range the broker allows", () => {
  const input = read("billing.html").match(/<input id="topupCustom"[^>]*>/)?.[0];
  assert.ok(input, "the custom-amount input is there");
  const attr = (name) => Number(input.match(new RegExp(`${name}="([0-9.]+)"`))?.[1]);
  assert.equal(attr("min"), goConst("MinTopupUSD"), "the floor matches MinTopupUSD");
  assert.equal(attr("max"), goConst("MaxTopupUSD"), "the ceiling matches MaxTopupUSD");
  // Cents are allowed, so the step cannot be whole dollars.
  assert.match(input, /step="0\.01"/, "the input accepts the cents the broker accepts");
});

test("topup bounds: the billing click handler guards both ends and whole cents", () => {
  // Scoped to the CLICK HANDLER, not the file. Two things bit earlier versions of this
  // test: a bare includes() is satisfied by a comment naming the number, and the same
  // comparison also appears in reflect(), which only paints a label - so a file-wide
  // match stayed green with the guard that actually stops the request deleted.
  const js = src("js/billing.js");
  const handler = js.match(/on\("topup", "click", function \(\) \{[\s\S]*?\n      \}\);/)?.[0];
  assert.ok(handler, "the top-up click handler is there");
  assert.match(handler, new RegExp(`usd < ${bound(goConst("MinTopupUSD"))}`), "the floor is a guard");
  assert.match(handler, new RegExp(`usd > ${bound(goConst("MaxTopupUSD"))}`), "the ceiling is a guard");
  assert.match(handler, /Math\.round\(usd \* 100\)\) > 1e-6/, "and a sub-cent amount is refused");
  assert.doesNotMatch(handler, /Math\.round\(usd \* 100\) \/ 100/, "and none of them is rounded away");
});

test("topup bounds: the local console guards the same three things", () => {
  const js = readFileSync(path.join(REPO, "internal", "webui", "assets", "console.js"), "utf8");
  const topup = js.match(/function topup\(\)[\s\S]*?\n  \}/)?.[0] || "";
  assert.ok(topup, "the console has a topup handler");
  assert.match(topup, new RegExp(`usd < ${bound(goConst("MinTopupUSD"))}`), "floor");
  assert.match(topup, new RegExp(`usd > ${bound(goConst("MaxTopupUSD"))}`), "ceiling");
  assert.match(topup, /Math\.round\(usd \* 100\)\) > 1e-6/, "whole cents");
});

// Scoping the guard pins to their handlers left the LABELS unpinned: the button copy,
// the hint under it, and the console toast all print the maximum as a literal, and a
// changed constant would leave them promising the old number. Every place a surface
// states the cap to a person has to state the current one.
test("topup bounds: every user-facing minimum is the current minimum", () => {
  // The maximum was pinned and the minimum was not, so the same hole stayed open at the
  // other end: three surfaces print "$1" as a literal and would have gone on promising
  // it. Go writes the floor as a whole dollar, and so do the surfaces.
  const floor = String(goConst("MinTopupUSD")).replace(/\.0$/, "");
  const surfaces = {
    "js/billing.js": src("js/billing.js"),
    "webui console.js": readFileSync(path.join(REPO, "internal", "webui", "assets", "console.js"), "utf8"),
  };
  for (const [name, js] of Object.entries(surfaces)) {
    const stated = [...js.matchAll(/an amount of \$([\d,.]+) or more|[Tt]op-up minimum is \$([\d,.]+)/g)]
      .map((m) => (m[1] || m[2]).replace(/\.$/, ""));
    assert.ok(stated.length > 0, `${name} states the minimum to a person`);
    for (const shown of stated) {
      assert.equal(shown, floor, `${name} tells the reader $${shown} while the floor is $${floor}`);
    }
  }
});

test("topup bounds: every user-facing maximum is the current maximum", () => {
  const cap = money(goConst("MaxTopupUSD")); // "999,999.99"
  const surfaces = {
    "js/billing.js": src("js/billing.js"),
    "webui console.js": readFileSync(path.join(REPO, "internal", "webui", "assets", "console.js"), "utf8"),
  };
  for (const [name, js] of Object.entries(surfaces)) {
    const stated = [...js.matchAll(/maximum top-up is \$([\d,.]+)|Top-up maximum is \$([\d,.]+)/gi)]
      .map((m) => (m[1] || m[2]).replace(/\.$/, ""));
    assert.ok(stated.length > 0, `${name} states the maximum to a person`);
    for (const shown of stated) {
      assert.equal(shown, cap, `${name} tells the reader $${shown} while the cap is $${cap}`);
    }
  }
});

// reflect() paints the button and does not stop anything, but it decides which of the
// three labels the reader sees, so its ceiling has to be the real one too.
test("topup bounds: the button label branches on the same ceiling", () => {
  const reflect = src("js/billing.js").match(/function reflect\(\)[\s\S]*?\n      \}/)?.[0];
  assert.ok(reflect, "reflect() is there");
  assert.match(reflect, new RegExp(`usd <= ${bound(goConst("MaxTopupUSD"))}`),
    "the accept branch uses the real ceiling");
  assert.match(reflect, new RegExp(`usd > ${bound(goConst("MaxTopupUSD"))}`),
    "and so does the over-the-cap branch");
  const floors = reflect.match(new RegExp(`usd >= ${bound(goConst("MinTopupUSD"))}`, "g")) || [];
  assert.equal(floors.length, 2, "both label branches gate on the real floor");
});
