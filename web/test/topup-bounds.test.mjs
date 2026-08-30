// The top-up bounds are ONE pair of numbers that live in Go
// (internal/client/topup_amount.go). Every other surface restates them - the billing
// page's input attributes, its click handler, the local console's - and a restatement
// drifts. This reads the constants out of the Go source and requires the web surfaces to
// agree, so a change to the policy fails here instead of shipping a page that promises
// one number while the broker enforces another.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
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
// "125"); the word boundary KEPT, because dropping it for the lookahead let `usd < 1e6`
// and `usd < 1_000` satisfy a pin that reads as `usd < 1`; and the lookahead on top of
// it for the case \b cannot see - a decimal, where `usd < 1.5` sits at a boundary after
// the 1 and would otherwise satisfy a pin of 1.
const bound = (v) => `${String(v).replace(/\./g, "\\.")}\\b(?![\\d.])`;

// The same number as a person reads it, which is how the surfaces print it. A whole
// dollar prints plain ("$1"), anything else with cents and separators ("$999,999.99"),
// so a floor that became 2.50 or 1000 is compared against what a corrected surface would
// actually say rather than against String(v).
const money = (v) =>
  v.toLocaleString("en-US", { minimumFractionDigits: Number.isInteger(v) ? 0 : 2 });

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

test("topup bounds: every user-facing minimum is the current minimum", () => {
  // The maximum was pinned and the minimum was not, so the same hole stayed open at the
  // other end: three surfaces print "$1" as a literal and would have gone on promising
  // it. Go writes the floor as a whole dollar, and so do the surfaces.
  const floor = money(goConst("MinTopupUSD")); // "1"
  const surfaces = {
    "js/billing.js": src("js/billing.js"),
    "webui console.js": readFileSync(path.join(REPO, "internal", "webui", "assets", "console.js"), "utf8"),
  };
  for (const [name, js] of Object.entries(surfaces)) {
    const stated = [...js.matchAll(/an amount of \$([\d,.]+) or more|[Tt]op-up minimum is \$([\d,.]+)/g)]
      .map((m) => (m[1] || m[2]).replace(/[.,]$/, ""));
    assert.ok(stated.length > 0, `${name} states the minimum to a person`);
    for (const shown of stated) {
      assert.equal(shown, floor, `${name} tells the reader $${shown} while the floor is $${floor}`);
    }
  }

  // The prose surfaces print it too, and pinning only the scripts is the same
  // one-end-pinned asymmetry this test was added to close.
  //
  // Anchored to the TOP-UP context, not to the bare word "minimum". These pages also
  // carry the $25 PAYOUT minimum, and both write it in shapes this would otherwise
  // match - "$25 minimum" is only saved from the second alternative by the absence of a
  // </b>, which is a coincidence of styling rather than a rule, and bolding it the way
  // pricing.html bolds the floor would have tripped this on correct copy.
  //
  // EVERY page, and every occurrence CLASSIFIED rather than filtered. A filter that
  // skips what it does not recognize checks nothing about what it skipped, and a
  // per-page count only catches a mention disappearing - a new one stated far from the
  // words "top-up" still slid past. So each occurrence must be either a top-up minimum,
  // which has to equal the floor, or the payout minimum, which is a different policy
  // number this test does not own. Anything in neither context fails by name, because an
  // unclassifiable dollar minimum in the docs is the thing worth looking at.
  const TOPUP_NEAR = 140;
  const payoutMin = String(
    readFileSync(path.join(REPO, "internal", "store", "ledger.go"), "utf8")
      .match(/MinPayout:\s*([0-9.]+)/)?.[1],
  );
  assert.ok(payoutMin !== "undefined", "the payout minimum is declared in internal/store/ledger.go");

  // Per-page counts, not a global tally: a global one stays green if a mention MOVES
  // between pages, so pricing.html could lose its only statement while the manual gained
  // one. A page not listed must state it zero times, which is what makes a new page
  // stating the floor fail rather than pass unnoticed.
  const EXPECTED = { "pricing.html": 1, "manual.html": 3 };
  // _partials included: a minimum stated in a partial appears on every built page and
  // would be checked on none.
  const pages = [
    ...readdirSync(path.join(WEB, "src")).filter((f) => f.endsWith(".html")),
    ...readdirSync(path.join(WEB, "src", "_partials")).filter((f) => f.endsWith(".html")).map((f) => `_partials/${f}`),
  ];
  const seen = {};
  for (const page of pages) {
    const html = src(page);
    for (const m of html.matchAll(/minimum[^<$]{0,12}\$(\d(?:[\d,]*\d)?(?:\.\d+)?|\.\d+)|\$(\d(?:[\d,]*\d)?(?:\.\d+)?|\.\d+)(?:<\/[a-z]+>)?\s*minimum/gi)) {
      const window = html.slice(Math.max(0, m.index - TOPUP_NEAR), m.index + TOPUP_NEAR);
      const shown = m[1] || m[2]; // the capture ends in a digit, so nothing to strip
      // Compared as NUMBERS: the payouts page renders "$25.00" from a live figure while
      // the policy is written 25, and those are the same minimum.
      const amount = Number(shown.replace(/,/g, ""));
      if (/top-?up/i.test(window)) {
        seen[page] = (seen[page] || 0) + 1;
        // The top-up floor stays an EXACT string match: money() already produces the
        // form the surfaces print, and comparing numerically would quietly accept
        // "$1.00" where every surface says "$1".
        assert.equal(shown, floor,
          `${page} tells the reader the top-up minimum is $${shown}, but the floor is $${floor}`);
      } else if (/payout|payable|cash ?out|earnings/i.test(window)) {
        assert.equal(amount, Number(payoutMin),
          `${page} states a payout minimum of $${shown}, but the policy is $${payoutMin}`);
      } else {
        assert.fail(
          `${page} states a "$${shown}" minimum in neither a top-up nor a payout context. ` +
            "Classify it: if it is one of those, widen the context words here; if it is a " +
            "third kind of minimum, this test's scope needs saying out loud.",
        );
      }
    }
  }
  // Iterate the MAP as well as the pages: a page listed here and then deleted would
  // otherwise have its pin evaporate silently, which is the same skip-and-say-nothing
  // shape as everything else this file has had to grow out of.
  for (const page of Object.keys(EXPECTED)) {
    assert.ok(pages.includes(page), `${page} is gone - the top-up minimum it carried needs a new home`);
  }
  for (const page of pages) {
    assert.equal(seen[page] || 0, EXPECTED[page] || 0,
      `${page} states the top-up minimum ${seen[page] || 0} times, expected ${EXPECTED[page] || 0} - if the copy changed on purpose, update the map here`);
  }
});

// Scoping the guard pins to their handlers left the LABELS unpinned: the button copy,
// the hint under it, and the console toast all print the maximum as a literal, and a
// changed constant would leave them promising the old number. Every place a surface
// states the cap to a person has to state the current one.
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
