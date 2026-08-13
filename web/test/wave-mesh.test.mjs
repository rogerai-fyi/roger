// Regression locks for the WAVE MESH engineering deck (Playbox MESH tab, M1).
//
// Source: UI-HANDOFF-PLAYBOX-WAVE-MESH-2026-08-12.md. The deck's whole claim is that what
// it shows is real - a committed export of the actual simulation suite, not a mock-up - so
// most of these tests are about PROVENANCE and HONESTY rather than markup. Static-content
// assertions over web/src, like playbox.test.mjs.
// Run: node --test test/wave-mesh.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "../src");
const read = (p) => readFileSync(path.join(SRC, p), "utf8");
const flat = (s) => s.replace(/\s+/g, " ");

const html = read("playbox.html");
const htmlFlat = flat(html);
const js = read("js/wave-mesh.js");
const css = read("styles/wave-mesh.css");
const catalog = JSON.parse(read("data/wave-catalog.json"));
const scene = JSON.parse(read("data/wave-scene-recorded.json"));

// ---------- the data is real, and says where it came from ---------------------

test("catalog: the snapshot carries its provenance", () => {
  const p = catalog._provenance;
  assert.ok(p, "the export must record where it came from");
  assert.equal(p.source, "wavesim.catalog()", "the source call is named");
  assert.match(p.exported, /^\d{4}-\d{2}-\d{2}$/, "the export date is recorded");
  assert.ok(/wavesim|export-wave-catalog/.test(p.note || ""), "it says how to regenerate");
});

test("catalog: the device browser has real devices to browse", () => {
  const c = catalog.catalog;
  const names = Object.keys(c);
  assert.ok(names.length >= 16, `want the full asset set, got ${names.length}`);
  // Spot-check the shape every device tile renders from.
  for (const name of names) {
    assert.ok(c[name].source, `${name} must declare simulated or live`);
    assert.ok(c[name].channels, `${name} must expose channels`);
    assert.ok(c[name].root_causes, `${name} must declare root causes`);
    assert.ok(Array.isArray(c[name].modalities), `${name} must list modalities`);
  }
  assert.equal(catalog._provenance.assets, names.length, "the provenance count must match reality");
});

// ---------- the honesty rails (handoff section 8) -----------------------------

test("honesty: the live box never carries invented sensor faults", () => {
  const live = catalog.catalog.roggentoo;
  assert.ok(live, "the live box must be in the catalogue");
  assert.equal(live.source, "live", "roggentoo is real hardware, not simulated");
  assert.deepEqual(live.sensor_faults, [],
    "truth is null on live data - the deck must never have a fault to invent");
});

test("honesty: the export refuses to overwrite a snapshot that breaks the truth-null rail", () => {
  const script = readFileSync(path.join(SRC, "../scripts/export-wave-catalog.mjs"), "utf8");
  assert.ok(script.includes("sensor_faults"),
    "the exporter must check the live box declares no faults");
  assert.ok(/refusing to overwrite|refusing/.test(script),
    "a bad export must refuse rather than replace a good snapshot");
});

test("honesty: the live device is marked as real hardware with unlabelled truth", () => {
  assert.ok(js.includes("REAL HARDWARE"), "the live tile must say it is real hardware");
  assert.ok(/unlabelled truth|no invented faults/.test(js),
    "the live tile must state that its truth is unlabelled");
  assert.ok(css.includes(".wm-dev--live"), "the live tile must be visually distinct");
});

test("honesty: the recorded bundle is labelled RECORDED and never presented as live", () => {
  assert.ok(/RECORDED/.test(scene._provenance.label), "the bundle labels itself RECORDED");
  assert.ok(/never present as live/i.test(scene._provenance.label),
    "the label states it is not live");
  assert.ok(htmlFlat.includes("wm-tag--rec"), "the wire bench carries a RECORDED tag");
  assert.ok(/no model has run/i.test(htmlFlat),
    "the page must say no model has run on it - the transport shows signal, not conclusions");
});

test("honesty: the transport describes the signal, never asserts a model verdict", () => {
  // The verdict strings must talk about samples and channels, not diagnoses. A page with no
  // model on it must not say "cavitation detected".
  const verdicts = js.slice(js.indexOf("var verdict = $(\"wmVerdict\")"));
  for (const banned of ["detected", "diagnos", "fault found", "failure predicted"]) {
    assert.ok(!new RegExp(banned, "i").test(verdicts.slice(0, 900)),
      `the transport must not claim "${banned}" - no model has run`);
  }
});

