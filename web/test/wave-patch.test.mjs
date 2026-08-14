// Regression locks for THE SIGNAL BENCH (Playbox MESH deck).
//
// REBUILT 2026-08-13 (founder direction, third revision): a wall of recorded sensors
// with ON/OFF levers and condition dials, a radio that seats ANY Wave family model,
// and a console that prints what the model SAID. Every HONESTY lock from the previous
// suites is carried forward - those tests are the only enforcement the rails have -
// and the derivation is EXECUTED under a stub DOM, not just grepped.
// Run: node --test test/wave-patch.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, statSync } from "node:fs";
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

// ---------- the module, EXECUTED under a stub DOM ------------------------------

function loadHook() {
  const stubEl = () => ({ addEventListener() {}, setAttribute() {}, style: {}, classList: { add() {}, remove() {}, toggle() {} }, appendChild() {}, textContent: "", hidden: true, focus() {}, dataset: {}, closest: () => null, title: "" });
  const sandbox = {
    window: { matchMedia: () => ({ matches: true }), localStorage: { getItem: () => null, setItem() {} }, location: { hash: "" }, addEventListener() {} },
    document: {
      readyState: "complete", addEventListener() {}, removeEventListener() {},
      getElementById: () => null, querySelector: () => null, querySelectorAll: () => [],
      createElement: stubEl, createElementNS: stubEl, body: { classList: { add() {}, remove() {} }, appendChild() {} },
      activeElement: null,
    },
    fetch: () => new Promise(() => {}),
    setTimeout: () => 0,
    requestAnimationFrame: () => 0,
  };
  sandbox.window.document = sandbox.document;
  const fn = new Function("window", "document", "fetch", "setTimeout", "requestAnimationFrame",
    js + "; return window.__wavePatchTest;");
  const hook = fn(sandbox.window, sandbox.document, sandbox.fetch, sandbox.setTimeout, sandbox.requestAnimationFrame);
  hook.setScene(scene, catalog);
  hook.setMeasured(measured);
  hook.buildSensors();
  return hook;
}

const fam = (h, id) => h.family.find((f) => f.id === id);

// ---------- provenance: every figure traceable to a run ------------------------

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

test("data: the provenance line is rendered, not just committed", () => {
  assert.ok(htmlFlat.includes('id="wpProv"'), "the bar carries a provenance line");
  assert.ok(/recount of these records/.test(js),
    "and it states that every reading is a recount of the records");
});

// ---------- the honesty rails (carried forward) ---------------------------------

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
  assert.ok(/recount of real records|recount of these records/i.test(js + htmlFlat),
    "the module states that readings are recounts of records");
  assert.ok(!/setInterval/.test(js), "nothing may animate continuously");
});

test("honesty: no evidence dict is invented for escalations", () => {
  for (const r of measured.records) assert.ok(!("evidence" in r));
});

