import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (name) => readFileSync(path.join(DIST, name), "utf8");
const visibleText = (s) => s.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();
const text = (name) => read(name).replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// research.html is now a hub; the model catalogue lives on research-models.html.
// `surface` is for assertions that are true of the research area as a whole and
// should not care which of the two pages carries the markup.
// The research AREA, not one page. The hub stays inside its conciseness budget by
// pushing detail to siblings, so an assertion about "the research surface" must span
// them - otherwise splitting a page silently drops the guarantee with it.
const PAGES = ["research.html", "research-models.html", "research-industry.html"];
const surface = () => PAGES.map(read).join("\n");
const surfaceText = () => PAGES.map(text).join(" ");


test("Research is a concise first-class destination", () => {
  const page = read("research.html");
  assert.match(page, /<title>RogerAI Research/);
  assert.match(page, /RogerAI Labs/);
  assert.match(page, /aria-current="page"/);
  assert.match(page, /rel="canonical" href="https:\/\/rogerai\.fm\/research\.html"/);
  const main = page.match(/<main[\s\S]*?<\/main>/)?.[0] || "";
  // Budget raised 18000 -> 19500 on founder direction: the industry section now names
  // the four focus markets (oil and gas, power generation, manufacturing, aerospace)
  // and the plant-interface standards an OT buyer checks for. That is the substance a
  // grant or enterprise reviewer came for, so it earns its bytes - but the ceiling stays
  // low on purpose. If a change needs more room, cut something before raising this.
  //
  // The CEILING HAS NOT MOVED; what it measures has. This budget is about how much a
  // reader has to get through, and counting raw markup charged SVG coordinate data as
  // if it were prose - the scope's plot geometry is ~13KB of path arcs that nobody
  // reads. So the conciseness budget now measures the page with the instruments
  // collapsed, and a separate, looser ceiling keeps total page weight honest so this
  // cannot become a loophole for dumping unbounded SVG.
  const collapsed = main.replace(/<svg[\s\S]*?<\/svg>/g, "<svg/>");
  assert.ok(collapsed.length < 19500, `research content is concise (${collapsed.length} bytes of prose)`);
  assert.ok(main.length < 40000, `research page stays light (${main.length} bytes total)`);
});

// The mission family the founder asked for, in RogerAI's voice rather than a
// borrowed one. Each line is a commitment somebody could hold us to, which is why
// they are asserted: a mood survives a copy edit, a commitment should not.
test("the lab says why it exists, in terms it can be held to", () => {
  const why = read("research.html").match(/<section class="section" id="why"[\s\S]*?<\/section>/)?.[0] || "";
  assert.ok(why, "the why section exists");
  const copy = why.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");
  assert.match(copy, /Engineers for engineers/i);
  assert.match(copy, /Built in America, open to everyone/i);
  assert.match(copy, /Orange County/);
  // The two claims that are checkable rather than aspirational.
  assert.match(copy, /published as it happens|weights, recipes, raw evaluations/i);
  assert.match(copy, /nothing we publish needs us to keep existing/i, "the no-lock-in promise");
  // It must not drift into claiming a scale or a customer we do not have.
  assert.doesNotMatch(copy, /\b\d+\+? (employees|customers|partners)\b/i);
});

test("the page leads with the work, not a biography of the company", () => {
  // Strip comments first: this asserts what a VISITOR reads. A build note explaining
  // that "an OT security team asks this" is not the page describing its own staff, and
  // a source comment should never be able to fail a content assertion.
  const page = read("research.html").replace(/<!--[\s\S]*?-->/g, "");
  assert.match(page, /Models built for local constraints/i);
  assert.match(page, /smaller|less memory|local hardware/i);
  assert.doesNotMatch(page, /team|founders?|venture|employees|our origins/i);
});

test("the model list gives every program a reason and honest status", () => {
  const page = text("research-models.html");
  // SPECTRUM RENAME (2026-08-14): the Wave ladder is Pico -> Exa.
  for (const model of [
    "DeepSeek-V4-Flash MTP",
    "Kimi-K3",
    "Wave Pico",
    "Wave Nano",
    "Wave Micro",
    "Wave Giga",
    "Wave Tera",
    "Wave Peta",
    "Wave Exa",
  ]) assert.match(page, new RegExp(model));
  for (const reason of [
    /practical local inference/i,
    /high-memory workstation/i,
    /reads one machine&rsquo;s telemetry|telemetry and asserts/i,
    /fleet gateway/i,
    /site brain/i,
    /full-plant reasoning/i,
    /expert-pruned/i,
    /the teacher the\s+whole family learns from/i,
  ]) assert.match(page, reason);
  assert.match(page, /Research build/i);
  assert.match(page, /Planned/i);
  assert.match(page, /Trained/i);
  // The fabricated artifact id is gone. Naming-CONVENTION placeholders on design
  // rows (wave-core-1b-instruct, wave-nano-<size>-<task>) are fine - they document
  // the id scheme, they are not download claims.
  assert.doesNotMatch(page, /wave-micro-350m-instruct/i);
});

