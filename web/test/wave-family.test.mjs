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
import { readFileSync, readdirSync } from "node:fs";
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
/* AMENDED 2026-08-17: this matched a BARE <tr> only. §4 now carries two row forms -
   the 27 plant jobs stay attribute-free because research-page.test.mjs counts them out
   of this table and pins them to the scope figure's job arcs, while rows beyond the
   plant set (the workshop bench, the top of the ladder) carry data-scope. A regex that
   reads only the bare form would have let every new row into the page unchecked, which
   is the opposite of what these assertions are for. It reads both forms now, so "every
   job names a real slot", "no job twice" and the clinical sweep cover the whole table. */
const rows = (table) => [...table.matchAll(/<tr\b[^>]*>(?!\s*<th scope="col")[\s\S]*?<\/tr>/g)]
  .map((m) => m[0]).filter((r) => /<th scope="row"/.test(r));

// Every slot the family actually has. A jobs row may name only these.
// SPECTRUM RENAME (founder, 2026-08-14): the ladder is Pico -> Nano -> Micro ->
// Giga -> Tera -> Peta -> Exa; the jobs table names the four deployable-on-site
// tiers, mapped 1:1 from the old vocabulary (Edge->Pico, Core->Giga).
/* AMENDED 2026-08-17: the list was the four deployable-on-site tiers, because §4 only
   ever named those four. The guarantee is "a job may point only at a slot the family
   actually HAS", and the family has seven - so the list is now the locked ladder in
   full. §4 covers the whole of it (founder: it thinned out above Micro), and a typo or
   an invented tier is still caught, which is the point. */
const SLOTS = ["Pico", "Nano", "Micro", "Giga", "Tera", "Peta", "Exa"];