test("honesty: no benchmark number appears without a version tag", () => {
  // The handoff's numbers (30B comparisons, +13 macro, 85-89% adjudication) may only ship
  // with their suite version and R-number. None are exported yet, so none may be on the page.
  const body = htmlFlat.replace(/<!--[\s\S]*?-->/g, "");
  for (const claim of ["muse-glimmer", "IEB-Signals", "85-89", "+13 macro"]) {
    assert.ok(!body.includes(claim),
      `"${claim}" is a benchmark claim; it needs its version tag and export before it ships`);
  }
});

test("honesty: no certification we do not hold is claimed", () => {
  const body = htmlFlat.replace(/<!--[\s\S]*?-->/g, "");
  // Named standards and safety ratings only. NOT the bare word "certified": the Playbox's
  // existing vocabulary calls a replayed contract a "certified contract" - certified by our
  // own test suite - and banning that word outright would flag honest, pre-existing copy
  // while catching nothing real.
  for (const claim of ["IEC 61508", "UNS conformance", "SIL 2", "SIL 3",
                       "certified to ", "certification body", "we are certified"]) {
    assert.ok(!new RegExp(claim, "i").test(body), `the deck must never claim ${claim}`);
  }
});

test("honesty: the rack states what it is waiting for rather than faking a birth certificate", () => {
  assert.ok(/birth certificate/i.test(htmlFlat), "the rack explains what a faceplate must be");
  assert.ok(/requested|not on this deck yet/i.test(htmlFlat),
    "the missing rack must say it is pending, not pretend");
  // A digest that is not a real export must never appear.
  assert.ok(!/sha256:[0-9a-f]{8,}/i.test(htmlFlat), "no invented model digest may ship");
});

test("attribution: the Orange County line is present", () => {
  assert.ok(htmlFlat.includes("Designed by RogerAI in Orange County, California."),
    "the attribution line is non-negotiable");
});

// ---------- the wire bench ----------------------------------------------------

test("wire bench: all eight dialects are recorded, and none are identical", () => {
  const r = scene.renders;
  const want = ["signal", "modbus", "sparkplug", "prometheus", "datadog", "influx", "syslog", "opcua"];
  for (const m of want) {
    assert.ok(r[m] && r[m].length > 40, `${m} must carry real rendered output`);
  }
  // The demo's claim is that one truth reads differently in each dialect. If two matched,
  // the page would be quietly overstating the difference between wire formats.
  const seen = new Map();
  for (const [m, text] of Object.entries(r)) {
    assert.ok(!seen.has(text), `${m} and ${seen.get(text)} rendered identically`);
    seen.set(text, m);
  }
});

test("wire bench: the recorded scene is reproducible from its printed spec", () => {
  assert.ok(scene.spec, "the spec must ship with the recording");
  assert.equal(scene.spec.seed, 42, "the seed is what makes it reproducible");
  assert.ok(scene.spec.faults && scene.spec.faults.vibration,
    "the demo depends on the stuck vibration sensor");
  assert.ok(/deterministic/i.test(scene._provenance.reproduce || ""),
    "the provenance must state it is reproducible");
  assert.ok(htmlFlat.includes('id="wmSpec"'), "the page prints the spec it recorded");
});

test("transport: the steps show the stuck fault emerging over time", () => {
  const s = scene.steps;
  assert.ok(s.length >= 3, "there must be enough windows to scrub");
  // This is the whole demo: at t=0 nothing looks wrong, and the run grows until the window
  // is entirely identical samples. If this ever flattened, the scrub would show nothing.
  assert.ok(s[0].longest_run <= 2, "at the first window the channel still looks normal");
  assert.ok(s[s.length - 1].longest_run > s[0].longest_run * 4,
    "the identical-sample run must grow dramatically as the sensor sticks");
  assert.equal(s[s.length - 1].sd_tail, 0, "a fully stuck channel has zero tail deviation");
});

// ---------- structure + accessibility ----------------------------------------