test("the model scope plots parameter class as radar range on a true log axis", () => {
  const page = surface();
  const css = read("styles/research.css");
  const scope = page.match(/<figure class="scope"[\s\S]*?<\/figure>/)?.[0];
  assert.ok(scope, "the Wave scope exists");

  const order = ["Wave Pico", "Wave Nano", "Wave Micro", "Wave Giga"];
  let cursor = -1;
  for (const name of order) {
    const next = scope.indexOf(name);
    assert.ok(next > cursor, `${name} follows the smaller class`);
    cursor = next;
  }

  // The proof that the axis is really logarithmic: consecutive DECADE rings must be
  // equally spaced in radius. On a linear axis they would not be, and the plot would
  // be implying a proportion the five-order-of-magnitude family does not have.
  // Centre is read from the grid rather than hardcoded, so re-scaling the viewBox does
  // not silently disable this check the way a literal cx="200" did.
  const rings = [...scope.matchAll(/<circle cx="([\d.]+)" cy="([\d.]+)" r="([\d.]+)"\/>/g)];
  assert.ok(rings.length >= 5, `expected decade rings, found ${rings.length}`);
  const [cx, cy] = [Number(rings[0][1]), Number(rings[0][2])];
  const radii = rings.map((m) => Number(m[3]));
  const gaps = radii.slice(1).map((r, i) => r - radii[i]);
  const spread = Math.max(...gaps) - Math.min(...gaps);
  assert.ok(spread <= 1, `decade rings are evenly spaced (gaps ${gaps.map((g) => g.toFixed(1))})`);
  // The rim is where the spokes end, not the outermost decade ring: a 1B-class band is
  // centred ON the 1B ring, so half of it legitimately sits outside that ring.
  const spokes = [...scope.matchAll(/<line x1="[\d.]+" y1="[\d.]+" x2="([\d.]+)" y2="([\d.]+)"\/>/g)];
  assert.ok(spokes.length >= 6, "the bearing spokes are drawn");
  const rim = Math.max(...spokes.map((m) => Math.hypot(Number(m[1]) - cx, Number(m[2]) - cy)));
  assert.ok(rim > Math.max(...radii), "the plot extends past its outermost decade ring");

  // Every slot is an annular arc: one cell per variation it hosts, at its range band.
  // Each cell must stay inside the rim, and each slot must draw at least one.
  const varAxis = scope.match(/<g class="scope__contacts" data-axis="variations">[\s\S]*?\n          <\/g>/)[0];
  const contacts = [...varAxis.matchAll(/<g class="scope__contact" data-slot="([^"]+)">([\s\S]*?)<\/g>/g)];
  assert.equal(contacts.length, order.length, "every slot draws a contact on the capability axis");
  for (const [, slot, body] of contacts) {
    const cells = [...body.matchAll(/<path class="scope__cell" d="M([\d.]+) ([\d.]+)/g)];
    assert.ok(cells.length >= 1, `${slot} draws at least one variation cell`);
    for (const c of cells) {
      const r = Math.hypot(Number(c[1]) - cx, Number(c[2]) - cy);
      assert.ok(r <= rim + 1, `${slot} stays inside the scope rim (${r.toFixed(1)} > ${rim})`);
    }
  }

  for (const tick of ["100K", "1M", "10M", "100M", "1B"]) {
    assert.ok(scope.includes(`>${tick}<`), `range ring is labelled at ${tick}`);
  }
  // Bearing MEANS something now, so the axis must be named on the instrument - that is
  // what let the caption drop from five sentences to two.
  for (const bearing of ["GUARD", "AUDIO", "VISION", "TEXT", "EMBED", "TOOL"]) {
    assert.ok(scope.includes(`>${bearing}<`), `the ${bearing} bearing is labelled`);
  }

  const prose = scope.replace(/\s+/g, " ");
  assert.doesNotMatch(prose, /[Bb]earing carries no meaning/,
    "bearing carries data now, so the old disclaimer would be false");
  assert.match(prose, /declared design target/i);
  assert.match(prose, /no public checkpoint is released/i);
  assert.doesNotMatch(scope, /\b\d+(\.\d+)?\s?(GB|MB|tok\/s|ms)\b/, "no invented footprint or speed");

  assert.match(page, /RogerAI-designed open model program/i);
  assert.match(page, /release gate|no (?:public )?checkpoint/i, "the scope states program status, not availability");
  assert.doesNotMatch(page, /Wave Edge/i);
  for (const selector of [".scope", ".scope__cell", ".model-group-head"]) {
    assert.match(css, new RegExp(selector.replace(".", "\\.")), `${selector} is styled`);
  }
  // Drawn is the DEFAULT state; the sweep and reveal are both reduced-motion safe.
  assert.match(css, /prefers-reduced-motion[\s\S]*scope__sweep/i);
});

test("model identity follows family size variant conventions", () => {
  const page = read("research-models.html");
  // SPECTRUM RENAME (2026-08-14): ids follow the pico->exa ladder.
  for (const id of [
    "wave-pico-&lt;size&gt;-&lt;task&gt;",
    "wave-nano-&lt;size&gt;-&lt;task&gt;",
    "wave-giga-&lt;size&gt;-instruct",
  ]) assert.match(page, new RegExp(id));
  // The "Naming contract" paragraph came off the page on founder direction
  // (2026-07-31, noise reduction); the ids above still pin the convention.
});

test("upstream optimization keeps visible lineage and license", () => {
  const page = read("research-models.html");
  assert.match(page, /Optimized by RogerAI/);
  assert.match(page, /Upstream: DeepSeek-V4-Flash/);
  assert.match(page, /Upstream: Kimi-K3/);
  assert.match(page, /MIT License/i);
  assert.match(page, /Kimi K3 License/i);
  assert.match(page, /284B total[^<]*13B active/i);
  assert.match(page, /2\.8T total[^<]*104B active/i);
  assert.doesNotMatch(page, /RogerAI (?:pre)?trained (?:DeepSeek|Kimi)/i);
});

test("evidence and local-use promises stay explicit", () => {
  const page = text("research.html");
  assert.match(page, /hardware, artifact, runtime, settings, and raw results/i);
  assert.match(page, /Negative results remain available/i);
  assert.match(page, /download and run qualifying artifacts locally/i);
  assert.match(page, /network is optional/i);
  const html = read("research.html");
  assert.match(html, /href="\/broadcasts\.html"/);
  assert.match(html, /href="https:\/\/huggingface\.co\/rogerai-fyi"/);
  assert.match(html, /href="https:\/\/github\.com\/rogerai-fyi\/roger"/);
});

test("only released artifacts present download controls", () => {
  const page = read("research-models.html");
  assert.match(page, /href="https:\/\/huggingface\.co\/rogerai-fyi\/DeepSeek-V4-Flash-MTP-GGUF"/);
  // Exactly one download control: the DeepSeek research build, which is public.
  // Wave slots have nothing to download, so they present none.
  assert.equal((page.match(/class="model-download"/g) || []).length, 1);
  assert.doesNotMatch(page, /coming soon/i);
  assert.doesNotMatch(page, /aria-disabled="true"/);
  for (const stage of [
    /16K saliency calibration/,
    /Certifying the waypoint/,
    /Architecture and data design/,
    /Scratch build queued/,
  ]) assert.match(page, stage);
  assert.doesNotMatch(page, /href="[^"]*(?:placeholder|coming-soon)[^"]*"/i);
});

test("Wave Micro publishes its program status instead of a release contract", () => {
  const page = read("research-models.html");
  const card = page.match(/<article class="model-row"[^>]*id="wave-micro"[\s\S]*?<\/article>/)?.[0] || "";
  assert.ok(card, "the Wave Micro row exists");
  const cardText = card.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

  // Nothing that implies a shipping artifact.
  for (const claim of [/AVAILABLE/, /wave-micro-350m-instruct/, /v1\.0/, /GGUF/, /Q4_K_M/]) {
    assert.doesNotMatch(cardText, claim);
  }
  assert.doesNotMatch(card, /class="model-download"/);
  assert.doesNotMatch(card, /huggingface\.co\/rogerai-fyi\/wave-/);

  // The catalogue entry was trimmed to its ROLE so it matches every other row - the
  // programme detail (bake-off state, decision rule, licence intent) is not what a reader
  // scanning a model list came for. What must survive is the status a reader could be
  // misled about, and it does: the chip, the id, and no artifact link or evidence fields.
  // SPECTRUM RENAME (2026-08-14): Micro is now the 7-8B base+specialize site
  // tier - no trained-artifact claim, its honest status is the selected base.
  assert.match(cardText, /BASE SELECTED/i);
  assert.match(cardText, /pipeline standing up/i);
  assert.match(cardText, /no checkpoint released/i);
  // No placeholder evidence: the earlier version listed five fields that all deferred to
  // a model card that did not exist, which looked like rigour and was not.
  for (const field of [/peak memory/i, /measured speed/i, /tested hardware/i]) {
    assert.doesNotMatch(cardText, field, "no evidence field without a checkpoint to measure");
  }
  // The licence boundary is stated once for every Wave artifact rather than per entry.
  assert.match(text("research-models.html"), /separate network services under separate terms/i);
  assert.match(text("research-models.html"), /never require RogerAI or its broker/i);
});

test("developers have a compact path from artifact to local inference", () => {
  const page = read("research.html");
  assert.match(page, /id="developers"/);
  // Download -> run -> broadcast. The third step closes the loop into the network
  // instead of restating the evaluation contract, which §4 already carries.
  assert.match(page, /Download/);
  assert.match(page, /Run locally/);
  assert.match(page, /Broadcast/);
  assert.match(page, /huggingface-cli download/);
  assert.match(page, /llama-server/);
  assert.match(page, /OpenAI-compatible endpoint/i);
  assert.match(page, /roger share/);
  // Going on air stays optional - the local path must never read as requiring it.
  assert.match(page, /Optional/i);
});

test("research and the live network directory remain distinct", () => {
  const page = read("research.html");
  assert.match(page, /href="\/models\.html"/);
  assert.match(page, /live network models/i);
  assert.match(page, /research programs/i);
});

test("company handoff resolves to concise industry deployment patterns", () => {
  const page = surface();
  assert.match(page, /<section[^>]+id="industry"/);
  // The founder-named focus markets, in the founder's own terms.
  for (const market of [
    /oil and gas/i,
    /power generation/i,
    /manufacturing/i,
    /aerospace/i,
  ]) assert.match(page, market);
  // The wording moved (the old headline named no constraint), but the two claims it
  // carried must survive any rewrite: these are patterns rather than customers, and the
  // model advises without entering the control loop. wave-family.test.mjs pins the rest.
  assert.match(page, /NDA|not named here|not customer/i);
  assert.doesNotMatch(page, /customers (it|we) do(es)? not have|no customers/i,
    "confidential is not the same as nonexistent");
  assert.match(page, /advis/i);
  assert.match(page, /control loop|closed-loop control/i);
});

// An industrial buyer and a grant reviewer both check the same thing first: does
// this vendor speak the plant's language, and does it know where its box is
// allowed to sit? Naming the standards is the cheapest, highest-signal proof that
// the work is grounded in operational technology rather than in a demo.
test("the industrial interface is described in the plant's own standards", () => {
  const copy = surfaceText();
  for (const standard of [
    /OPC UA/,             // tags
    /Modbus/,             // brownfield fieldbus
    /Sparkplug B/,        // MQTT payload spec behind the unified namespace
    /ISA-95/,             // asset hierarchy
    /NE 107/,             // NAMUR device-health status
    /IEC 62443/,          // zones and conduits
    /Purdue/,             // network level placement
  ]) assert.match(copy, standard, `names ${standard}`);
  // The one architectural promise an OT security team asks for by name.
  assert.match(copy, /outbound/i);
  assert.match(copy, /no inbound|without.{0,20}inbound|exposes no inbound/i);
});

test("the industrial pitch states what the model must never touch", () => {
  const copy = surfaceText();
  assert.match(copy, /protection|interlock|safety-instrumented/i);
  assert.match(copy, /deterministic/i);
  assert.match(copy, /beside|alongside/i, "positioned next to classical analytics, not replacing it");
});

// Splitting a page is how a guarantee quietly disappears: the assertions stay pointed
// at the hub while the content moves to a sibling nobody added to the surface. These
// pin the industrial page itself.
test("the industrial page carries the placement diagram and its boundaries", () => {
  const page = read("research-industry.html");
  const copy = page.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

  // Where the box sits - the first question an OT reviewer asks.
  assert.match(page, /<figure class="purdue"/, "the placement diagram is here, not on the hub");
  for (const lv of ["L3.5", "L3", "L2", "L1", "L0"]) {
    assert.ok(copy.includes(lv), `Purdue level ${lv} is drawn`);
  }
  assert.match(copy, /outbound only/i);
  assert.match(copy, /never touched/i, "the safety path is drawn as untouched");

  // The boundary section exists to stop an advisory system being trusted past its remit.
  assert.match(copy, /closed-loop/i);
  assert.match(copy, /interlocks/i);
  assert.match(copy, /deterministic/i);

  // Headings must not restate the hero - the split lifted a section that already had one.
  const h1 = page.match(/<h1[^>]*>([\s\S]*?)<\/h1>/)?.[1]?.trim();
  const h2s = [...page.matchAll(/<h2[^>]*>([\s\S]*?)<\/h2>/g)].map((m) => m[1].trim());
  assert.ok(h1, "the page has a headline");
  assert.ok(!h2s.includes(h1), `no section repeats the hero headline (${h1})`);

  // Section numbers must be contiguous from 1 - lifting a section carried its old number.
  const nums = [...page.matchAll(/sectionno">§(\d+)/g)].map((m) => +m[1]);
  assert.deepEqual(nums, nums.map((_, i) => i + 1), `sections number from 1 (got ${nums.join(",")})`);
});

test("the hub points at the industrial page rather than dropping it", () => {
  assert.match(read("research.html"), /href="\/research-industry\.html"/);
});

test("device and roadmap claims remain gated", () => {
  const page = surface();
  const copy = surfaceText();
  assert.match(page, /Raspberry Pi/i);
  assert.match(page, /ESP32/i);
  assert.match(copy, /exact board.*runtime.*format.*quantization/i);
  // Stronger than the old 'does not run Wave Micro': NO Wave tier fits an ESP32.
  assert.match(copy, /No Wave tier runs on an ESP32/i);
  assert.match(copy, /full delegation.*partial delegation.*CPU fallback/i);
  // The Wave Tool / Vision / Audio roadmap block was retired on founder direction.
  // Those modalities are now absent entirely, so the honest invariant is that they
  // are never NAMED as things you could have - not that a gating sentence exists.
  for (const unshipped of ["Wave Tool", "Wave Vision", "Wave Audio"]) {
    assert.doesNotMatch(page, new RegExp(unshipped), `${unshipped} is not advertised`);
  }

  // ROGER EDGE (2026-08-17). The founder asked for the microcontroller line to appear
  // beside the Wave catalogue, AND for the material a hobbyist or a professional would
  // use to wire a small board to a model. None of the second half exists: no Roger Edge
  // model is trained, and no library, board package or specification is published. So the
  // section is written as intent, and the failure it invites is the next edit quietly
  // promoting that intent into something a reader believes they can go and get. The
  // guarantee this test already carried - a device claim is gated by what was measured -
  // is the same guarantee, re-anchored to the line that has measured nothing at all.
  const edge = read("research-models.html").match(/<section[^>]+id="roger-edge"[\s\S]*?<\/section>/)?.[0] || "";
  assert.ok(edge, "the Roger Edge section is on the catalogue page");
  const edgeCopy = edge.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");
  assert.match(edgeCopy, /nothing trained/i, "the status says no model exists");
  /* AMENDED 2026-08-18: the status paragraph used to enumerate what does not
     exist ("no Roger Edge model, no library and nothing to download yet");
     the founder replaced it with a prototype-phase note. The guarantee -
     that a reader can never think there is something here to go and get -
     is unchanged and now rests on three things asserted together: the
     section says nothing is trained (above), it declares itself prototype,
     and it offers no download control or install path (below). */
  assert.match(edgeCopy, /in prototype/i, "the line declares itself unfinished");
  assert.match(edgeCopy, /No Wave tier runs on an ESP32 or an Arduino/i);
  /* AMENDED 2026-08-18: these pinned two HEADINGS that defined Roger Edge by
     what it is not ("Not a Wave tier", "Wave does not depend on it"). The
     founder asked for the section to say what the line IS, so the copy is
     positive now. The GUARANTEE is unchanged and still asserted, just on the
     sentences that carry it: it must read as a CLASSIFIER rather than a
     language model (so it can never be mistaken for an eighth Wave tier),
     and a Wave model must still be described as running WITH OR WITHOUT
     one (so Wave never reads as needing it). */
  assert.match(edgeCopy, /classifier with a fixed set of labels/i,
    "Roger Edge reads as a task model, never as a smaller Wave tier");
  assert.match(edgeCopy, /Roger Edge detects; Wave reasons/i,
    "and the division of labour is stated");
  assert.match(edgeCopy, /with or without a board like this/i,
    "a Wave model runs whether or not one is present");
  // Nothing to download, and no install path for a thing that does not exist.
  assert.doesNotMatch(edge, /class="model-download"/);
  assert.doesNotMatch(edgeCopy, /\b(SDK|pip install|arduino-cli|PlatformIO)\b/i,
    "no install path is offered for an unbuilt line");
});

test("services are optional and preserve local model independence", () => {
  const page = read("research.html");
  assert.match(page, /id="services"/);
  for (const service of [
    /optimization engineering/i,
    /edge deployment/i,
    /benchmarking/i,
    /industrial pilots/i,
  ]) assert.match(page, service);
  assert.match(page, /never requires buying services/i);
  assert.match(page, /services never require the network/i);
  assert.match(page, /mailto:labs@rogerai\.fm/);
  // The contact affordance must LOOK actionable: a .research-button, not a link
  // buried at the end of a prose sentence (a reader missed it as a link).
  const services = page.match(/id="services"[\s\S]*?<\/section>/)[0];
  assert.match(services, /<a class="research-button[^"]*" href="mailto:labs@rogerai\.fm">/,
    "the services contact is a visible button");
});

test("new institutional pages contain no em dash character", () => {
  for (const name of ["research.html", "company.html", "index.html", "careers.html"]) {
    assert.doesNotMatch(read(name), /—/, `${name} contains an em dash`);
  }
});

// The style rule is about the writing, not the file extension, and specs are writing we
// ship. This guard only scanned web/src, so features/ drifted: careers.feature opened with
// an em dash and nothing objected.
test("the specs under features/web contain no em dash character either", () => {
  const dir = path.join(WEB, "..", "features", "web");
  const specs = readdirSync(dir).filter((f) => f.endsWith(".feature"));
  assert.ok(specs.length > 0, "there are feature files to check");
  for (const name of specs) {
    assert.doesNotMatch(
      readFileSync(path.join(dir, name), "utf8"), /—/,
      `features/web/${name} contains an em dash`,
    );
  }
});

// SVG has no z-index: source order IS paint order. The sweep was drawn after the contacts
// and painted over them, so the blips the figure exists to show were invisible under it.
// Geometry assertions cannot see this, which is how it shipped.
test("the radar sweep is painted before the contacts it must not cover", () => {
  const page = read("research.html");
  const sweep = page.indexOf("scope__sweep");
  const contacts = page.indexOf("scope__contacts");
  assert.ok(sweep >= 0 && contacts >= 0, "both layers are present");
  assert.ok(sweep < contacts, "the sweep must come first in source, or it paints over the blips");
});

// ---- the scope is one object: plot + legend, linked both ways ----------------
// Rendered verification found these working in Chromium; these assertions keep them
// from silently regressing, since the offline suite cannot drive a pointer.
test("every legend row is a real control, so the link is not pointer-only", () => {
  const scope = surface().match(/<figure class="scope"[\s\S]*?<\/figure>/)[0];
  const rows = [...scope.matchAll(/<li class="scope__row[^"]*" data-slot="([^"]+)">([\s\S]*?)<\/li>/g)];
  assert.equal(rows.length, 4, "one row per slot");
  const contacts = [...scope.matchAll(/<g class="scope__contact" data-slot="([^"]+)">/g)].map((m) => m[1]);
  for (const [, slot, body] of rows) {
    assert.ok(contacts.includes(slot), `${slot} row pairs with a contact of the same slot`);
    const btn = body.match(/<button[^>]*class="scope__pick"[^>]*>/)?.[0];
    assert.ok(btn, `${slot} row is a button, so it is focusable and clickable`);
    assert.match(btn, /type="button"/, "never submits");
    assert.match(btn, /aria-pressed="false"/, "selection state starts off and is exposed");
  }
});

test("the link is driven from focus as well as hover, and can be released", () => {
  const js = read("js/scope.js");
  for (const evt of ["mouseenter", "mouseleave", "focus", "blur", "click"]) {
    assert.match(js, new RegExp(`addEventListener\\("${evt}"`), `the scope handles ${evt}`);
  }
  assert.match(js, /aria-pressed/, "selection is announced, not just painted");
  assert.match(js, /Escape/, "a pinned slot can be released from the keyboard");
});

test("the scope stays readable with no script and with motion off", () => {
  const js = read("js/scope.js");
  const css = read("styles/research.css");
  // The drawn state is the DEFAULT: only the script hides anything, so JS-off is complete.
  assert.match(js, /classList\.add\("is-pending"\)/, "pending is added by script, never authored");
  assert.match(css, /\.scope\.is-pending .scope__contact \{ opacity: 0; \}/);
  // Reduced motion must stop the sweep AND leave every arc at full opacity.
  const reduced = css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/g)
    ?.find((b) => b.includes(".scope__sweep"));
  assert.ok(reduced, "the scope has a reduced-motion block");
  assert.match(reduced, /\.scope__sweep \{ animation: none/);
  assert.match(reduced, /animation: none; opacity: 1/, "arcs stay drawn when motion is off");
});

// The plot draws exactly what the family page's variation table says, so the two cannot
// drift. This is the assertion that makes bearing trustworthy rather than decorative.
test("each slot's arc spans exactly the variations the family page gives it", () => {
  const scope = surface().match(/<figure class="scope"[\s\S]*?<\/figure>/)[0];
  const family = read("research-wave-family.html");
  const variations = family.match(/<section[^>]*id="variations"[\s\S]*?<\/section>/)[0];

  // "Lives at" column -> which slots host each variation.
  const livesAt = {};
  for (const row of variations.matchAll(/<tr><th scope="row">([^<]+)<\/th>[\s\S]*?<td class="tier-cell">([^<]+)<\/td>/g)) {
    livesAt[row[1].trim()] = row[2].trim();
  }
  assert.ok(Object.keys(livesAt).length >= 6, "the variation table was found");

  const ORDER = ["Guard", "Audio", "Vision", "Text", "Embed", "Tool"]; // bearing order, north-clockwise
  const SLOTS = ["Wave Pico", "Wave Nano", "Wave Micro", "Wave Giga"];
  const expands = (livesAtText) => {
    if (/every slot/i.test(livesAtText)) return new Set(SLOTS);
    const short = (s) => SLOTS.find((n) => n.endsWith(s) || n === s);
    if (/and up/i.test(livesAtText)) {
      const from = SLOTS.indexOf(short(livesAtText.replace(/\s*and up/i, "").trim()));
      return new Set(SLOTS.slice(from));
    }
    if (/\bto\b/i.test(livesAtText)) {
      const [a, z] = livesAtText.split(/\s+to\s+/i).map((s) => SLOTS.indexOf(short(s.trim())));
      return new Set(SLOTS.slice(a, z + 1));
    }
    return new Set(livesAtText.split(/\s*,\s*/).map((s) => short(s.trim())).filter(Boolean));
  };

  for (const slot of SLOTS) {
    const expected = ORDER.filter((v) => expands(livesAt[v]).has(slot));
    const slug = slot.toLowerCase().replace(/\s+/g, "-");
    const axis = scope.match(/<g class="scope__contacts" data-axis="variations">[\s\S]*?\n          <\/g>/)[0];
    const g = axis.match(new RegExp(`<g class="scope__contact" data-slot="${slug}">([\\s\\S]*?)</g>`))[1];
    const drawn = (g.match(/<path class="scope__cell"/g) || []).length;
    assert.equal(drawn, expected.length,
      `${slot} draws ${drawn} cells but the variation table gives it ${expected.length} (${expected.join(", ")})`);
  }
});

// ---- the plant-jobs axis ----------------------------------------------------
// Bearing means two different things depending on the toggle, so the same drift risk
// applies twice: the jobs axis must match the family page's JOBS table exactly, the way
// the capability axis matches the variations table.
const scopeFig = () => surface().match(/<figure class="scope"[\s\S]*?<\/figure>/)[0];
const axisGroup = (fig, axis) =>
  fig.match(new RegExp(`<g class="scope__contacts" data-axis="${axis}">[\\s\\S]*?\\n          </g>`))[0];

test("the scope offers both axes and defaults to the capability one", () => {
  const fig = scopeFig();
  assert.match(fig, /data-mode="variations"/, "the served default is the capability axis");
  const modes = [...fig.matchAll(/<button[^>]*class="scope__mode"[^>]*data-mode="([^"]+)"[^>]*>/g)]
    .map((m) => m[0]);
  assert.equal(modes.length, 2, "two axis buttons");
  assert.equal(modes.filter((m) => /aria-pressed="true"/.test(m)).length, 1,
    "exactly one is pressed at rest");
  // Both axes are SERVED, so the toggle only flips visibility - nothing is re-rendered.
  for (const axis of ["variations", "jobs"]) {
    assert.ok(axisGroup(fig, axis), `${axis} contacts are in the markup`);
    assert.match(fig, new RegExp(`<g class="scope__bearings[^"]*" data-axis="${axis}"`),
      `${axis} labels are in the markup`);
  }
});

test("each slot's job arc matches the family page's jobs table", () => {
  const jobsSection = read("research-wave-family.html")
    .match(/<section[^>]*id="jobs"[\s\S]*?<\/section>/)[0];
  const SLOTS = ["Pico", "Nano", "Micro", "Giga"];
  const expand = (cell) => {
    if (/\bto\b/i.test(cell)) {
      const [a, z] = cell.split(/\s+to\s+/i).map((x) => SLOTS.indexOf(x.trim()));
      return new Set(SLOTS.slice(a, z + 1));
    }
    return new Set(cell.split(/\s*(?:\+|and|,)\s*/).map((x) => x.trim()).filter(Boolean));
  };
  // Count, per slot, how many jobs the TABLE says it takes part in.
  const expected = Object.fromEntries(SLOTS.map((s) => [s, 0]));
  let jobs = 0;
  for (const row of jobsSection.matchAll(/<tr><th scope="row">[^<]+<\/th><td class="tier-cell">([^<]+)<\/td>/g)) {
    jobs++;
    for (const slot of expand(row[1])) {
      assert.ok(slot in expected, `jobs table names a real slot, got "${slot}"`);
      expected[slot]++;
    }
  }
  assert.ok(jobs >= 16, `the table still carries the full job set, found ${jobs}`);

  const axis = axisGroup(scopeFig(), "jobs");
  for (const [short, slug] of [["Pico", "wave-pico"], ["Nano", "wave-nano"],
                               ["Micro", "wave-micro"], ["Giga", "wave-giga"]]) {
    const g = axis.match(new RegExp(`<g class="scope__contact" data-slot="${slug}">([\\s\\S]*?)</g>`))[1];
    const drawn = (g.match(/<path class="scope__cell"/g) || []).length;
    assert.equal(drawn, expected[short],
      `${slug} draws ${drawn} job cells but the jobs table gives it ${expected[short]}`);
  }
});

test("the jobs axis says what a shared job means, and does not overclaim", () => {
  const fig = scopeFig();
  const caption = visibleText(fig);
  assert.match(caption, /takes part in|does not mean it carries that job alone/i,
    "a slot on a job is participation, not sole ownership - several jobs are pipelines");
  // The axis legend has to change with the axis, or the picture is mislabelled.
  assert.match(fig, /<span data-axis="jobs">[\s\S]*?plant job/i);
  assert.match(fig, /<span data-axis="variations">[\s\S]*?capability variation/i);
});

test("the axis toggle is hidden when it would not work", () => {
  const css = read("styles/research.css");
  // Two dead buttons is worse than one axis. The control appears only once html.js is set.
  assert.match(css, /\.scope__modes \{ display: none; \}/, "hidden by default");
  assert.match(css, /html\.js \.scope__modes \{[^}]*display: inline-flex/, "shown only with script");
  // And the axis the no-script reader gets must be the one the markup declares.
  assert.match(scopeFig(), /data-mode="variations"/);
});

test("the axis buttons name the two modes a reader is choosing between", () => {
  const fig = scopeFig();
  const labels = [...fig.matchAll(/<button[^>]*class="scope__mode"[^>]*>([^<]+)<\/button>/g)]
    .map((m) => m[1].trim());
  assert.deepEqual(labels, ["Capability", "Industrial"],
    "the pair reads as two kinds of axis, not one axis and one unit");
});

// Every onward row is a three-across grid. The deployment row shipped with one card in
// it, which reads as a broken grid rather than a deliberate single destination - and it
// under-sold a section that now has three real places to go.
test("each onward row offers a full set of destinations that resolve", () => {
  const page = read("research.html");
  const rows = [...page.matchAll(/<div class="research-onward">([\s\S]*?)<\/div>\s*<\/div>/g)];
  assert.ok(rows.length >= 2, `the hub has more than one onward row, found ${rows.length}`);
  for (const [, row] of rows) {
    const cards = [...row.matchAll(/<a href="([^"]+)">\s*<b>([^<]+)<\/b>\s*<span>([\s\S]*?)<\/span>/g)];
    assert.equal(cards.length, 3, `each row fills the grid, found ${cards.length}`);
    for (const [, href, title, blurb] of cards) {
      assert.ok(title.trim().length > 0, "the card is named");
      assert.ok(visibleText(blurb).length > 40, `${title} says what is there, not just where`);
      // Deep links have to land on a section that exists, or the card is a dead end.
      const [file, anchor] = href.replace(/^\//, "").split("#");
      const target = read(file);
      if (anchor) assert.match(target, new RegExp(`id="${anchor}"`), `${href} resolves to a real section`);
    }
  }
});

// The sweep pivot must equal the scope centre. It was left at 220,220 when the viewBox
// grew to 480 for the jobs axis, and the beam swept around a point 20px off-centre. This
// is the same class of bug as the mascot's stale transform-origin: invisible in a static
// build, obvious the moment it moves. Read the centre from the geometry, never a literal.
test("the radar sweep pivots on the scope centre", () => {
  const scope = scopeFig();
  const ring = scope.match(/<circle cx="([\d.]+)" cy="([\d.]+)" r="[\d.]+"\/>/);
  assert.ok(ring, "the grid rings give the centre");
  const [cx, cy] = [Number(ring[1]), Number(ring[2])];
  const origin = read("styles/research.css")
    .match(/\.scope__sweep \{[^}]*transform-origin:\s*([\d.]+)px\s+([\d.]+)px/);
  assert.ok(origin, ".scope__sweep declares a transform-origin");
  assert.equal(Number(origin[1]), cx, "pivot x is the scope centre");
  assert.equal(Number(origin[2]), cy, "pivot y is the scope centre");
  // The beam itself has to start there too, or it sweeps a wedge detached from the origin.
  const beam = scope.match(/<line x1="([\d.]+)" y1="([\d.]+)" x2="[\d.]+" y2="[\d.]+"\/>\s*<\/g>/);
  if (beam) {
    assert.equal(Number(beam[1]), cx, "the beam starts at the centre");
    assert.equal(Number(beam[2]), cy, "the beam starts at the centre");
  }
});