test("the jobs table covers the plant broadly, not as a token gesture", () => {
  const table = jobsTable();
  const jobs = rows(table);
  assert.ok(jobs.length >= 16,
    `the jobs table is the breadth claim for the whole programme, found ${jobs.length} rows`);
  /* AMENDED 2026-08-17: §4 gained a filter toolbar. The breadth claim is only a claim
     if a reader can SEE it, so the same guarantee now covers the JS-off page: every row
     is served in the markup (asserted above, which reads dist/ HTML, not a rendered
     DOM), and the toolbar ships hidden so nothing is filtered away by a script that may
     never run. If a future edit moves rows into JavaScript, this fails. */
  const section = read("research-wave-family.html").match(/<section[^>]*id="jobs"[\s\S]*?<\/section>/)[0];
  assert.doesNotMatch(section, /<script/i, "no inline script builds the table");
  assert.match(section, /<div class="wj__bar"[^>]*\bhidden\b/,
    "the toolbar is served hidden, so a reader without JS sees the whole table");
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

/* FOUNDER RULING 2026-08-17: §2 CAPABILITY is off this page entirely. It was the
   expert-pruning experiment - a frontier model cut to 60 of its 896 parts, the ratio
   drawn as a bar, three score meters, and the pump line - and the founder's words on
   the whole block were "lets not add this ... it doesn't make any sense with what we
   are doing right there, that was an old test." So the section, the figure and every
   sentence built on it are gone, and the remaining sections renumbered (§3 SLOTS ->
   §2, and so on down to Wave Infinite at §5).

   THIS IS A RE-ANCHORING, NOT A DELETION. Four assertions lived here:
     "leads with what survived, not what broke", "the compression is drawn, not just
     asserted", "the numbers keep their model and settings attribution", and "the zero
     is explained as a non-answer".
   The first, second and fourth were about the shape of a figure that no longer exists,
   and there is nothing honest left for them to hold. The THIRD one's guarantee outlives
   its subject and is the reason this block still exists: a borrowed model's numbers may
   never appear on this page stripped of the model that produced them and the settings
   they were produced under. That is now stated as a conditional - it binds whatever
   borrowed figure lands here next - and it is joined by the assertion that the retired
   experiment does not creep back, which is the founder's ruling made executable.

   Kimi-K3 still appears on research-models.html, which is a different page with its own
   lock in research-page.test.mjs. Nothing here touches it. */
const family = () => read("research-wave-family.html");

test("the retired pruning experiment stays off the family page", () => {
  const page = family();
  const copy = visible(page.match(/<main[\s\S]*?<\/main>/)[0]);
  assert.doesNotMatch(copy, /Kimi-K3/,
    "the borrowed pruning model is not named on this page any more");
  assert.doesNotMatch(page, /class="capability/,
    "and the figure it was drawn in is gone, not merely emptied");
  for (const relic of [/896/, /60 of \d+ experts/i, /route coverage/i, /0\s*\/\s*10/]) {
    assert.doesNotMatch(copy, relic, `the retired experiment must not come back (${relic})`);
  }
  // The section numbering has to close over the gap, or the page cites a §2 it lost.
  const nos = [...page.matchAll(/class="sectionno">&sect;(\d+) \/ ([A-Z ]+)</g)].map((n) => n[1]);
  assert.deepEqual(nos, ["1", "2", "3", "4", "5"], "the sections are numbered without a hole");
  assert.doesNotMatch(copy, /Section 2 says/i, "and nothing still points at the section that left");
});

test("any borrowed-model figure on this page carries its model and settings", () => {
  /* The guarantee the attribution test was written for, kept alive past its subject:
     if a number measured on somebody else's model appears here, the model and the
     settings it was measured under travel with it. Today no such figure exists, and
     the assertion says so in a way that will bind the next one that does. */
  const copy = visible(family().match(/<main[\s\S]*?<\/main>/)[0]);
  const MODELS = /\b(Kimi[- ]?K\d|Qwen|Llama|Nemotron|Mistral|DeepSeek|GPT-\d|Gemini|Claude)\b/i;
  if (MODELS.test(copy)) {
    const named = copy.match(MODELS)[0];
    assert.match(copy, /temperature\s*\d/i,
      `${named} is named with numbers attached, so the settings must be named too`);
  }
  // The one comparative claim we are allowed stays qualitative and stays cited.
  const bench = family().match(/<aside class="wf-bench"[\s\S]*?<\/aside>/)?.[0];
  if (bench) {
    const said = visible(bench);
    assert.match(said, /30B-class open models/, "the claim names the roster it is about");
    assert.match(said, /near chance/i, "and stays qualitative");
    assert.match(said, /No frontier or chat-tuned model has ever been measured there/i,
      "and bounds itself to what was actually put on the bench");
    assert.match(said, /IEB-Signals public-release plan, 2026-08-14/,
      "and carries its citation");
    assert.doesNotMatch(said, /\b1[34]\.\d\b/, "no embargoed IEB figure is printed");
  }
  assert.equal((family().match(/class="wf-bench"/g) || []).length <= 1, true,
    "the comparative claim appears at most once on the page");
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

// The market set grew four -> six -> eight (healthcare and defense sustainment, founder-
// approved 2026-08-01). Both pages that name it have to agree, and the count has to be
// stated correctly - "Four industries" with eight articles under it is the kind of drift a
// reader notices before we do.
// Founder-approved 2026-08-01 (features/web/healthcare_defense_markets.feature). These
// two boundaries are the whole basis on which we may claim healthcare and defense at all:
// healthcare is the EQUIPMENT AND FACILITIES layer (clinical work is FDA-regulated Software
// as a Medical Device), and defense is SUSTAINMENT (no effects). If a future edit softens
// or drops them, that is a regulated-claims problem, not a copy problem.
test("the healthcare and defense boundaries are stated where the markets are claimed", () => {
  const industry = visible(read("research-industry.html"));
  assert.match(industry, /equipment and facilities, not patients/i,
    "the healthcare boundary must be stated on the page that claims healthcare");
  for (const forbidden of [/do not diagnose/i, /do not treat/i, /do not read scans/i]) {
    assert.match(industry, forbidden, `the boundary must say it ${forbidden}`);
  }
  assert.match(industry, /never a decision about a live alarm/i,
    "alarm work must be scoped to offline journal review, not bedside decisions");
  assert.match(industry, /sustainment, not effects/i, "the defense boundary must be stated");
  assert.match(industry, /no targeting/i, "defense must disclaim targeting explicitly");
  // and the company page cannot claim healthcare without the same scope
  assert.match(visible(read("company.html")), /equipment and facilities layer/i,
    "the company page's industry line must carry the healthcare scope too");
});

test("no job in the table describes clinical work", () => {
  const table = jobsTable();
  for (const row of rows(table)) {
    const text = visible(row);
    for (const clinical of [/\bdiagnos/i, /\btreatment\b/i, /\bdosing\b/i,
                            /\bpatient (triage|acuity)\b/i, /read(ing)? a scan/i,
                            /\bwaveform\b/i, /\becg\b/i]) {
      assert.doesNotMatch(text, clinical,
        `a job row strays into clinical territory (${clinical}): ${text.slice(0, 70)}`);
    }
  }
});

test("the industrial market set is consistent wherever it is named", () => {
  const MARKETS = [/oil and gas/i, /power generation/i, /manufacturing/i,
                   /aerospace/i, /mining/i, /water/i, /healthcare/i, /defense/i];
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
  const words = { 4: /\bfour\b/i, 5: /\bfive\b/i, 6: /\bsix\b/i, 7: /\bseven\b/i,
                  8: /\beight\b/i, 9: /\bnine\b/i };
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

// Founder ruling 2026-07-31: the tiers-argument and compliance-regime paragraphs came
// off the envelope (noise reduction). IEC 62443 stays named in the Placement figure,
// which the surface-standards test still asserts.

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

// .device-contract lead-ins are block-level, so whatever follows starts a new line. That
// is only safe while every card is written as "<b>Lead-in.</b> Capitalised sentence" - the
// careers strip already shipped a line beginning with a comma once, and this is the same
// trap in a different component. Swept across every page that uses the card.
test("no card lead-in leaves its sentence starting with punctuation", () => {
  let checked = 0;
  for (const page of readdirSync(DIST).filter((f) => f.endsWith(".html"))) {
    const html = read(page);
    for (const block of html.matchAll(/<div class="device-contract">[\s\S]*?<\/div>/g)) {
      checked++;
      // Only the first <b> of a card is the block lead-in; a later one is inline emphasis
      // mid-sentence, whose continuation legitimately starts lowercase.
      for (const card of block[0].matchAll(/<p>\s*<b>[^<]*<\/b>([^<]*)/g)) {
        const after = card[1];
        assert.doesNotMatch(after, /^\s*[,;:]/,
          `${page}: a card sentence begins "${after.trim().slice(0, 44)}"`);
        if (after.trim()) {
          assert.match(after.trim(), /^[A-Z(&"']/,
            `${page}: a card sentence should start a new sentence, got "${after.trim().slice(0, 44)}"`);
        }
      }
    }
  }
  assert.ok(checked >= 3, `the card is used on more than one page, swept ${checked}`);
});

// The wave mark, taken from the homepage social card and put on the page the family is
// named after. What makes it this mark and not a lookalike: two S-curves on one
// centreline, and the beacon exactly where they cross. Those are the things asserted -
// a redrawn approximation would drift, and the point is that the card and the page carry
// the same object.
test("the Wave family page carries the wave mark", () => {
  const page = read("research-wave-family.html");
  const mark = page.match(/<figure class="wave-mark"[\s\S]*?<\/figure>/)?.[0];
  assert.ok(mark, "the family page draws the wave mark");
  assert.match(mark, /role="img"/, "it is named for a screen reader");
  assert.match(mark, /aria-label="[^"]+"/);

  /* 2026-08-17: the mark gained a SPECTRUM behind it - harmonics at 2x/3x/4x,
     the ladder drawn as wavelengths. The guarantee is unchanged and now
     stronger: the two PRINCIPAL waves still cross at the beacon, and every
     harmonic is a harmonic of the same fundamental, so each has a node at
     that same crossing - the animation cannot move the point the mark is
     about. Harmonics are asserted separately below. */
  const waves = [...mark.matchAll(/<path class="wave-mark__wave wave-mark__wave--(?:wide|live)"[^>]*d="([^"]+)"/g)].map((m) => m[1]);
  assert.equal(waves.length, 2, `two principal waves, one behind the other, found ${waves.length}`);
  const harmonics = [...mark.matchAll(/wave-mark__wave--h(\d)"[^>]*d="M0 (\d+)/g)];
  assert.equal(harmonics.length, 3, "three harmonics stand behind them");
  assert.ok(harmonics.every((h) => h[2] === "78"), "every harmonic rests on the same centreline");

  // Both run on the same centreline and share the same midpoint - that shared point is
  // where they cross, and it is the only place the beacon may sit.
  const shape = waves.map((d) => d.match(/^M0 (\d+)C[\d ]+ (\d+) (\d+)S[\d ]+ (\d+) (\d+)$/));
  assert.ok(shape.every(Boolean), "both waves are S-curves in the card's form");
  const [a, b] = shape;
  assert.equal(a[1], b[1], "both start on the same centreline");
  assert.equal(a[5], b[5], "and end on it");
  assert.equal(a[2] + "," + a[3], b[2] + "," + b[3], "and cross at the same point");

  const node = mark.match(/<circle class="wave-mark__node"[^>]*cx="([\d.]+)"[^>]*cy="([\d.]+)"/);
  assert.ok(node, "there is a beacon node");
  assert.equal(node[1], a[2], "the beacon sits where the waves cross, horizontally");
  assert.equal(node[2], a[3], "and vertically");
  // And the pulse pivots on the beacon, or it drifts off the crossing as it breathes.
  const css = readFileSync(path.join(WEB, "src", "styles", "wave-family.css"), "utf8");
  const origin = css.match(/\.wave-mark__node \{[^}]*transform-origin:\s*([\d.]+)px\s+([\d.]+)px/);
  assert.ok(origin, "the beacon declares a transform-origin");
  assert.deepEqual([origin[1], origin[2]], [a[2], a[3]], "it pivots on the crossing point");
});

test("the wave mark is whole without motion", () => {
  const css = readFileSync(path.join(WEB, "src", "styles", "wave-family.css"), "utf8");
  const block = css.slice(css.indexOf(".wave-mark"));
  assert.ok(block.length, "the mark is styled");
  // Only the beacon animates, and only its scale: the curves are the mark and must not
  // move. Nothing may be hidden at rest.
  /* 2026-08-17: the curves are now animated by SCRIPT (js/wave-mark.js), which
     recomputes their `d` from standing-wave math with the resting frame equal
     to this markup. The CSS guarantee stands as written - no CSS animation
     smears the curves, and reduced-motion users never load the script, so the
     page they see is this exact static mark. */
  assert.doesNotMatch(block, /\.wave-mark__wave[^{]*\{[^}]*animation:/, "no CSS animation moves the curves");
  assert.doesNotMatch(block, /\.wave-mark__(wave|node)[^{]*\{[^}]*opacity:\s*0\s*[;}]/,
    "nothing is hidden at rest");
  /* the animation is opt-out, not opt-in: the script bails on reduced-motion,
     so what remains must be the whole mark */
  const js = readFileSync(path.join(WEB, "src", "js", "wave-mark.js"), "utf8");
  assert.match(js, /prefers-reduced-motion: reduce/, "the script asks before it moves anything");
  assert.match(js, /if \(reduce\) return;/, "and does nothing at all when asked not to");
  const reduced = (css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/g) || [])
    .find((b) => b.includes("wave-mark"));
  assert.ok(reduced, "the mark has a reduced-motion escape");
  assert.match(reduced, /animation: none/);
});

// Same trap as the action buttons and the signal path: a shared component defined in a
// page-specific sheet. .wf-back lived in wave-family.css, which research-industry.html
// does not load, so its back link shipped unstyled. Assert the definition is somewhere
// every page using it actually loads.
test("the back-to-Labs link is styled on every page that has one", () => {
  const srcDir = path.join(WEB, "src");
  const pages = readdirSync(srcDir)
    .filter((f) => f.endsWith(".html") && readFileSync(path.join(srcDir, f), "utf8").includes("wf-back"));
  assert.ok(pages.length >= 2, `the link is shared, found it on ${pages.length} pages`);
  const research = readFileSync(path.join(srcDir, "styles", "research.css"), "utf8");
  assert.match(research, /\.wf-back \{/, "defined in the sheet every research page loads");
  for (const p of pages) {
    const line = readFileSync(path.join(WEB, "build.mjs"), "utf8")
      .split("\n").find((l) => l.includes(`"${p}":`)) || "";
    assert.match(line, /research\.css/, `${p} loads the sheet that defines it (bundle: ${line.trim()})`);
  }
  // It is navigation above the callsign, not part of it.
  assert.match(research, /\.wf-back \{[^}]*display: block/, "it sits on its own line");
});

test("the beacon pulses about itself, on any page that carries the mark", () => {
  /* 2026-08-18: the slim mark on the Labs page had its dot walking right and
     down as it breathed. An SVG transform scales about the USER-SPACE ORIGIN,
     not the element's middle, and the transform-origin that fixes it lived in
     wave-family.css - which the Labs page does not load. The component sets
     it itself now, so the beacon stays on the crossing the mark is about
     wherever the mark is dropped. */
  const js = readFileSync(path.join(WEB, "src", "js", "wave-mark-spectrum.js"), "utf8");
  assert.match(js, /node\.style\.transformOrigin = CX \+ "px " \+ CY \+ "px"/,
    "the mark pins its own transform-origin to the crossing point");
});

test("text painted with a gradient can never render invisible", () => {
  /* 2026-08-18. Two headings use background-clip:text with color:transparent -
     the homepage's "Tune in" and this page's "grows". In a browser that does
     not support the clip, that combination renders the text INVISIBLE, and on
     the homepage that is the site's first sentence. Every stylesheet that
     clips text to a gradient must carry an @supports fallback restoring a
     real colour. This scans, so a third one cannot ship without the guard. */
  const dir = path.join(WEB, "src", "styles");
  for (const file of readdirSync(dir).filter((f) => f.endsWith(".css"))) {
    const css = readFileSync(path.join(dir, file), "utf8");
    if (!/background-clip:\s*text/.test(css)) continue;
    assert.match(css, /@supports not \(\(background-clip: text\)/,
      `${file} clips text to a gradient, so it needs the @supports fallback`);
    const guard = css.slice(css.indexOf("@supports not ((background-clip: text)"));
    assert.match(guard.slice(0, 400), /color:\s*(inherit|var\(--[\w-]+\))/,
      `${file}'s fallback must restore a real colour, not leave it transparent`);
  }
});
