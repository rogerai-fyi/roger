// Regression locks for THE PATCH SHEET (Playbox MESH deck).
//
// This replaces wave-mesh.test.mjs. The deck's presentation was rebuilt as a node graph;
// every HONESTY lock from the old suite is carried forward here, because those tests are
// the only enforcement those rails have. Static-content assertions over web/src, like
// playbox.test.mjs.
// Run: node --test test/wave-patch.test.mjs
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
const js = read("js/wave-patch.js");
const css = read("styles/wave-patch.css");
const catalog = JSON.parse(read("data/wave-catalog.json"));
const scene = JSON.parse(read("data/wave-scene-recorded.json"));
const measured = JSON.parse(read("data/wave-measured.json"));

// ---------- provenance: every figure traceable to a run -----------------------

test("data: the snapshots carry their provenance", () => {
  assert.equal(catalog._provenance.source, "wavesim.catalog()");
  assert.match(catalog._provenance.exported, /^\d{4}-\d{2}-\d{2}$/);
  assert.equal(measured._provenance.suite, "IEB-Signals v1.2");
  for (const k of ["frames", "escalation", "records", "quants"]) {
    assert.ok(measured._provenance.sources[k], `${k} must name its source`);
  }
  assert.ok(measured.escalation.child && measured.escalation.parent && measured.escalation.bench,
    "the sweep must name whose numbers these are");
});

test("data: templates only name assets and root causes that exist", () => {
  // Every template is built from the catalogue at runtime. A template naming a cause the
  // catalogue does not have would render a device with no ports and no explanation.
  const cat = catalog.catalog;
  const tpl = js.match(/asset: "([a-z_0-9]+)", cause: "([a-z_0-9]+)"/g) || [];
  assert.ok(tpl.length >= 5, `want the full template shelf, found ${tpl.length}`);
  for (const m of tpl) {
    const [, asset, cause] = /asset: "([a-z_0-9]+)", cause: "([a-z_0-9]+)"/.exec(m);
    assert.ok(cat[asset], `template asset ${asset} must exist in the catalogue`);
    assert.ok(cat[asset].root_causes[cause],
      `${asset} must actually have the root cause ${cause}`);
    const moved = Object.keys(cat[asset].root_causes[cause]).length;
    assert.ok(moved >= 2 && moved < Object.keys(cat[asset].channels).length + 1,
      `${asset}/${cause} must move a readable number of channels, got ${moved}`);
  }
});

// ---------- the honesty rails (carried forward) -------------------------------

test("honesty: the live box never carries invented sensor faults", () => {
  const live = catalog.catalog.roggentoo;
  assert.equal(live.source, "live");
  assert.deepEqual(live.sensor_faults, [],
    "truth is null on live data - the deck must never have a fault to invent");
});

test("honesty: the exporter refuses a snapshot that breaks the truth-null rail", () => {
  const script = readFileSync(path.join(SRC, "../scripts/export-wave-catalog.mjs"), "utf8");
  assert.ok(script.includes("sensor_faults") && /refusing/.test(script));
});

test("honesty: the deck states it is a replay, and no model runs in the browser", () => {
  assert.ok(/REPLAY/.test(htmlFlat), "the run state must be labelled a replay");
  assert.ok(/RECORDED/.test(htmlFlat), "and recorded");
  assert.ok(/RECORDED/.test(scene._provenance.label));
  assert.ok(/never present as live/i.test(scene._provenance.label));
  // The rule that keeps the animation honest.
  assert.ok(/a packet may only move/i.test(js),
    "the module must state that a packet moves only because a record moved");
});

test("honesty: the escalation packet fires on a run, never as a stream", () => {
  // Node-RED refuses to animate messages on wires because it floods the editor; the one
  // contrib that does ships a warning. Here it is also the honesty rail: a continuous
  // animation would imply live inference.
  assert.ok(js.includes("flyPacket"), "there is a single packet animation");
  assert.ok(/if \(esc\.length && !quiet && chain\.escWire\) flyPacket\(chain\.escWire\)/.test(js),
    "it must fire only on an explicit run that actually escalated, on the wire that carried it");
  assert.ok(!/setInterval/.test(js), "nothing may animate continuously");
});

