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
