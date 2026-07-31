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

// §2 CAPABILITY read as a post-mortem - "we cut a model and measured what BROKE" - when
// the finding is the opposite: delete 93.3% of a frontier model's experts and the plant's
// actual work still lands. The section now leads with the survivor and DRAWS the ratio
// instead of describing it, because the number is the whole argument.
const capability = () => {
  const page = read("research-wave-family.html");
  const s = page.match(/<section[^>]*id="capability"[\s\S]*?<\/section>/)?.[0]
        || page.match(/<!-- §2 CAPABILITY -->[\s\S]*?<\/section>/)?.[0];
  assert.ok(s, "the family page has a §2 CAPABILITY section");
  return s;
};

test("the capability section leads with what survived, not what broke", () => {
  const copy = visible(capability());
  assert.doesNotMatch(copy, /measured what broke/i, "the post-mortem framing is gone");
  assert.match(copy, /6\.7|93/, "the compression ratio is the headline fact");
  // The three outcomes and their scores all survive the rewrite.
  for (const fact of [/3\s*\/\s*3/, /3\s*\/\s*4/, /0\s*\/\s*10/]) assert.match(copy, fact);
  assert.match(copy, /extraction/i);
  assert.match(copy, /explanation/i);
});

test("the compression is drawn, not just asserted", () => {
  const fig = capability();
  // A bar whose width IS the ratio: a reader sees 6.7% before reading a word.
  const pct = fig.match(/--kept:\s*([\d.]+)%/);
  assert.ok(pct, "the kept fraction is expressed as a drawn width");
  assert.ok(Math.abs(Number(pct[1]) - 6.7) < 0.2, `the bar draws the real ratio, got ${pct[1]}%`);
  // Each result carries a meter whose fill matches its own score.
  const meters = [...fig.matchAll(/data-score="(\d+)\/(\d+)"[^>]*style="--fill:\s*([\d.]+)%/g)];
  assert.equal(meters.length, 3, "each of the three results is metered");
  for (const [, got, of_, fill] of meters) {
    const expected = (Number(got) / Number(of_)) * 100;
    assert.ok(Math.abs(Number(fill) - expected) < 0.6,
      `${got}/${of_} should fill ${expected.toFixed(1)}%, drew ${fill}%`);
  }
});

test("shrinking the prose does not drop a caveat", () => {
  const copy = visible(capability());
  assert.match(copy, /uncompressed/i, "the control is still stated");
  assert.match(copy, /answered every one/i, "including that the control answered everything");
  assert.match(copy, /two points|not a curve|where on the compression/i,
    "the scope limit survives: this is two points, not a curve");
  assert.match(copy, /whether an answer arrives|not whether it is right/i,
    "and what the measurement does not cover");
  // The named model and settings stay attached to the numbers.
  assert.match(copy, /Kimi-K3/);
  assert.match(copy, /temperature 0/i);
});

// The promo strip advertised "next 1000 new accounts". The cap is real, but a visible
// counter reads as pressure, dates the offer, and would have to be kept true as the seed
// is used up. The offer stays; the countdown does not.
test("the promo strip offers the credit without advertising a remaining count", () => {
  const strip = read("index.html").match(/<aside class="promo"[\s\S]*?<\/aside>/)?.[0];
  assert.ok(strip, "the promo strip renders");
  const copy = visible(strip);
  assert.match(copy, /\$1/, "the offer itself survives");
  assert.match(copy, /free credit/i);
  assert.doesNotMatch(copy, /\b1000\b|new accounts/i, "no remaining-count claim");
  // The separator span existed only to divide the two clauses; it must not be orphaned.
  const css = readFileSync(path.join(WEB, "src", "styles", "base.css"), "utf8");
  assert.doesNotMatch(css, /\.promo__sep/, "the separator's CSS went with the clause it divided");
});

// A reader asked whether 0/10 was good or bad, which is the page's fault: the cell said
// "never reached an answer" without saying that means NO answer rather than ten wrong
// ones. In a plant those are different events, and the difference is the whole safety
// argument for shipping a small model at all.
test("the zero is explained as a non-answer, not ten wrong answers", () => {
  const copy = visible(capability());
  assert.match(copy, /no answer at all|not ten wrong/i,
    "the cell distinguishes not-answering from answering wrongly");
  assert.match(copy, /invents|confident|dangerous/i, "and says why that distinction matters");
  assert.match(copy, /Guard/, "which is what Guard exists for");
  // The scope caveat must still say what the measurement does NOT cover.
  assert.match(copy, /not whether it is right/i);
});

// The market set grew from four to six. Both pages that name it have to agree, and the
// count has to be stated correctly - "Four industries" with six articles under it is the
// kind of drift a reader notices before we do.
test("the industrial market set is consistent wherever it is named", () => {
  const MARKETS = [/oil and gas/i, /power generation/i, /manufacturing/i,
                   /aerospace/i, /mining/i, /water/i];
  const industry = read("research-industry.html");
  const grid = industry.match(/<div class="deployment-grid">[\s\S]*?<\/div>/)[0];
  const cards = [...grid.matchAll(/<article><b>([^<]+)<\/b>/g)].map((m) => m[1]);
  assert.equal(cards.length, MARKETS.length, `one card per market, found ${cards.length}`);
  for (const m of MARKETS) {
    assert.ok(cards.some((c) => m.test(c)), `the grid names ${m}`);
    assert.match(visible(read("research.html")), m, `the hub names ${m} too`);
  }
  // Every card must say what the work IS, not just name the sector.
  for (const card of grid.matchAll(/<article><b>[^<]+<\/b><p>([\s\S]*?)<\/p>/g)) {
    assert.ok(visible(card[1]).length > 40, "each market names a concrete workload");
  }
  // The stated count and the actual count cannot disagree.
  const words = { 4: /\bfour\b/i, 5: /\bfive\b/i, 6: /\bsix\b/i, 7: /\bseven\b/i };
  for (const [n, word] of Object.entries(words)) {
    if (Number(n) === cards.length) continue;
    for (const [page, html] of [["research-industry.html", industry], ["research.html", read("research.html")]]) {
      const copy = visible(html.match(/<main[\s\S]*?<\/main>/)[0]);
      assert.doesNotMatch(copy, new RegExp(`${word.source}\\s+(industries|markets)`, "i"),
        `${page} must not say ${word} when there are ${cards.length}`);
    }
  }
});

// The tiers are dictated by certification and power physics, not by preference. That is a
// far stronger claim than "we designed it this way" and it is the first thing an OT
// reviewer can check independently, so it gets its own section with the standards named.
const envelope = () => {
  const s = read("research-industry.html").match(/<section[^>]*id="envelope"[\s\S]*?<\/section>/)?.[0];
  assert.ok(s, "the industrial page explains the physical envelope");
  return s;
};

test("the envelope names the certification limits, not just the conclusion", () => {
  const copy = visible(envelope());
  assert.match(copy, /T4|135\s?&deg;C|135°C/i, "the temperature class that caps sealed compute");
  assert.match(copy, /intrinsic|Ex ia/i, "intrinsic safety");
  assert.match(copy, /1\.2|1\.3/, "and the watt ceiling it imposes");
  assert.match(copy, /purged|safe area|outside the zone|fibre|fiber/i, "the shapes that do work");
});

test("the power arithmetic is shown, because it is what decides the tier", () => {
  const copy = visible(envelope());
  assert.match(copy, /solar|battery|autonomy/i);
  assert.match(copy, /duty-cycl/i, "duty-cycled or microcontroller-class is the only solar fit");
  assert.match(copy, /panel|kWh|Wh/i, "with the arithmetic, not just the verdict");
});

test("the compliance regime is named the way a reviewer would check it", () => {
  const copy = visible(envelope());
  assert.match(copy, /62443/, "the OT security standard");
  assert.match(copy, /NERC CIP|CIP-0/i, "and the grid regime");
  assert.match(copy, /change[- ]management|change control/i,
    "the consequence that actually shapes the product: a model update is a change event");
});

test("the negative claim is attributed to our search, not asserted as universal", () => {
  const copy = visible(envelope());
  // "No hazloc-certified GPU box exists" is a negative search result. Publishing it as a
  // universal fact invites one counterexample to discredit the page.
  assert.doesNotMatch(copy, /no [^.]{0,40}(certified|hazardous)[^.]{0,20}(box|enclosure|GPU) exists/i,
    "not stated as a universal absence");
  assert.match(copy, /we have not found|we could not find|none we have found/i,
    "attributed to our own search");
});

test("the envelope concludes the tiers are forced, and cites no vendor", () => {
  const copy = visible(envelope());
  assert.match(copy, /not a preference|physics|forced|dictate/i);
  // Vendor names and prices are market intelligence with their own confidence labels;
  // this page carries standards and physics only.
  for (const vendor of [/jetson/i, /hailo/i, /qualcomm/i, /rockwell/i, /advantech/i, /\$\d/]) {
    assert.doesNotMatch(copy, vendor, `no vendor or price detail (${vendor})`);
  }
});