test("honesty: no evidence dict is invented for escalations", () => {
  for (const r of measured.records) assert.ok(!("evidence" in r));
  assert.ok(/carry no evidence dict/i.test(js),
    "the wire inspector must say the evidence dict is absent, not omit it silently");
});

test("honesty: the model faceplate leaves the digest empty rather than inventing one", () => {
  // A faceplate that is meant to BE a birth certificate cannot ship with a made-up hash.
  assert.ok(/pending export/.test(js), "the digest slot must read as pending");
  assert.ok(css.includes(".wp-cert__pending"), "and be styled as an absence");
  assert.ok(!/sha256[:-][0-9a-f]{8,}/i.test(htmlFlat + js),
    "no invented model digest may ship");
});

test("honesty: the retracted Q4 collapse cannot come back", () => {
  const r = measured._provenance.retracted;
  assert.match(r.status, /RETRACTED/);
  const q4 = measured.quants.find((q) => /q4/i.test(q.quant));
  assert.ok(q4.fault_id_macro > 0.6, "Q4 holds full accuracy; 0.223 was a harness bug");
  assert.ok(!/quantization-fragile/.test(js), "the retracted copy must stay gone");
});

test("honesty: identical aggregates across quantizations are refused as a harness fault", () => {
  const script = readFileSync(path.join(SRC, "../scripts/export-wave-measured.mjs"), "utf8");
  assert.ok(/JSON.stringify\(q4raw.results\) === JSON.stringify\(q8raw.results\)/.test(script));
  assert.ok(/harness/i.test(script));
});

test("honesty: the cost gauge is not allowed to read as money", () => {
  // The bundle's own cost_note says mean_params_per_item is a residency proxy. Unrendered,
  // a dial labelled "cost" turns that into a claim about dollars.
  assert.ok(measured.escalation.cost_note, "the caveat must be in the data");
  assert.ok(js.includes("wpCostNote"), "and it must be rendered");
  assert.ok(/residency proxy/.test(htmlFlat), "the gauge itself says what it measures");
  assert.ok(!/\$/.test(htmlFlat.slice(htmlFlat.indexOf("wp-gauges"), htmlFlat.indexOf("wp-gauges") + 900)),
    "no currency may appear on the gauges");
});

test("honesty: macro recall is labelled, never shown as bare 'accuracy'", () => {
  // child-only macro recall is 29.7 on the hard band. A gauge reading ACCURACY 29.7% about
  // our own model would be a self-own AND wrong about what was measured.
  assert.ok(/macro recall/i.test(htmlFlat), "the gauge names the metric it shows");
  assert.ok(js.includes("wpRaw"), "and shows raw accuracy beside it for context");
});

test("honesty: no measured number is hardcoded into the markup", () => {
  const body = htmlFlat.replace(/<!--[\s\S]*?-->/g, "");
  for (const n of ["72.6", "22.3", "62.3%", "43.0%", "29.7", "0.999"]) {
    assert.ok(!body.includes(n), `${n} must render from data, not be typed in`);
  }
});

test("attribution: the Orange County line is present", () => {
  assert.ok(htmlFlat.includes("Designed by RogerAI in Orange County, California."));
});

// ---------- the graph model ---------------------------------------------------

// AMENDED 2026-08-13 (founder direction): the ladder rotated from bottom-up to
// LEFT-TO-RIGHT - source on the left, operator on the right, like every signal chain an
// engineer already reads. The invariant is unchanged: geometry decides legality, and a
// wire can never point backwards.
test("graph: wires can only ever flow rightward up the ladder", () => {
  assert.ok(/COLS.indexOf\(to.tier\) > COLS.indexOf\(from.tier\)/.test(js),
    "connection legality must be decided by column order");
  assert.ok(/COLS = \["device", "pico", "nano", "human"\]/.test(js),
    "the ladder runs device -> pico -> nano -> human, left to right");
});