test("deck: the mesh view carries the browser, the bench and the transport", () => {
  for (const id of ["wmDevices", "wmDetail", "wmDialects", "wmWire", "wmScrub", "wmVerdict"]) {
    assert.ok(htmlFlat.includes(`id="${id}"`), `the ${id} surface must exist`);
  }
  assert.ok(htmlFlat.includes('id="pgMeshView"'), "the mesh view must exist");
  assert.ok(/hidden/.test(htmlFlat.slice(htmlFlat.indexOf('id="pgMeshView"'),
    htmlFlat.indexOf('id="pgMeshView"') + 160)), "the mesh view starts hidden behind the switch");
});

test("deck: the scrub control is labelled for screen readers", () => {
  assert.ok(htmlFlat.includes('for="wmScrub"'), "the range input must have a label");
  assert.ok(css.includes(".wm-scrub__label"), "the label may be visually hidden, not absent");
  assert.ok(htmlFlat.includes('role="progressbar"'), "the run meter reports its value");
});

test("deck: it degrades honestly when the catalogue does not load", () => {
  assert.ok(js.includes("failState"), "there must be a resting state for a failed load");
  assert.ok(/did not load/i.test(js), "the failure must say so rather than showing an empty rail");
  assert.ok(/Nothing here is live/i.test(js), "a failed load must not read as 'no devices exist'");
});

test("deck: reduced motion is honoured", () => {
  assert.ok(js.includes("prefers-reduced-motion"), "the module checks the preference");
  assert.ok(css.includes("prefers-reduced-motion"), "the run bar's transition is disabled too");
});

