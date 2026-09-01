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
  assert.match(js, /upstream \+ /, "and shows the split: pass-through plus fee");
  assert.match(js, /cost \|\| 0\) - \(\+e\.owner_share/, "the fee is the difference, never a second charge");
});

test("the history read is credentialed - no other account can see it", () => {
  const js = read("js/metrics.js");
  assert.match(js, /fetch\(BROKER \+ "\/usage", \{ credentials: "include" \}\)/,
    "the /usage read carries the caller's own credentials and nothing else");
});

// --- Scenario: the operator's earnings page shows pass-through, not profit ------

test("a curated operator's station shows pass-through, never income", () => {
  const js = read("js/stations.js");
  assert.match(js, /CURATED/, "the curated badge exists");
  assert.match(js, /Upstream pass-through/, "the money line is named pass-through");
  assert.match(js, /not income/, "and says outright that it is not income");
  // The human branch still says Earned - the distinction is the point.
  assert.match(js, /'Earned ' \+/, "human earnings keep their own label");
});