test("graph: modules snap to typed columns, never a free canvas", () => {
  // The snap preview may only ever appear in the module's own column - a pico cannot be
  // dropped in the nano column, because the column IS the type. And the slot pitch equals
  // tile + gap (n8n's invariant), so a dropped tile lands where a tidy-up would put it.
  assert.ok(/colX\(tierOf\(\)\)/.test(js), "the snap ghost is pinned to the module's own column");
  assert.ok(/SLOT_PITCH = TILE_H \+ GAP_Y/.test(js), "slot pitch = tile + gap, so tidy-up is a no-op");
  assert.ok(/pointercancel/.test(js), "drags clean up on pointercancel, where stuck-drag bugs live");
  assert.ok(/< 6\)/.test(js), "6px of drift before a press becomes a drag");
});

test("graph: added tiles outside the recorded chain never invent numbers", () => {
  // The recorded run has ONE child under ONE parent. A second pico the visitor adds must
  // say it was not part of the run, not display figures nothing measured.
  assert.ok(/not in the recorded run/.test(js),
    "a tile outside the replayed chain says so plainly");
});

test("graph: a parent takes at most one assertion input", () => {
  // Node-RED's load-bearing constraint: it makes 'what feeds this?' answerable at a glance.
  assert.ok(/w.toId === toId && w.kind === "escalate"/.test(js),
    "connecting a second escalation must replace the first");
});

test("graph: only the channels a root cause moves become ports", () => {
  // The catalogue has 95 channels. Drawing them all would make a wiring harness.
  assert.ok(/asset.root_causes\[tpl.cause\]/.test(js),
    "ports must come from the selected root cause's effects");
});

test("graph: the patch selects the measured config, so no unmeasured number can show", () => {
  assert.ok(/child\+parent@/.test(js), "the topology maps onto a measured config name");
  const names = measured.escalation.configs.map((c) => c.config);
  assert.ok(names.includes("child-only") && names.includes("parent-direct"),
    "both ends of the measured sweep must exist to map onto");
});

// ---------- interaction -------------------------------------------------------

test("interaction: connecting is click-to-connect, not HTML5 drag and drop", () => {
  // HTML5 DnD is not supported on touch at all, and a drag onto a 13px port is occluded by
  // the finger doing it. Click-to-connect is also the keyboard path, so there is one
  // state machine instead of three.
  assert.ok(js.includes("armPort") && js.includes("tryConnect"));
  assert.ok(!/dragstart|dataTransfer/.test(js), "no HTML5 drag and drop");
  assert.ok(/Escape/.test(js), "Escape must cancel an armed wire");
});

test("interaction: compatible ports are shown, not remembered", () => {
  assert.ok(js.includes("markCompatible"), "arming a wire must mark where it can land");
  assert.ok(css.includes(".wp-port.is-ok") && css.includes(".wp-port.is-no"),
    "compatible and incompatible ports must look different");
});

test("interaction: ports are distinguished by shape and fill, never by hue", () => {
  // 13 quantity kinds is a paint chart, not a colour code - and the palette spends its one
  // red on the live glint.
  assert.ok(css.includes(".wp-port--chan { border-radius: 50%; }"), "a channel is round");
  assert.ok(css.includes(".wp-port--rec  { border-radius: 2px; }"), "a record is square");
  assert.ok(/\.wp-port--out, \.wp-port--up \{ background: var\(--ink-700\); \}/.test(css),
    "an output is filled with ink, not a colour");
});