test("honesty: the model faceplate leaves the digest empty rather than inventing one", () => {
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

test("honesty: compute never reads as money, and names itself a residency proxy", () => {
  assert.ok(measured.escalation.cost_note, "the caveat must be in the data");
  assert.ok(/residency proxy/.test(js), "the knob tooltip says what the compute figure measures");
  const tipBlock = js.slice(js.indexOf("function knobTip"), js.indexOf("function knobTip") + 900);
  assert.ok(!/\$\s?\d|USD|dollar/i.test(tipBlock), "no currency near the measured figures");
});

test("honesty: macro recall is named as the metric, never bare 'accuracy'", () => {
  assert.ok(/macro recall/i.test(js), "the knob tooltip names the metric it shows");
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

// ---------- the exported windows: tags, units, bounds, the frame -----------------

test("data: records carry the window the model actually read", () => {
  const withW = measured.records.filter((r) => r.window);
  assert.ok(withW.length === measured.records.length,
    "every record joins its bench row - tags and bounds are data, not decoration");
  for (const r of withW) {
    assert.ok(r.window.tag, "a recorded tag");
    assert.ok(r.window.body.includes("range=["), "and the literal window body");
    assert.ok(r.window.lo <= r.window.mean && r.window.mean <= r.window.hi,
      "bounds parsed from the body must contain the mean");
    assert.ok(r.window.body.includes(String(r.window.tag)),
      "parsed fields must come FROM the body, never beside it");
  }
});

test("data: a unit the wire did not state is null, never defaulted", () => {
  const unknown = measured.records.filter((r) => r.window && r.window.unit === null);
  assert.ok(unknown.length > 0, "the bench really does contain unit-less wires");
  assert.ok(/unit not stated/.test(js), "and the bay says so instead of guessing");
  const exp = readFileSync(path.join(SRC, "../scripts/export-wave-measured.mjs"), "utf8");
  assert.ok(/unit === "\?" \? null : unit/.test(exp.replace(/\s+/g, " ")) || /"\?" means the wire did not state a unit/.test(exp),
    "the exporter documents the rule");
});

test("data: the task frame is exported verbatim, retiring the placeholder", () => {
  assert.ok(measured.task_frame && measured.task_frame.length > 100,
    "the T01 frame ships with the bundle");
  assert.ok(/sensor-interpretation unit/.test(measured.task_frame),
    "and it is the real frame, not a summary");
  // the envelope now carries it
  const h = loadHook();
  const env = h.envelopeFor({ modality: "raw numbers", channels: [], body: "1, 2, 3" });
  assert.ok(env.includes("task frame - exported verbatim"), "the envelope names its source");
  assert.ok(env.includes(measured.task_frame.slice(0, 60)), "and carries the frame itself");
  assert.ok(!/frame text — pending export/.test(env),
    "the pending-export placeholder is retired from the envelope");
});

test("bays: the log window is byte-for-byte the recorded input body", () => {
  assert.ok(/w.body \|\|/.test(js) || js.includes("ws-log\", w.body"),
    "the log renders window.body, not a rendering of it");
  assert.ok(/byte-for-byte/.test(js), "and says so");
  assert.ok(/was not exported - the log is absent, not invented/.test(js),
    "a missing window is an absence, never a synthesis");
});

test("bays: the live pulse is chrome, and says REPLAY on its face", () => {
  assert.ok(/MONITORING · REPLAY/.test(js), "the REC chip is labelled a replay");
  assert.ok(/is CHROME/.test(css), "the css says the pulse claims nothing");
  assert.ok(/prefers-reduced-motion: reduce\) \{ \.ws-rec\.is-live \.ws-rec__dot \{ animation: none/.test(css),
    "and it stops for people who asked for less motion");
});

// ---------- the sensor wall: recorded conditions only ----------------------------

test("wall: sensors are built FROM the records - one per recorded channel", () => {
  const h = loadHook();
  const channels = new Set(measured.records.map((r) => r.node_id.slice(-3)));
  assert.equal(h.state.sensors.length, channels.size,
    "one sensor per recorded channel, no invented sensors");
  for (const s of h.state.sensors) assert.ok(channels.has(s.ch));
});

test("wall: a dial's positions are exactly the recorded conditions for its channel", () => {
  const h = loadHook();
  for (const s of h.state.sensors) {
    const truths = new Set(measured.records
      .filter((r) => r.node_id.slice(-3) === s.ch).map((r) => r.truth));
    assert.deepEqual(new Set(s.conds), truths,
      `sensor ${s.ch}: the dial physically cannot ask for an unrecorded condition`);
    assert.equal(s.conds[0], "none", "the healthy position comes first on every dial");
  }
});

test("wall: a dial position always replays the SAME recorded instance", () => {
  const h = loadHook();
  for (const s of h.state.sensors) {
    for (const c of s.conds) {
      const i = s.recIdx[c];
      const r = measured.records[i];
      assert.equal(r.truth, c, "the pick carries the dialed truth");
      assert.equal(r.node_id.slice(-3), s.ch, "and belongs to this channel");
      const first = measured.records.findIndex(
        (x) => x.node_id.slice(-3) === s.ch && x.truth === c);
      assert.equal(i, first, "deterministic: the FIRST record with that truth, always");
    }
  }
});

// ---------- the radio: the whole family, honestly -------------------------------

test("rack: the attach menu carries the WHOLE Wave family with honest statuses", () => {
  const h = loadHook();
  const ids = h.family.map((f) => f.id);
  for (const want of ["pico", "nano", "edge", "micro", "core", "station", "satellite"]) {
    assert.ok(ids.includes(want), `${want} must be on the shelf`);
  }
  const recorded = h.family.filter((f) => f.status === "recorded").map((f) => f.id).sort();
  assert.deepEqual(recorded, ["nano", "pico"],
    "exactly two slots have a recorded run on this bench - claiming more would be a lie");
});

test("rack: an unrecorded model attaches, but never speaks a number", () => {
  const h = loadHook();
  h.state.sensors.forEach((s) => { s.model = null; });
  h.state.sensors[0].model = "micro";
  h.state.sensors[0].on = true;
  h.derive();
  assert.equal(h.state.verdict.state, "off", "the lamp refuses to glow for it");
  assert.ok(/no recorded run/.test(h.state.verdict.why), "and says why");
  const rd = h.readOf(h.state.sensors[0]);
  assert.ok(rd.nodata, "a read from an unrecorded slot carries NO prediction and NO margin");
  assert.ok(!("said" in rd) || rd.said == null);
  // the family entry itself says what it will do
  assert.ok(/will attach silent/.test(js), "the menu warns before you attach");
});

test("rack: pico reads are the recorded child fields, nano reads the recorded parent", () => {
  const h = loadHook();
  const s = h.state.sensors[0];
  s.on = true; s.cond = s.conds[1]; // a recorded fault
  const r = measured.records[s.recIdx[s.cond]];

  s.model = "pico"; s.floor = 0.5; h.state.senior = false;
  const rd = h.readOf(s);
  if (r.child.margin >= 0.5) {
    assert.equal(rd.said, r.child.prediction, "an assert says the CHILD's recorded word");
    assert.equal(rd.margin, r.child.margin, "with the child's recorded margin");
  } else {
    assert.ok(rd.esc, "below the floor it escalates");
  }

  s.model = "nano";
  const rd2 = h.readOf(s);
  assert.equal(rd2.said, r.parent.prediction, "the senior direct says the PARENT's recorded word");
  assert.equal(rd2.margin, r.parent.margin);
});

test("rack: knob detents are exactly the measured floors - nothing else is dialable", () => {
  const h = loadHook();
  const measuredFloors = measured.escalation.configs
    .map((c) => /^child\+parent@([\d.]+)$/.exec(c.config))
    .filter(Boolean).map((m) => parseFloat(m[1])).sort((a, b) => a - b);
  assert.deepEqual(h.detents, measuredFloors,
    "the knob physically cannot ask for an unmeasured number");
});

test("rack: the measured figures live on the knob, with their suite", () => {
  assert.ok(/measured at this floor/.test(js), "the tooltip owns the numbers");
  assert.ok(/_provenance.suite/.test(js.slice(js.indexOf("function knobTip"))),
    "and cites the suite they were measured on");
});

// ---------- the lamp: derived, honest, playable ----------------------------------

test("lamp: every state is a recount of the recorded records", () => {
  const h = loadHook();
  h.state.sensors.forEach((s) => { s.model = null; });
  h.derive();
  assert.equal(h.state.verdict.state, "off", "no model, no verdict colour");

  // dial a knob-fixable miss: c00 DRIFTING at floor 1.5 asserts wrong (margin
  // 1.88 < TOP) while... find one from the data instead of hardcoding:
  h.state.senior = true; h.state.operator = true;
  let found = null;
  for (const s of h.state.sensors) {
    for (const c of s.conds) {
      const r = measured.records[s.recIdx[c]];
      if (c !== "none" && r.child.margin >= 1.5 && r.child.margin < 2.0 &&
          r.child.prediction !== c && r.parent.prediction === c) found = { s, c };
    }
  }
  if (found) {
    h.state.sensors.forEach((s) => { s.on = false; s.model = null; });
    found.s.on = true; found.s.cond = found.c; found.s.model = "pico"; found.s.floor = 1.5;
    h.derive();
    assert.equal(h.state.verdict.state, "red", "a knob-fixable miss goes red");
    assert.ok(/[Rr]aise the FLOOR/.test(h.state.verdict.why),
      "escalation fires when margin < floor, so the honest advice is RAISE");
    found.s.floor = 2.0;
    h.derive();
    assert.notEqual(h.state.verdict.state, "red", "raising the knob fixes what it can");
  }
});

test("lamp: doubtful reads with no senior are counted, never silently caught", () => {
  const h = loadHook();
  h.state.senior = false;
  h.state.sensors.forEach((s) => { s.on = true; s.cond = s.conds[1] || "none"; s.model = "pico"; s.floor = 2.0; });
  h.derive();
  const t = h.state.verdict.totals;
  if (t.escalated > 0) {
    assert.equal(t.deadEnd, t.escalated, "every escalation is unheard without the senior");
  }
});

test("lamp: green at the ceiling never claims ALL CLEAR while faults were missed", () => {
  assert.ok(/AT CEILING/.test(js), "the ceiling label exists");
  assert.ok(/senior itself - no knob setting changes that/.test(js),
    "and the why-line attributes the remainder to the recorded parent, not to luck");
  const h = loadHook();
  h.state.senior = true; h.state.operator = true;
  h.state.sensors.forEach((s) => { s.on = true; s.cond = s.conds[1] || "none"; s.model = "pico"; s.floor = 2.0; });
  h.derive();
  const v = h.state.verdict;
  if (v.state === "green" && v.totals.missed > 0) {
    assert.equal(v.label, "AT CEILING");
  }
});

test("lamp: the founder's colours are literals, fenced to lamps and marks", () => {
  assert.ok(/FOUNDER MANDATE/.test(css), "the exception is documented where it lives");
  const hueRules = css.split("}").filter((b) => /#1E7A3C|#C99700/.test(b));
  for (const b of hueRules) {
    assert.ok(/wp-lamp|wp-read__mark/.test(b),
      `green/yellow may only colour lamps and read-marks, got: ${b.split("{")[0].trim()}`);
  }
  assert.ok(/STANDING BY|ALL CLEAR|DEGRADED|FAULTS MISSED/.test(js),
    "every lamp state carries a word");
  assert.ok(/[·●△⊗]/.test(js), "and an NE-107-style shape, so the verdict never rides on hue alone");
});

test("console: WHAT IT SAYS prints only recorded words, and the strip is capped", () => {
  assert.ok(htmlFlat.includes('id="wpReads"'), "the reads window exists");
  assert.ok(htmlFlat.includes('id="wpLamp2"'), "under the lamp window");
  assert.ok(htmlFlat.includes('id="wpStrip"'), "above the chart strip");
  assert.ok(/insertBefore\(li, strip.firstChild\)/.test(js), "newest strip line on top");
  assert.ok(/> 6\) strip.removeChild/.test(js), "capped - a strip, not a log");
  // reads lines carry the record's provenance on hover
  assert.ok(/recorded record " \+ rd.r.node_id/.test(js),
    "every printed line can show which record it recounts");
});

// ---------- interaction ------------------------------------------------------------

test("interaction: the dial is tap-to-step, drag, and keyboard - one state machine", () => {
  const dial = js.slice(js.indexOf("function drawDial"), js.indexOf("function render()"));
  assert.ok(/click/.test(dial) && /\+ 1\) % opts.values.length/.test(dial),
    "a plain tap steps to the next position - the tablet path");
  assert.ok(/setPointerCapture/.test(dial), "drag captures its pointer");
  assert.ok(/ArrowUp|ArrowRight/.test(dial), "arrows turn it");
  assert.ok(/pointercancel/.test(dial), "and drags clean up on pointercancel");
});

test("interaction: no HTML5 drag anywhere; the intake is the one drop target", () => {
  assert.ok(!/dragstart/.test(js), "we never initiate HTML5 drags");
  const dndUses = (js.match(/dataTransfer/g) || []).length;
  const dropBlock = js.slice(js.indexOf('addEventListener("drop"'), js.indexOf('addEventListener("drop"') + 600);
  assert.ok(dndUses > 0 && dndUses === (dropBlock.match(/dataTransfer/g) || []).length,
    "dataTransfer appears only in the intake's external-drop handler");
});

test("interaction: the escalation cable is dashed, and cables never catch a tap", () => {
  assert.ok(/\.wb-cable--esc \{ stroke-dasharray/.test(css));
  assert.ok(/\.wb-cables \{ position: absolute; inset: 0; pointer-events: none/.test(css),
    "the cable layer is decoration over live geometry, not a control");
});

// ---------- the palette ----------------------------------------------------------

test("palette: only tokens that exist are referenced", () => {
  const tokens = read("styles/tokens.css");
  const used = new Set((css.match(/var\(--[a-z0-9-]+/g) || []).map((v) => v.slice(4)));
  for (const name of used) {
    assert.ok(tokens.includes(`${name}:`), `${name} must be a real token`);
  }
});

test("palette: red is a signal, never a surface", async () => {
  const reds = (css.match(/var\(--live\)/g) || []).length;
  assert.ok(reds > 0 && reds < 30, `red is used ${reds} times; it must stay a glint`);
  const filled = [];
  for (const block of css.split("}")) {
    if (/background:\s*var\(--live\)/.test(block)) {
      const sel = (block.split("{")[0] || "").trim().split("\n").pop().trim();
      filled.push(sel);
    }
  }
  // Four rules may fill with red: the one primary action (the intake's SEND),
  // the masthead's spot plate, the radio's engraved lamp glint (both clipped
  // by tiny alpha masks), and the LAMP WINDOW in its red state - the
  // founder-mandated verdict light.
  assert.deepEqual(filled.sort(),
    ['.wm-masthead__spot', '.wp-lampwin[data-state="red"]', '.wp-run'].sort(),
    `got ${filled.join(", ")}`);
  const pin = (f, kb) => {
    const bytes = statSync(path.join(SRC, "assets/wave/" + f)).size;
    assert.ok(bytes < kb * 1024, `${f} is ${bytes} bytes; past ~${kb}KB it stops being a glint`);
  };
  pin("mesh-console-spot.png", 16);
  pin("radio-reader-spot.png", 16);
});

// ---------- accessibility ---------------------------------------------------------

test("a11y: levers are switches, dials are sliders", () => {
  assert.ok(/"role", "switch"/.test(js.replace(/setAttribute\(/g, '"role", ')) || js.includes('setAttribute("role", "switch")'),
    "the ON/OFF lever is a switch to assistive tech");
  assert.ok(js.includes('setAttribute("role", "slider")'), "the dial is a slider");
  assert.ok(/aria-valuetext/.test(js), "that speaks its position by name");
  assert.ok(/aria-checked/.test(js), "and the lever states its side");
});

test("a11y: there is a list mirror of the bench, always in the DOM", () => {
  assert.ok(htmlFlat.includes('id="wpMirror"'), "the mirror element exists");
  assert.ok(js.includes("renderMirror"), "and is kept in sync");
  assert.ok(/View this patch as a list/i.test(htmlFlat), "and is reachable");
});

test("a11y: reactions are announced once, and reduced motion is honoured", () => {
  assert.ok(htmlFlat.includes('aria-live="polite"'), "reactions are announced");
  assert.ok(!/role="application"/.test(htmlFlat), "never role=application - it kills browse mode");
  assert.ok(/screen-reader-only/.test(css), "the announcer is sr-only; the strip is the visible narrator");
  assert.ok(css.includes("prefers-reduced-motion"), "animations have a static equivalent");
  assert.ok(js.includes("REDUCED"), "and the module checks the preference too");
});

// ---------- structure + offline ----------------------------------------------------

test("deck: the rack, the senior rack, and the console all exist", () => {
  for (const id of ["wsRack", "wbBench", "wbSenior", "wbCables", "wpLamp2", "wpWhy", "wpReads", "wpStrip", "wbOp", "wpInspect", "wpProv"]) {
    assert.ok(htmlFlat.includes(`id="${id}"`), `${id} must exist`);
  }
});

test("deck: the data is fetched same-origin, so the deck works offline", () => {
  const fetches = js.match(/fetch\("([^"]+)"/g) || [];
  assert.ok(fetches.length >= 3, "the deck loads its snapshots");
  for (const f of fetches) assert.ok(!/https?:\/\//.test(f), `${f} must be same-origin`);
  assert.ok(js.includes("did not load"), "and says so honestly when they do not");
});

test("deck: the scope only draws for the scene that has committed samples", () => {
  assert.ok(/r.scene_id === PATCH.scene.scene_id/.test(js),
    "the trace is the recorded pump scene, drawn only for its own records");
});

// ---------- the translation shim (handoff 3) ----------------------------------------

test("shim: pasted bytes earn an envelope, never a result", () => {
  assert.ok(js.includes("DRAFT · NOT RUN"), "the draft state is named");
  assert.ok(js.includes("envelopeFor"), "what a paste earns is the request envelope");
  assert.ok(/pending export/.test(js.slice(js.indexOf("function envelopeFor"))),
    "the task frame text is not invented - its slot says pending, like the digest");
  assert.ok(/margin is a logprob difference/.test(js),
    "the reason no preview margin exists is stated");
});

test("shim: the classifier shows its evidence and refuses thin guesses", () => {
  assert.ok(js.includes("FINGERPRINTS"), "detection is fingerprint-based");
  assert.ok(/matched/.test(js), "each detection row prints what it matched");
  assert.ok(js.includes('"ambiguous"'), "near-ties are surfaced, not silently resolved");
  assert.ok(/refuses to guess on thin evidence/.test(js),
    "and the refusal is stated as the real system's behaviour");
});

test("shim: raw numbers never get a defaulted unit", () => {
  assert.ok(/NOT STATED IN THE WIRE/.test(js), "the unit's absence is stated, not papered over");
  assert.ok(!/unit: "Cel"|unit: "mm\/s"/.test(js.slice(js.indexOf("INTAKE"))),
    "no unit is ever assumed for pasted data");
});

test("shim: conversation never reaches a model", () => {
  assert.ok(js.includes('"talk"'), "small talk is a classified case");
  assert.ok(/answered by this interface, from the faceplate/.test(js),
    "and is answered by the interface");
  assert.ok(/never reaches a model/.test(js), "stated plainly");
});

test("shim: scenario words are recognised, and never free-texted to a model", () => {
  assert.ok(js.includes("scenario-asset"), "a described scenario is a classified case");
  assert.ok(/words are never sent to a Wave model/.test(js));
});

test("shim: the intake drawer exists and the samples are the recorded renders", () => {
  for (const id of ["wpIntake", "wpPaste", "wpDetMod", "wpDetShape", "wpDetFrame", "wpSend", "wpSamples"]) {
    assert.ok(htmlFlat.includes(`id="${id}"`), `${id} must exist`);
  }
  assert.ok(js.includes("sc.renders[mo]"), "samples load the committed renderer output, not mock text");
});

// ---------- the classifier, EXECUTED ------------------------------------------------

test("classifier: every committed render classifies as its own dialect", () => {
  const { classify } = loadHook();
  for (const [mo, text] of Object.entries(scene.renders)) {
    const v = classify(text);
    assert.equal(v.kind, "blob", `${mo} must classify as a wire blob`);
    assert.equal(v.mod, mo, `${mo} must be recognised as itself`);
  }
});

test("classifier: headerless bodies still classify - real plants do not send our banners", () => {
  const { classify } = loadHook();
  let recognised = 0;
  for (const [mo, text] of Object.entries(scene.renders)) {
    const tail = text.split("\n").slice(2).join("\n");
    const v = classify(tail);
    if (v.kind === "blob" && v.mod === mo) recognised++;
    assert.notEqual(v.kind, "talk",
      `${mo}'s own body must never be lectured about corpus dreams`);
  }
  assert.ok(recognised >= 5, `want most headerless bodies recognised, got ${recognised}/8`);
});

test("classifier: the spec's own phrases route correctly", () => {
  const { classify } = loadHook();
  assert.equal(classify("cavitating pump with a stuck vibration sensor").kind, "scenario-asset",
    "handoff 3 case 3's own example is recognised as a scenario about a catalogue asset");
  assert.equal(classify("gearbox running dry").kind, "scenario-asset",
    "a catalogue device's scenario is recognised even with the shelf gone");
  assert.equal(classify("71.2, 71.3, 71.1, 71.4, 71.2, 71.3, 71.5, 71.2").kind, "numbers",
    "the founder's bare list");
  const withUnit = classify("71.2 mm/s, 71.3 mm/s, 71.1 mm/s, 71.4 mm/s, 71.2 mm/s, 71.3 mm/s, 71.5 mm/s, 71.2 mm/s");
  assert.equal(withUnit.kind, "numbers", "volunteering the unit must not be punished");
  assert.equal(withUnit.unit, "mm/s", "and the stated unit is carried, not invented");
  assert.equal(classify("71.2, 71.3, 71.1").kind, "few-numbers",
    "a short series says it needs more, instead of falling to talk");
  assert.equal(classify("hi").kind, "talk");
  assert.equal(classify("what are you").kind, "talk");
});

// ---------- the engraved plates ------------------------------------------------------

test("plates: every mask plate ships as an ink/spot pair", () => {
  for (const base of ["sensor-gauge", "radio-reader", "radio-senior", "radio-pocket", "mesh-console", "tape-reader"]) {
    for (const half of ["ink", "spot"]) {
      statSync(path.join(SRC, `assets/wave/${base}-${half}.png`));
    }
  }
});

test("plates: illustrations are labelled illustration, and sit on non-data surfaces", () => {
  assert.ok(/aria-hidden/.test(js.slice(js.indexOf("ws-bay__art"))),
    "the sensor engraving is decoration to assistive tech");
  assert.ok(/ILLUSTRATION|illustration only|re-inked per theme \(illustration/.test(js + css + htmlFlat),
    "somewhere the surface says these are illustrations, not data");
});
