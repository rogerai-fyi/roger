// Executable lock for features/curated/curated_web.feature.
//
// Curated flow must be labeled apart wherever money or reputation renders, and the
// assertions here are of the "this distinction cannot silently disappear" form: the
// public dashboard counts proxies apart from humans, the private history names the
// provider and the split, and a curated operator's page never dresses reimbursement
// up as income.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// --- Scenario: the stations wall separates curated from human service -----------

test("the public dashboard counts curated stations apart from human ones", () => {
  const js = read("js/bands.js");
  // The human-supply counter and the curated counter are separate accumulators, and the
  // curated one never feeds b.live (the human claim).
  assert.match(js, /if \(o\.curated\) b\.curatedLive/, "a curated station increments its own counter");
  assert.match(js, /else b\.live\+\+/, "and never the human one");
  assert.match(js, /curated_providers/, "the /market curated count is read");
  // The row copy names them apart rather than folding them into "N stations".
  assert.match(js, /curated<\/span>/, "the band row labels curated supply");
});

test("curated stations render under their own heading in the station list", () => {
  const js = read("js/bands.js");
  assert.match(js, /filter\(function \(s\) \{ return s\.curated; \}\)/, "proxies are split out");
  assert.match(js, /qsl-row--heading/, "a heading row exists for them");
  assert.match(js, /commercial-API proxies, counted apart/, "the heading says what curated means");
});

// --- Scenario: the consumer's history shows the routing, privately --------------

test("the usage history names the band, the provider and the split", () => {
  const html = read("usage.html");
  assert.match(html, /id="useRecentRows"/, "the recent-requests table exists");
  assert.match(html, /<th>Band<\/th>/, "it names the band");
  assert.match(html, /<th>Routing<\/th>/, "and the routing");

  const js = read("js/metrics.js");
  assert.match(js, /curated_provider/, "a curated row names its provider");
  assert.match(js, /upstream \+ /, "and shows the split: list plus fee");
  assert.match(js, /curated_list/, "the list comes from the server, which knows the markup");
});

test("the history read is credentialed - no other account can see it", () => {
  const js = read("js/metrics.js");
  assert.match(js, /fetch\(BROKER \+ "\/usage", \{ credentials: "include" \}\)/,
    "the /usage read carries the caller's own credentials and nothing else");
});

// --- Scenario: the operator's earnings page shows pass-through, not profit ------

test("a curated operator's station keeps reimbursement and income apart", () => {
  const js = read("js/stations.js");
  assert.match(js, /CURATED/, "the curated badge exists");
  assert.match(js, /list reimbursed/, "the reimbursement is named as reimbursement");
  assert.match(js, /half of the routing fee/, "and the fee share is named as the income half");
  assert.match(js, /never income/, "the comment keeps the line: the reimbursement is never income");
  // The human branch still says Earned - the distinction is the point.
  assert.match(js, /'Earned ' \+/, "human earnings keep their own label");
});

// --- Scenario: a curated-only band is on air on the website, counted apart ------
// REGRESSION 2026-09-02: both /market readers computed live from providers alone,
// so the homepage said "8 bands on air" while the TUI dial showed 15.

test("both /market readers count curated bands as on air", () => {
  for (const f of ["js/market.js", "js/bands.js"]) {
    const js = read(f);
    assert.match(js, /var curated = \+m\.curated_providers \|\| 0;/,
      `${f} reads curated_providers`);
    assert.match(js, /var live = providers \+ curated > 0;/,
      `${f} includes curated supply in the on-air decision`);
  }
});

test("the rendered row keeps the curated count apart, marked", () => {
  const mk = read("js/market.js");
  assert.match(mk, /provBits\.push\('<span title="curated: commercial-API proxy stations, counted apart/,
    "the homepage row carries the counted-apart curated span");
  assert.match(mk, /&raquo; ' \+ c\.curated \+ ' curated/, "wearing the » mark");
  assert.doesNotMatch(mk, /providers \+ curated \+ ' on air'/,
    "and the human on-air number is never inflated by curated supply");
  const bj = read("js/bands.js");
  assert.match(bj, /provBits\.push\('<span class="band-curated"/,
    "the models dial keeps its counted-apart span");
});

test("the QSL detail subline never prints a zero human count for a curated-only band", () => {
  // audit 2026-09-02: the row hid the zero but the detail card said "0 stations on air".
  const js = read("js/bands.js");
  assert.match(js, /if \(b\.providers > 0\) subBits\.push/, "the human count renders only when > 0");
  assert.doesNotMatch(js, /"- " \+ subN \+ " station"/, "the unconditional count form is gone");
});