test("interaction: the escalation wire is dotted, so the exception path is visible", () => {
  assert.ok(/\.wp-wire--escalate .wp-wire__line \{ stroke-dasharray/.test(css));
});

test("interaction: wires are hit-testable with a fat invisible path", () => {
  // stroke-opacity 0 (not stroke:none) so visibleStroke still hit-tests it.
  assert.ok(/stroke-opacity: 0; stroke-width: 20/.test(css), "20px hit target");
  assert.ok(/pointer: coarse\) \{ \.wp-wire__hit \{ stroke-width: 32/.test(css),
    "wider on touch, where 20px is marginal");
});

// ---------- the palette -------------------------------------------------------

test("palette: only tokens that exist are referenced", () => {
  // The previous stylesheet referenced --red, --rule and --amber, none of which exist;
  // every reference silently fell back to a literal, and --amber reintroduced a hue the
  // system deliberately removed.
  const tokens = read("styles/tokens.css");
  const used = new Set((css.match(/var\(--[a-z0-9-]+/g) || []).map((v) => v.slice(4)));
  for (const name of used) {
    assert.ok(tokens.includes(`${name}:`), `${name} must be a real token`);
  }
});

test("palette: red is a signal, never a surface", () => {
  // It may light an armed port, an escalation, focus, and the RUN button. Nothing else.
  const reds = (css.match(/var\(--live\)/g) || []).length;
  assert.ok(reds > 0 && reds < 22, `red is used ${reds} times; it must stay a glint`);
  // Enumerate every rule that FILLS with red and check the set, rather than guessing at
  // size. Exactly two may: the 1px floor tick on a VU, and the one primary action.
  const filled = [];
  for (const block of css.split("}")) {
    if (/background:\s*var\(--live\)/.test(block)) {
      const sel = (block.split("{")[0] || "").trim().split("\n").pop().trim();
      filled.push(sel);
    }
  }
  assert.deepEqual(filled.sort(), [".wp-run", ".wp-vu__red"].sort(),
    `only the floor tick and the RUN button may be filled red; got ${filled.join(", ")}`);
});

// ---------- accessibility -----------------------------------------------------

test("a11y: the graph is keyboard operable with the same state machine", () => {
  assert.ok(/e.key === "c" \|\| e.key === "C"/.test(js), "C arms a wire from the focused tile");
  assert.ok(/e.key === "Enter" && PATCH.armed/.test(js), "Enter connects");
  assert.ok(/e.key.indexOf\("Arrow"\) === 0/.test(js), "arrows move between tiles");
  assert.ok(js.includes("moveFocus"), "and focus follows the ladder");
});

test("a11y: there is a list mirror of the graph, always in the DOM", () => {
  assert.ok(htmlFlat.includes('id="wpMirror"'), "the mirror element exists");
  assert.ok(js.includes("renderMirror"), "and is kept in sync");
  assert.ok(/View this patch as a list/i.test(htmlFlat), "and is reachable");
});

test("a11y: tiles are described, and status is announced", () => {
  assert.ok(js.includes('setAttribute("aria-label", describe(t))'), "each tile is described");
  assert.ok(js.includes("describe"), "the description names tier, floor and status");
  assert.ok(htmlFlat.includes('aria-live="polite"'), "connection outcomes are announced");
  assert.ok(!/role="application"/.test(htmlFlat), "never role=application - it kills browse mode");
});

test("a11y: reduced motion is honoured", () => {
  assert.ok(css.includes("prefers-reduced-motion"), "animations have a static equivalent");
  assert.ok(/@media \(prefers-reduced-motion: reduce\) \{ \.wp-packet \{ display: none/.test(css),
    "the packet does not fly for people who asked for less motion");
  assert.ok(js.includes("REDUCED"), "and the module checks the preference too");
});

// ---------- structure + offline -----------------------------------------------

test("deck: the patch sheet and the results half both exist", () => {
  for (const id of ["wpShelf", "wpSheet", "wpWires", "wpRun", "wpScope", "wpFeed", "wpInspect"]) {
    assert.ok(htmlFlat.includes(`id="${id}"`), `${id} must exist`);
  }
});

test("deck: the data is fetched same-origin, so the deck works offline", () => {
  const fetches = js.match(/fetch\("([^"]+)"/g) || [];
  assert.ok(fetches.length >= 3, "the deck loads its snapshots");
  for (const f of fetches) assert.ok(!/https?:\/\//.test(f), `${f} must be same-origin`);
  assert.ok(js.includes("did not load"), "and says so honestly when they do not");
});

test("deck: the wire inspector only shows bytes for the scene that has them", () => {
  // Only the pump/cavitation/seed-42 scene has committed renders. Showing another
  // template's cable with those bytes would attribute one device's data to another.
  assert.ok(/PATCH.template.asset === sc.asset_type/.test(js),
    "the inspector must check the loaded template matches the recorded scene");
});