test("deck: the catalogue is fetched same-origin, not from a third party", () => {
  const fetches = js.match(/fetch\("([^"]+)"/g) || [];
  assert.ok(fetches.length >= 2, "the deck loads its two snapshots");
  for (const f of fetches) {
    assert.ok(!/https?:\/\//.test(f), `${f} must be same-origin - the deck works with no network`);
  }
});

// ---------- the measured bundle (handoff 2) ----------------------------------

const measured = JSON.parse(read("data/wave-measured.json"));

test("measured: every figure carries the run and suite it came from", () => {
  const p = measured._provenance;
  assert.ok(p, "the bundle must record its provenance");
  assert.equal(p.suite, "IEB-Signals v1.2", "the suite version is the citation");
  for (const k of ["frames", "escalation", "records", "quants"]) {
    assert.ok(p.sources[k], `${k} must name its source file`);
  }
  // The deck names whose numbers these are; a figure with no run behind it is decoration.
  assert.ok(measured.escalation.child && measured.escalation.parent,
    "the sweep must name the child and the parent that produced it");
  assert.ok(measured.escalation.bench, "the bench must be named");
});

test("measured: the escalation sweep is a real curve, not a slogan", () => {
  const c = measured.escalation.configs;
  assert.ok(c.length >= 4, "there must be a curve to drag along");
  const childOnly = c.find((x) => x.config === "child-only");
  const direct = c.find((x) => x.config === "parent-direct");
  assert.ok(childOnly && direct, "both ends of the argument must be present");
  // The whole claim of a mesh: the parent alone is more accurate, and escalating only
  // low-margin items buys most of that back for less. If this inverted, the deck would be
  // arguing for something the measurements do not support.
  assert.ok(direct.macro_recall > childOnly.macro_recall,
    "the parent must actually be more accurate than the child alone");
  const mid = c.find((x) => /@2\.0$/.test(x.config));
  assert.ok(mid, "the recommended floor must be one of the measured configs");
  assert.ok(mid.macro_recall > childOnly.macro_recall,
    "escalating at the floor must beat the child alone");
  assert.ok(mid.pct_of_parent_everywhere < 1,
    "and it must cost less than asking the parent about everything");
});

test("measured: the per-frame redlines match the measured floors", () => {
  // R.48: A 2.0 / B 2.5 / C 2.5. These are the VU meter's redlines, so a drift here
  // would mis-draw every tile's escalation threshold.
  assert.deepEqual(
    Object.fromEntries(Object.entries(measured.frames).map(([k, v]) => [k, v.floor])),
    { A: 2.0, B: 2.5, C: 2.5 });
  for (const [name, f] of Object.entries(measured.frames)) {
    assert.ok(f.n > 0, `frame ${name} must report the n it was measured on`);
    assert.ok(f.median_margin_correct > f.median_margin_wrong,
      `frame ${name}: a correct answer must carry a wider margin than a wrong one, ` +
      "or the margin gate has nothing to gate on");
  }
});

test("measured: the records are real predictions, with truth to check them against", () => {
  assert.ok(measured.records.length >= 20, "there must be enough records to read");
  for (const r of measured.records) {
    assert.ok(r.node_id && r.truth, "each record identifies a node and its truth");
    assert.equal(typeof r.child.margin, "number", "child margins are real numbers");
    assert.equal(typeof r.parent.margin, "number", "parent margins are real numbers");
  }
  // At least one escalation must actually be overturned by the parent, or the feed would
  // show a mesh that never earns its second tier.
  const overturned = measured.records.filter((r) => r.parent.prediction !== r.child.prediction);
  assert.ok(overturned.length > 0, "the recorded run must contain real adjudications");
});

test("honesty: no evidence dict is invented for escalations", () => {
  // The frozen schema carries one; this export does not have it. A plausible-looking
  // evidence blob on a deck whose claim is "this is real" is the worst kind of lie.
  for (const r of measured.records) {
    assert.ok(!("evidence" in r), "records must not carry a fabricated evidence dict");
  }
  assert.ok(/carry no evidence dict/i.test(js),
    "the feed must say the evidence dict is absent rather than quietly omitting it");
});

test("honesty: Q8 is excluded, with the reason recorded", () => {
  // Its exported result is indistinguishable from Q4 (1199/1200 identical predictions),
  // which is not a plausible outcome for two quantizations four bits apart. Publishing it
  // would put "Q8 collapses too" on the page as a measured fact.
  const quants = measured.quants.map((q) => q.quant);
  assert.ok(!quants.some((q) => /q8/i.test(q)), "Q8 must not be published yet");
  assert.ok(measured._provenance.excluded && measured._provenance.excluded.Q8,
    "the exclusion must be recorded, not silent");
  assert.ok(/indistinguishable|identical/i.test(measured._provenance.excluded.Q8),
    "the reason must say what was wrong with it");
});

test("honesty: the quantization badges state what shrinking costs", () => {
  const f16 = measured.quants.find((q) => q.quant === "f16");
  const q4 = measured.quants.find((q) => /q4/i.test(q.quant));
  assert.ok(f16 && q4, "both the reference and the shrunk build must be shown");
  // The lesson is that this is NOT free. If a future export made them comparable, the copy
  // below would be wrong and this test should fail loudly.
  assert.ok(q4.fault_id_macro < f16.fault_id_macro * 0.6,
    "the Q4 collapse is the lesson; if it stops collapsing, rewrite the copy");
  assert.ok(js.includes("quantization-fragile"), "the deck states the lesson in words too");
});

test("feed: assert vs escalate is derived from the margin, not stored", () => {
  // A tile decides this at runtime by comparing its margin to its floor. If the deck
  // rendered a canned kind instead, moving the floor slider would be theatre.
  assert.ok(js.includes("r.child.margin < floorVal"),
    "the record's kind must be computed from its margin against the floor");
  assert.ok(js.includes("floorOf"), "the floor must be read from the selected config");
});

test("feed: the honesty lamp describes exactly what it counted", () => {
  // It is computed from the visible records. It must not be confused with the measured
  // faithfulness result, which is a different, separately cited claim.
  assert.ok(/overturned the child/.test(js), "the lamp states what it counted");
  assert.ok(/matched the truth/.test(js), "and whether those adjudications were right");
});

test("deck: the mesh bench, feed and quant panels exist", () => {
  for (const id of ["wmFloor", "wmConfig", "wmMacro", "wmEsc", "wmCost", "wmMeshVerdict",
    "wmFrames", "wmRecords", "wmLamp", "wmQuants"]) {
    assert.ok(htmlFlat.includes(`id="${id}"`), `the ${id} surface must exist`);
  }
  assert.ok(htmlFlat.includes('for="wmFloor"'), "the floor slider must be labelled");
});

test("deck: no measured number is hardcoded into the markup", () => {
  // Every figure must come from the bundle, so it moves when the measurements move.
  const body = htmlFlat.replace(/<!--[\s\S]*?-->/g, "");
  for (const n of ["72.6", "22.3", "62.3%", "43.0%", "0.999"]) {
    assert.ok(!body.includes(n), `${n} must be rendered from the measured bundle, not typed in`);
  }
});
