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
const rows = (table) => [...table.matchAll(/<tr>(?!\s*<th scope="col")[\s\S]*?<\/tr>/g)]
  .map((m) => m[0]).filter((r) => /<th scope="row"/.test(r));

// Every slot the family actually has. A jobs row may name only these.
// SPECTRUM RENAME (founder, 2026-08-14): the ladder is Pico -> Nano -> Micro ->
// Giga -> Tera -> Peta -> Exa; the jobs table names the four deployable-on-site
// tiers, mapped 1:1 from the old vocabulary (Edge->Pico, Core->Giga).
const SLOTS = ["Pico", "Nano", "Micro", "Giga"];

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

// Founder ruling 2026-07-31: the Controlled/Scope caveats paragraph and the
// "failure worth having" paragraph came off the page. What must survive is the
// attribution: the named model and settings stay attached to the numbers.
test("the capability numbers keep their model and settings attribution", () => {
  const copy = visible(capability());
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
