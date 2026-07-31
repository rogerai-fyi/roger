// The Wave family field guide: the slot table, the jobs table, and the industrial
// markets intro that sits on the deployment page.
//
// These tables are the site's main claim about WHERE a small model is actually useful,
// so the assertions are about breadth and internal consistency: a jobs table that names
// a slot the family does not have, or that covers three jobs, undersells or misdescribes
// the whole programme.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const visible = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// The §4 JOBS table, by its section rather than by position.
const jobsTable = () => {
  const page = read("research-wave-family.html");
  const section = page.match(/<section[^>]*id="jobs"[\s\S]*?<\/section>/)?.[0];
  assert.ok(section, "the family page has a §4 JOBS section");
  return section.match(/<table class="tier-matrix"[\s\S]*?<\/table>/)?.[0] || "";
};
const rows = (table) => [...table.matchAll(/<tr>(?!\s*<th scope="col")[\s\S]*?<\/tr>/g)]
  .map((m) => m[0]).filter((r) => /<th scope="row"/.test(r));

// Every slot the family actually has. A jobs row may name only these.
const SLOTS = ["Edge", "Nano", "Micro", "Core", "Station"];

test("the jobs table covers the plant broadly, not as a token gesture", () => {
  const table = jobsTable();
  const jobs = rows(table);
  assert.ok(jobs.length >= 16,
    `the jobs table is the breadth claim for the whole programme, found ${jobs.length} rows`);
});

test("every job names a slot the family actually has", () => {
  for (const row of rows(jobsTable())) {
    const cells = [...row.matchAll(/<t[dh][^>]*>([\s\S]*?)<\/t[dh]>/g)].map((m) => visible(m[1]));
    assert.equal(cells.length, 4, `every job row fills all four columns: ${cells[0]}`);
    for (const cell of cells) assert.ok(cell.length > 0, `no empty cell in "${cells[0]}"`);
    // The "smallest slot" cell reads like "Micro", "Edge + Nano", "Nano to Core".
    const named = cells[1].split(/\s*(?:\+|to|and|,)\s*/).filter(Boolean);
    assert.ok(named.length > 0, `"${cells[0]}" names a slot`);
    for (const slot of named) {
      assert.ok(SLOTS.includes(slot),
        `"${cells[0]}" points at "${slot}", which is not a Wave slot (${SLOTS.join(", ")})`);
    }
  }
});

test("no job is listed twice", () => {
  const names = rows(jobsTable()).map((r) => visible(r.match(/<th scope="row"[^>]*>([\s\S]*?)<\/th>/)[1]));
  assert.equal(new Set(names).size, names.length, "duplicate plant job in the table");
});

// "Four places the constraint is the point" told a reader nothing: it named neither the
// constraint nor why it favours us, and the line under it read as a hedge. The point is
// that these industries cannot send their data out, which is exactly what makes a local
// model the right answer rather than a compromise.
test("the markets section names the constraint instead of alluding to it", () => {
  const page = read("research-industry.html");
  const head = page.match(/<section[^>]*id="industry"[\s\S]*?<\/div>\s*<div class="deployment-grid"/)?.[0] || "";
  const copy = visible(head);
  assert.doesNotMatch(copy, /the constraint is the point/i, "the cryptic headline is gone");
  assert.match(copy, /data|network/i, "the headline names what the constraint IS");
  // The advisory boundary is the one claim on this page that must never blur.
  assert.match(copy, /advis/i, "the model advises");
  assert.match(copy, /never actuates|does not actuate|outside the control loop|out of the control loop/i,
    "it stays out of the control loop, said plainly rather than as jargon");
  assert.match(copy, /protection|safety/i, "and out of the protection path");
  // Engagements exist and are confidential. The page must signal the NDA rather than
  // imply an empty pipeline - the earlier copy read as "we have no customers", which is
  // both wrong and the worst possible thing to tell a grant reviewer.
  assert.match(copy, /NDA|under confidentiality|not named here/i,
    "the confidentiality is stated, so the shapes read as work rather than as wishes");
  assert.doesNotMatch(copy, /customers (it|we) do(es)? not have|no customers|none yet/i,
    "the page never claims an empty customer list");
});

// The Purdue figure placed the L3 box and said nothing about Roger Edge, which sits at a
// completely different level - it is not an LLM at all. The lab brief is blunt: "An LLM
// cannot run on an ESP32. Not a small one, not a quantized one, not ever." Roger Edge is
// a 100 KB - 2 MB classifier on its own battery-powered sensor node, and the sentence
// that settles the confusion is "Roger Edge detects; Wave reasons."
const purdue = () => {
  const fig = read("research-industry.html").match(/<figure class="purdue"[\s\S]*?<\/figure>/)?.[0];
  assert.ok(fig, "the industrial page draws the Purdue placement figure");
  return fig;
};

test("the diagram places Roger Edge down at the instrument, not beside the L3 box", () => {
  const fig = purdue();
  assert.match(fig, /ROGER EDGE/i, "Roger Edge appears on the figure");
  // Its box must sit at the L0/L1 band (y >= 194 in the 300-unit viewBox), NOT at L3.
  const edge = fig.match(/<g class="purdue__edge"[\s\S]*?<\/g>/)?.[0];
  assert.ok(edge, "Roger Edge is its own group on the figure");
  const y = Number(edge.match(/<rect[^>]*\by="(\d+)"/)[1]);
  assert.ok(y >= 194, `Roger Edge sits at the sensing levels, got y=${y}`);
});

test("the figure says Roger Edge detects rather than reasons", () => {
  const copy = visible(purdue());
  assert.match(copy, /detect/i, "Roger Edge detects");
  assert.match(copy, /escalat/i, "and escalates what it cannot classify");
  // The safety boundary must extend to the new box, not just the L3 one.
  assert.match(copy, /own sensor|its own device|own node/i,
    "it is its own device, not something wired into the control system");
});

test("the caption keeps every claim it had, and gains the escalation path", () => {
  const caption = visible(purdue().match(/<figcaption[\s\S]*?<\/figcaption>/)[0]);
  for (const claim of [/Level 3/i, /outbound/i, /no inbound listener/i, /safety-instrumented/i]) {
    assert.match(caption, claim, "the original placement claims survive");
  }
  assert.match(caption, /Roger Edge/, "and the caption now explains the second box");
  assert.doesNotMatch(caption, /Roger Edge[^.]*\b(writes|actuates|controls)\b/i,
    "Roger Edge never gains a control claim");
});

// Voices moved off the top bar into the Models disclosure panel, which made it reachable
// but not VISIBLE from the models directory itself: a reader on /models.html saw a link
// to it only in the collapsed panel and the footer. The two directories list the same
// live network in two media, so each has to offer the other in the page body.
test("the models directory offers the voices directory in its own body", () => {
  for (const [page, target] of [["models.html", "/voices.html"], ["voices.html", "/models.html"]]) {
    const html = read(page);
    const body = html.match(/<main[\s\S]*?<\/main>/)?.[0];
    assert.ok(body, `${page} has a <main>`);
    assert.match(body, new RegExp(`href="${target.replace(".", "\\.")}"`),
      `${page} links ${target} from the page itself, not only from the chrome`);
  }
});
